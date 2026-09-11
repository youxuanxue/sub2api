#!/usr/bin/env python3
"""Manage scoped machine credentials using a human admin JWT. Never print keys."""

import argparse
import json
import os
from pathlib import Path
import sys
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # A redirect must never forward the human administrator JWT.
        return None


def request(base_url, jwt, method, suffix="", payload=None):
    url = base_url.rstrip("/") + "/api/v1/admin/settings/machine-admin-keys" + suffix
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(
        url, data=data, method=method,
        headers={"Authorization": "Bearer " + jwt, "Content-Type": "application/json"},
    )
    opener = urllib.request.build_opener(NoRedirect())
    try:
        with opener.open(req, timeout=30) as response:
            result = json.load(response)
    except urllib.error.HTTPError as exc:
        # Don't echo arbitrary server bodies, URLs or credentials in errors.
        raise RuntimeError(f"Administrator API returned HTTP {exc.code}; check session/MFA and endpoint") from None
    except (urllib.error.URLError, TimeoutError, ValueError) as exc:
        raise RuntimeError("Administrator API request failed; a mutation may have completed, inspect the key list before retrying") from exc
    if not isinstance(result, dict) or result.get("code") != 0 or "data" not in result:
        raise RuntimeError("Unexpected administrator API response")
    return result["data"]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True, help="Explicit target; HTTPS except localhost test fixtures")
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("list", help="List key metadata and the server-owned permission/route registry")
    create = commands.add_parser("create", help="Issue a scoped credential into a new private file")
    create.add_argument("--name", required=True)
    create.add_argument("--scope", action="append", required=True)
    create.add_argument("--ttl-hours", type=int, default=720)
    create.add_argument("--key-file", type=Path, required=True, help="Must not exist; plaintext credential, mode 0600")
    revoke = commands.add_parser("revoke")
    revoke.add_argument("--id", required=True)
    args = parser.parse_args(argv)
    target = urllib.parse.urlsplit(args.base_url)
    local = target.hostname in {"localhost", "127.0.0.1", "::1"}
    if (target.scheme != "https" and not (target.scheme == "http" and local)) or not target.hostname or target.username or target.password or target.query or target.fragment or target.path not in {"", "/"}:
        parser.error("base-url must be an HTTPS origin (HTTP allowed only on localhost)")
    jwt = os.environ.get("TOKENKEY_ADMIN_JWT", "").strip()
    if not jwt:
        parser.error("Set TOKENKEY_ADMIN_JWT to a human admin session token")
    if args.command == "revoke" and (len(args.id) != 32 or any(c not in "0123456789abcdef" for c in args.id)):
        parser.error("id must be the 32-character credential ID returned by list/create")
    if args.command == "create" and not 1 <= args.ttl_hours <= 2160:
        parser.error("ttl-hours must be 1 to 2160")
    try:
        if args.command == "list":
            result = request(args.base_url, jwt, "GET")
        elif args.command == "revoke":
            result = request(args.base_url, jwt, "DELETE", "/" + args.id)
        else:
            # Reserve the destination before making a non-idempotent request.
            # Exclusive creation rejects existing files and symlinks.
            fd = os.open(args.key_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, "w", encoding="utf-8") as output:
                issued = request(args.base_url, jwt, "POST", payload={
                    "name": args.name, "scopes": args.scope, "ttl_hours": args.ttl_hours,
                })
                # Print metadata first to preserve the revocation ID if disk I/O
                # fails. Never serialize the one-time response's key to stdout.
                result = issued["credential"]
                print(json.dumps(result, ensure_ascii=False), flush=True)
                output.write(issued["key"] + "\n")
                output.flush()
                os.fsync(output.fileno())
            return 0
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0
    except (OSError, RuntimeError, KeyError, TypeError):
        print("Machine key operation failed. Check the target, session/MFA and private output file; use list to reconcile any issued key before retrying.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
