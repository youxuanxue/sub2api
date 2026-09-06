#!/usr/bin/env python3
"""Evaluate healthy account group bindings from live model_mapping peer evidence.

The checker deliberately does not own a model-to-group catalog. For each explicit
client model, it derives the established groups from other healthy, schedulable
accounts in the same snapshot. This keeps new models and operator-created groups
out of a second hand-maintained policy table.
"""
from __future__ import annotations

import argparse
import json
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any, TextIO


SCHEMA_VERSION = 1


def _positive_int(value: Any) -> int | None:
    if isinstance(value, bool):
        return None
    try:
        parsed = int(value)
    except (TypeError, ValueError):
        return None
    return parsed if parsed > 0 else None


def _model_ids(account: dict[str, Any]) -> list[str]:
    raw = account.get("model_ids")
    if not isinstance(raw, list):
        return []
    return sorted({str(value).strip() for value in raw if str(value).strip()})


def _bindings(account: dict[str, Any]) -> list[dict[str, Any]]:
    raw = account.get("groups")
    if not isinstance(raw, list):
        return []
    result: list[dict[str, Any]] = []
    seen: set[int] = set()
    for item in raw:
        if not isinstance(item, dict):
            continue
        group_id = _positive_int(item.get("id"))
        if group_id is None or group_id in seen:
            continue
        seen.add(group_id)
        result.append(
            {
                "id": group_id,
                "name": str(item.get("name") or ""),
                "status": str(item.get("status") or ""),
            }
        )
    return sorted(result, key=lambda item: item["id"])


def evaluate_snapshot(snapshot: dict[str, Any]) -> dict[str, Any]:
    accounts_raw = snapshot.get("accounts")
    groups_raw = snapshot.get("groups")
    if not isinstance(accounts_raw, list) or not isinstance(groups_raw, list):
        raise ValueError("snapshot must contain accounts and groups arrays")

    group_names: dict[int, str] = {}
    for item in groups_raw:
        if not isinstance(item, dict):
            continue
        group_id = _positive_int(item.get("id"))
        if group_id is not None and str(item.get("status") or "") == "active":
            group_names[group_id] = str(item.get("name") or "")

    accounts: list[dict[str, Any]] = []
    skipped_ids: list[int] = []
    for raw in accounts_raw:
        if not isinstance(raw, dict):
            continue
        account_id = _positive_int(raw.get("id"))
        if account_id is None:
            continue
        models = _model_ids(raw)
        bindings = _bindings(raw)
        active_group_ids = sorted(
            {item["id"] for item in bindings if item["id"] in group_names}
        )
        account = {
            "id": account_id,
            "name": str(raw.get("name") or ""),
            "platform": str(raw.get("platform") or ""),
            "model_ids": models,
            "active_group_ids": active_group_ids,
            "inactive_groups": [
                {"id": item["id"], "name": item["name"], "status": item["status"]}
                for item in bindings
                if item["id"] not in group_names
            ],
        }
        if models:
            accounts.append(account)
        else:
            skipped_ids.append(account_id)

    model_group_accounts: dict[str, dict[int, set[int]]] = defaultdict(
        lambda: defaultdict(set)
    )
    for account in accounts:
        for model_id in account["model_ids"]:
            for group_id in account["active_group_ids"]:
                model_group_accounts[model_id][group_id].add(account["id"])

    findings: list[dict[str, Any]] = []
    evaluated_ids: list[int] = []
    inconclusive_model_count = 0

    for account in sorted(accounts, key=lambda item: item["id"]):
        evaluated_ids.append(account["id"])
        active_group_ids = set(account["active_group_ids"])
        candidate_models: dict[int, set[str]] = defaultdict(set)
        candidate_peers: dict[int, set[int]] = defaultdict(set)
        inconclusive_models: list[str] = []

        for model_id in account["model_ids"]:
            has_peer_group = False
            for group_id, account_ids in sorted(model_group_accounts[model_id].items()):
                peer_ids = sorted(account_ids - {account["id"]})
                if not peer_ids:
                    continue
                has_peer_group = True
                candidate_models[group_id].add(model_id)
                candidate_peers[group_id].update(peer_ids)
            if not has_peer_group:
                inconclusive_models.append(model_id)

        inconclusive_model_count += len(inconclusive_models)
        candidates = [
            {
                "group_id": group_id,
                "group_name": group_names.get(group_id, ""),
                "matching_models": sorted(candidate_models[group_id]),
                "peer_account_count": len(candidate_peers[group_id]),
            }
            for group_id in candidate_models
        ]
        candidates.sort(
            key=lambda item: (
                -len(item["matching_models"]),
                -item["peer_account_count"],
                item["group_id"],
            )
        )

        base = {
            "account_id": account["id"],
            "account_name": account["name"],
            "platform": account["platform"],
            "active_groups": [
                {"id": group_id, "name": group_names[group_id]}
                for group_id in account["active_group_ids"]
            ],
            "model_ids": account["model_ids"],
            "candidate_groups": candidates,
        }

        if not active_group_ids:
            findings.append(
                {
                    **base,
                    "code": "no_active_group",
                    "inactive_groups": account["inactive_groups"],
                }
            )
            continue

        candidate_group_ids = set(candidate_models)
        if candidate_group_ids and active_group_ids.isdisjoint(candidate_group_ids):
            findings.append(
                {**base, "code": "model_group_peer_mismatch"}
            )

    ungrouped_count = sum(
        1 for finding in findings if finding["code"] == "no_active_group"
    )
    mismatch_count = sum(
        1
        for finding in findings
        if finding["code"] == "model_group_peer_mismatch"
    )
    return {
        "schema_version": SCHEMA_VERSION,
        "verdict": "review" if findings else "aligned",
        "basis": "healthy_schedulable_explicit_model_mapping_peer_groups",
        "summary": {
            "healthy_schedulable_account_count": len(accounts_raw),
            "explicit_mapping_account_count": len(accounts),
            "skipped_no_explicit_mapping_count": len(skipped_ids),
            "finding_account_count": len(findings),
            "ungrouped_account_count": ungrouped_count,
            "peer_mismatch_account_count": mismatch_count,
            "inconclusive_model_count": inconclusive_model_count,
        },
        "coverage": {
            "evaluated_account_ids": evaluated_ids,
            "skipped_no_explicit_mapping_account_ids": sorted(skipped_ids),
        },
        "findings": findings,
    }


def _load_snapshot(path: str) -> dict[str, Any]:
    if path == "-":
        return json.load(sys.stdin)
    return json.loads(Path(path).read_text(encoding="utf-8"))


def main(argv: list[str] | None = None, out: TextIO = sys.stdout) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snapshot", required=True, help="snapshot JSON path or -")
    args = parser.parse_args(argv)
    try:
        report = evaluate_snapshot(_load_snapshot(args.snapshot))
    except (OSError, ValueError, TypeError, json.JSONDecodeError) as exc:
        report = {"schema_version": SCHEMA_VERSION, "verdict": "setup_error", "error": str(exc)}
        json.dump(report, out, ensure_ascii=False, separators=(",", ":"))
        out.write("\n")
        return 2
    json.dump(report, out, ensure_ascii=False, separators=(",", ":"))
    out.write("\n")
    return 1 if report["verdict"] == "review" else 0


if __name__ == "__main__":
    raise SystemExit(main())
