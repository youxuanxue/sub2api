#!/usr/bin/env python3
"""Evaluate supplier-source managed-account projection drift from a DB snapshot.

The live probe supplies ``TOTP_ENCRYPTION_KEY`` only inside the remote host so
credential fingerprints can be compared without returning account API keys.
Scheduling fields are deliberately outside this contract: status and
schedulable are runtime decisions, not supplier projection drift.
"""
from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import sys
import urllib.parse
from pathlib import Path
from typing import Any, TextIO


MEDIA_ONLY_CHANNEL_TYPES = {54}
DEFAULT_ACCOUNT_CONCURRENCY = 1000


def _positive_int(value: Any) -> int | None:
    if isinstance(value, bool):
        return None
    try:
        parsed = int(value)
    except (TypeError, ValueError):
        return None
    return parsed if parsed > 0 else None


def _discount_band(purchase_ratio: Any) -> int:
    if purchase_ratio is None or float(purchase_ratio) == 1:
        return 6
    ratio = float(purchase_ratio)
    if ratio < 0.2:
        return 1
    if ratio < 0.4:
        return 2
    if ratio < 0.6:
        return 3
    if ratio < 0.8:
        return 4
    return 5


def _normalized_endpoint(value: Any) -> str:
    raw = str(value or "").strip()
    parsed = urllib.parse.urlsplit(raw)
    host = (parsed.hostname or "").lower()
    if host == "qianfan.baidubce.com":
        return "https://qianfan.baidubce.com"
    netloc = host
    if parsed.port:
        netloc += f":{parsed.port}"
    return urllib.parse.urlunsplit(
        (parsed.scheme.lower(), netloc, parsed.path.rstrip("/"), "", "")
    )


def _protocol_url(base_url: str, protocol: str) -> str:
    parsed = urllib.parse.urlsplit(base_url)
    path = parsed.path.rstrip("/")
    if protocol == "messages":
        suffix = "/v1/messages"
        complete = path.endswith("/messages")
    else:
        suffix = "/v1/chat/completions"
        complete = path.endswith("/chat/completions")
    if not complete:
        if path.endswith("/v1") and suffix.startswith("/v1/"):
            suffix = suffix[3:]
        path += suffix
    return urllib.parse.urlunsplit(
        (parsed.scheme.lower(), parsed.netloc.lower(), path or "/", "", "")
    )


def _mapping(value: Any) -> dict[str, str] | None:
    if not isinstance(value, dict):
        return None
    result: dict[str, str] = {}
    for key, target in value.items():
        if not isinstance(key, str) or not isinstance(target, str):
            return None
        result[key] = target
    return result


def _credential_matches(api_key: Any, fingerprint: Any, key: str) -> bool:
    digest = "hmac-sha256:" + hmac.new(
        key.encode("utf-8"),
        str(api_key or "").strip().encode("utf-8"),
        hashlib.sha256,
    ).hexdigest()
    return hmac.compare_digest(digest, str(fingerprint or ""))


def _expected_protocol(source: dict[str, Any]) -> tuple[str, str, str]:
    channel_type = int(source.get("channel_type") or 0)
    endpoint = _normalized_endpoint(source.get("endpoint"))
    if channel_type == 14:
        return "messages", "anthropic", endpoint
    if channel_type == 46:
        return "chat_completions", "chat_completions", endpoint + "/v2/chat/completions"
    return "chat_completions", "chat_completions", endpoint


