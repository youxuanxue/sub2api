#!/usr/bin/env python3
"""Inventory / pack entry-host TLS materials for COS host-config offsite.

Contract: docs/approved/design-prod-entry-multi-app.md §2.1
Decision (2026-11-19): plaintext tar.gz on COS (SSE-COS only); no operator RSA.
Door = CAM / cos-archive.env who can read prod/host-config/. Avoids laptop-only
key custody (same class of failure as LUKS unlock materials on one broken machine).

Default is inventory-only. ``pack`` writes a local .tar.gz (never uploads).
``plan-upload`` packs to a temp file and prints upload_host_config plan JSON
(still no COS write unless the operator runs upload with --apply).
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import socket
import subprocess
import sys
import tarfile
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

_DIR = Path(__file__).resolve().parent

DEFAULT_ROOT = Path("/var/lib/tokenkey")
# Paths relative to root that belong in the entry offsite pack.
REL_PATHS = (
    "caddy/Caddyfile",
    "caddy/etc/Caddyfile",
    "caddy/data",
    "caddy/config",
    "caddy/legal",
    "data/legal",
)
LABELS = {
    "prod": "prod-entry",
    "cn-prod": "cn-prod-entry",
}


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _cert_meta(path: Path) -> dict[str, str] | None:
    """Return notBefore/notAfter/subject for a PEM cert; never dump the key."""
    try:
        text = path.read_text(encoding="utf-8", errors="ignore")
    except OSError:
        return None
    if "BEGIN CERTIFICATE" not in text:
        return None
    try:
        out = subprocess.check_output(
            [
                "openssl",
                "x509",
                "-noout",
                "-subject",
                "-dates",
                "-in",
                str(path),
            ],
            text=True,
            stderr=subprocess.DEVNULL,
        )
    except (OSError, subprocess.CalledProcessError):
        return {"path": str(path), "parse": "failed"}
    meta: dict[str, str] = {"path": str(path)}
    for line in out.splitlines():
        if "=" in line:
            key, _, value = line.partition("=")
            meta[key.strip()] = value.strip()
    return meta


def discover(root: Path) -> dict[str, Any]:
    """Read-only inventory of entry materials under root."""
    entries: list[dict[str, Any]] = []
    certs: list[dict[str, str]] = []
    missing: list[str] = []
    for rel in REL_PATHS:
        path = root / rel
        if not path.exists():
            missing.append(rel)
            continue
        if path.is_file():
            entries.append(
                {
                    "path": rel,
                    "kind": "file",
                    "bytes": path.stat().st_size,
                    "sha256": _sha256_file(path),
                }
            )
            continue
        file_count = 0
        total = 0
        for child in sorted(path.rglob("*")):
            if not child.is_file():
                continue
            file_count += 1
            total += child.stat().st_size
            if child.suffix.lower() in {".crt", ".pem"} or child.name == "certificate":
                meta = _cert_meta(child)
                if meta:
                    certs.append(meta)
        entries.append(
            {
                "path": rel,
                "kind": "dir",
                "files": file_count,
                "bytes": total,
            }
        )
    return {
        "ok": True,
        "hostname": socket.gethostname(),
        "root": str(root),
        "utc": datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ"),
        "entries": entries,
        "certificates": certs,
        "missing_optional": missing,
        "has_caddy_material": any(
            e["path"].startswith("caddy/") and e.get("bytes", 0) > 0 for e in entries
        ),
    }


def _collect_members(root: Path) -> list[Path]:
    members: list[Path] = []
    for rel in REL_PATHS:
        path = root / rel
        if not path.exists():
            continue
        if path.is_file():
            members.append(path)
            continue
        for child in sorted(path.rglob("*")):
            if child.is_file():
                members.append(child)
    if not members:
        raise SystemExit(f"no entry host-config material under {root}")
    return members


def build_tar(root: Path, tar_path: Path, inventory: dict[str, Any]) -> None:
    members = _collect_members(root)
    with tarfile.open(tar_path, "w:gz") as archive:
        manifest = tar_path.parent / "manifest.json"
        manifest.write_text(
            json.dumps(inventory, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        archive.add(manifest, arcname="manifest.json")
        for path in members:
            archive.add(path, arcname=str(path.relative_to(root)))


def pack(root: Path, out_path: Path, *, site: str) -> dict[str, Any]:
    inventory = discover(root)
    if not inventory["has_caddy_material"]:
        raise SystemExit("refusing pack: no caddy material found")
    out_path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="tk-host-config-") as tmp:
        tar_path = Path(tmp) / "entry-host-config.tar.gz"
        build_tar(root, tar_path, inventory)
        # Atomic replace into the destination.
        tmp_out = out_path.with_suffix(out_path.suffix + ".tmp")
        tmp_out.write_bytes(tar_path.read_bytes())
        os.chmod(tmp_out, 0o600)
        tmp_out.replace(out_path)
    return {
        "ok": True,
        "site": site,
        "label": LABELS[site],
        "format": "tar.gz",
        "encryption": "none-operator",
        "cos_sse": "AES256-on-upload",
        "inventory": inventory,
        "package": {
            "path": str(out_path),
            "bytes": out_path.stat().st_size,
            "sha256": _sha256_file(out_path),
        },
        "upload_hint": (
            f"python3 ops/archive/upload_host_config.py --file {out_path} "
            f"--label {LABELS[site]}   # plan; add --apply only after approval"
        ),
    }


def plan_upload(root: Path, *, site: str) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="tk-host-config-plan-") as tmp:
        out_path = Path(tmp) / f"{LABELS[site]}.tar.gz"
        packed = pack(root, out_path, site=site)
        plan_json = subprocess.check_output(
            [
                sys.executable,
                str(_DIR / "upload_host_config.py"),
                "--file",
                str(out_path),
                "--label",
                LABELS[site],
            ],
            text=True,
        )
    packed["upload_plan"] = json.loads(plan_json)
    return packed


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "command",
        choices=("inventory", "pack", "plan-upload"),
        help="inventory=read-only; pack=local tar.gz; plan-upload=pack+upload plan",
    )
    parser.add_argument("--site", choices=sorted(LABELS), default="prod")
    parser.add_argument(
        "--root",
        type=Path,
        default=Path(os.environ.get("TK_ENTRY_HOST_ROOT", str(DEFAULT_ROOT))),
    )
    parser.add_argument(
        "--out",
        type=Path,
        help="output path for pack (default: ./<label>-<utc>.tar.gz)",
    )
    args = parser.parse_args(argv)

    if args.command == "inventory":
        print(json.dumps(discover(args.root), ensure_ascii=False, indent=2))
        return 0

    if args.command == "pack":
        stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        out = args.out or Path(f"{LABELS[args.site]}-{stamp}.tar.gz")
        result = pack(args.root, out, site=args.site)
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0

    result = plan_upload(args.root, site=args.site)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except BrokenPipeError:
        raise SystemExit(0) from None