def _protocol_differences(
    account: dict[str, Any], source: dict[str, Any], desired_mapping: dict[str, str]
) -> list[str]:
    capability_id = account.get("capability_id")
    capability_key = account.get("capability_key")
    supported = account.get("supported_protocols") or []
    if not desired_mapping or int(source.get("channel_type") or 0) in MEDIA_ONLY_CHANNEL_TYPES:
        if capability_id is not None or capability_key or supported:
            return ["protocol_capability_link"]
        return []

    protocol, credential_protocol, expected_api_base = _expected_protocol(source)
    credentials = account.get("credentials") or {}
    differences: list[str] = []
    api_bases = credentials.get("api_base_urls") or {}
    if credentials.get("protocol_endpoints_exclusive") is not True:
        differences.append("protocol_endpoints_exclusive")
    if (
        not isinstance(api_bases, dict)
        or set(api_bases) != {credential_protocol}
        or _normalized_endpoint(api_bases.get(credential_protocol))
        != _normalized_endpoint(expected_api_base)
    ):
        differences.append("api_base_urls")
    if not capability_id or not capability_key:
        differences.append("protocol_capability_link")
        return differences

    identity = account.get("capability_identity") or {}
    identity_protocols = identity.get("protocol_endpoints") or {}
    actual_url = ""
    if isinstance(identity_protocols, dict):
        actual_url = str((identity_protocols.get(protocol) or {}).get("url") or "")
    if (
        identity.get("platform") != "newapi"
        or identity.get("endpoint_profile") != "custom_api_key"
        or str(identity.get("channel_type")) != str(source.get("channel_type"))
        or not isinstance(identity_protocols, dict)
        or set(identity_protocols) != {protocol}
        or actual_url.rstrip("/") != _protocol_url(expected_api_base, protocol).rstrip("/")
        or identity.get("upstream_request_profile") != "openai_json_v1"
        or (identity.get("routing_headers") or {}) != {}
    ):
        differences.append("protocol_capability_identity")
    if supported != [protocol]:
        differences.append("supported_protocols")
    evidence = account.get("probe_evidence") or {}
    if (
        evidence.get("initial_probe_completed") is not True
        or evidence.get("identity_conflict") is True
        or account.get("capability_identity_conflict") is True
    ):
        differences.append("protocol_probe_evidence")
    return differences


def evaluate_snapshot(snapshot: dict[str, Any], fingerprint_key: str) -> dict[str, Any]:
    sources = snapshot.get("sources") or []
    accounts = snapshot.get("accounts") or []
    if not isinstance(sources, list) or not isinstance(accounts, list):
        raise ValueError("snapshot must contain sources and accounts arrays")
    if not fingerprint_key:
        raise ValueError("TOTP_ENCRYPTION_KEY is unavailable")

    source_ids = {_positive_int(source.get("id")) for source in sources}
    source_ids.discard(None)
    accounts_by_source: dict[int, list[dict[str, Any]]] = {}
    orphans: list[dict[str, Any]] = []
    for account in accounts:
        extra = account.get("extra") or {}
        source_id = _positive_int(extra.get("supplier_source_id"))
        if source_id not in source_ids:
            orphans.append(
                {
                    "account_id": account.get("id"),
                    "supplier_source_id": extra.get("supplier_source_id"),
                    "differences": ["supplier_source_id"],
                }
            )
            continue
        accounts_by_source.setdefault(source_id, []).append(account)

    source_results: list[dict[str, Any]] = []
    for source in sources:
        source_id = _positive_int(source.get("id"))
        if source_id is None:
            raise ValueError("supplier source id must be positive")
        targets: dict[int, dict[str, str]] = {}
        for model in source.get("models") or []:
            band = _discount_band(model.get("purchase_ratio"))
            targets.setdefault(band, {})[str(model.get("client_model_id") or "")] = str(
                model.get("upstream_model_id") or ""
            )

        managed = accounts_by_source.get(source_id, [])
        by_band: dict[int, list[dict[str, Any]]] = {}
        source_issues: list[dict[str, Any]] = []
        account_results: list[dict[str, Any]] = []
        duplicate_account_ids: set[Any] = set()
        for account in managed:
            band = _positive_int((account.get("extra") or {}).get("supplier_discount_band"))
            if band is None or band > 6:
                source_issues.append(
                    {"code": "invalid_band", "account_id": account.get("id")}
                )
                account_results.append(
                    {
                        "account_id": account.get("id"),
                        "band": band,
                        "aligned": False,
                        "differences": ["supplier_discount_band"],
                    }
                )
                continue
            by_band.setdefault(band, []).append(account)

        for band, rows in sorted(by_band.items()):
            if len(rows) > 1:
                ids = [row.get("id") for row in rows]
                duplicate_account_ids.update(ids)
                source_issues.append({"code": "duplicate_band", "band": band, "account_ids": ids})
        for band, mapping in sorted(targets.items()):
            if band not in by_band:
                source_issues.append(
                    {"code": "missing_account", "band": band, "model_count": len(mapping)}
                )

        endpoint = _normalized_endpoint(source.get("endpoint"))
        expected_concurrency = int(source.get("account_concurrency") or 0) or DEFAULT_ACCOUNT_CONCURRENCY
        for band, rows in sorted(by_band.items()):
            desired_mapping = targets.get(band, {})
            desired_name = f"{source.get('supplier_name')}/{source.get('supplier_lane')} · 档位 {band}"
            expected_priority = int(source.get("base_priority") or 0) + band * 10
            for account in rows:
                credentials = account.get("credentials") or {}
                actual_mapping = _mapping(credentials.get("model_mapping"))
                differences: list[str] = []
                if account.get("id") in duplicate_account_ids:
                    differences.append("supplier_discount_band")
                if account.get("name") != desired_name:
                    differences.append("name")
                if account.get("platform") != "newapi":
                    differences.append("platform")
                if account.get("type") != "apikey":
                    differences.append("type")
                if int(account.get("channel_type") or 0) != int(source.get("channel_type") or 0):
                    differences.append("channel_type")
                if _normalized_endpoint(credentials.get("base_url")) != endpoint:
                    differences.append("endpoint")
                if not _credential_matches(
                    credentials.get("api_key"), source.get("credential_fingerprint"), fingerprint_key
                ):
                    differences.append("credential")
                if actual_mapping != desired_mapping:
                    differences.append("model_mapping")
                if int(account.get("priority") or 0) != expected_priority:
                    differences.append("priority")
                if int(account.get("concurrency") or 0) != expected_concurrency:
                    differences.append("concurrency")
                differences.extend(_protocol_differences(account, source, desired_mapping))
                account_results.append(
                    {
                        "account_id": account.get("id"),
                        "band": band,
                        "aligned": not differences,
                        "differences": differences,
                    }
                )

        aligned = not source_issues and all(row["aligned"] for row in account_results)
        source_results.append(
            {
                "source_id": source_id,
                "supplier": source.get("supplier_name"),
                "lane": source.get("supplier_lane"),
                "aligned": aligned,
                "issues": source_issues,
                "accounts": account_results,
            }
        )

    account_rows = [account for source in source_results for account in source["accounts"]]
    drift = bool(orphans) or any(not source["aligned"] for source in source_results)
    return {
        "verdict": "drift" if drift else "aligned",
        "ignored_runtime_fields": ["status", "schedulable"],
        "summary": {
            "source_count": len(source_results),
            "misaligned_source_count": sum(not source["aligned"] for source in source_results),
            "managed_account_count": len(accounts),
            "misaligned_account_count": sum(not account["aligned"] for account in account_rows),
            "orphan_account_count": len(orphans),
        },
        "sources": source_results,
        "orphans": orphans,
    }


def _read_snapshot(path: str) -> dict[str, Any]:
    handle: TextIO
    if path == "-":
        handle = sys.stdin
        return json.load(handle)
    with Path(path).open(encoding="utf-8") as handle:
        return json.load(handle)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snapshot", default="-", help="snapshot JSON path, or - for stdin")
    args = parser.parse_args(argv)
    try:
        report = evaluate_snapshot(
            _read_snapshot(args.snapshot), os.environ.get("TOTP_ENCRYPTION_KEY", "")
        )
    except (OSError, ValueError, TypeError, json.JSONDecodeError) as exc:
        print(json.dumps({"verdict": "setup_error", "error": str(exc)}, ensure_ascii=False))
        return 2
    print(json.dumps(report, ensure_ascii=False, separators=(",", ":")))
    return 0 if report["verdict"] == "aligned" else 1


if __name__ == "__main__":
    raise SystemExit(main())
