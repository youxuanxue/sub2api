#!/usr/bin/env python3
"""Report production Feishu config drift without changing production state."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import subprocess
import sys
from typing import Any

if __package__:
    from .feishu_delivery import DeliveryError, post_feishu
else:
    from feishu_delivery import DeliveryError, post_feishu


BASE_LABELS = {
    "prod-feishu-config": ("BFD4F2", "Production Feishu configuration"),
    "automated": ("C5DEF5", "Automated signal"),
    "needs-triage": ("FBCA04", "Needs human triage"),
}


def sh(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, text=True, check=check, capture_output=True)


def signature_label(codes: list[str]) -> str:
    signature = "prod-feishu-config|" + "|".join(sorted(codes))
    return "feishu-config-sig:" + hashlib.sha256(signature.encode()).hexdigest()[:12]


def load_probe(text: str) -> dict[str, Any]:
    """Parse probe stdout. Accept a single JSON object, or the first object when
    fan-out accidentally concatenates multiple objects on one stream."""
    payload = (text or "").strip()
    if not payload:
        return {}
    try:
        value = json.loads(payload)
    except json.JSONDecodeError:
        decoder = json.JSONDecoder()
        value, _offset = decoder.raw_decode(payload)
    if not isinstance(value, dict):
        raise ValueError("probe output must be a JSON object")
    return value


def findings(probe: dict[str, Any], probe_exit_code: int) -> list[str]:
    if probe_exit_code:
        return [f"probe_transport_failed:{probe_exit_code}"]
    db = probe.get("db") if isinstance(probe.get("db"), dict) else {}
    host = probe.get("host_env") if isinstance(probe.get("host_env"), dict) else {}
    checks = {
        "db_row_missing": db.get("row_present") is not True,
        "db_feishu_disabled": db.get("feishu_enabled") is not True,
        "db_webhook_missing": db.get("feishu_webhook_present") is not True,
        "db_secret_missing": db.get("feishu_secret_present") is not True,
        "host_webhook_missing": host.get("feishu_webhook_present") is not True,
        "host_secret_missing": host.get("feishu_secret_present") is not True,
    }
    return [code for code, failed in checks.items() if failed]


def issue_body(codes: list[str], *, run_url: str, run_id: str, probe_exit_code: int) -> str:
    lines = [
        "## Production Feishu alert configuration drift",
        "",
        "The daily read-only watchdog found a mismatch or missing value.",
        "",
        f"- Findings: `{', '.join(codes)}`",
        f"- Probe exit code: `{probe_exit_code}`",
        f"- Workflow run: {run_url or 'n/a'}",
        f"- Run ID: `{run_id or 'n/a'}`",
        "",
        "The watchdog does not modify production. Use the manual repair workflow after reviewing the finding.",
    ]
    return "\n".join(lines) + "\n"


def ensure_labels(signature: str) -> None:
    for name, (color, description) in BASE_LABELS.items():
        sh(["gh", "label", "create", name, "--color", color, "--description", description], check=False)
    sh(["gh", "label", "create", signature, "--color", "D4C5F9",
        "--description", "Stable Feishu configuration finding signature"], check=False)


def open_issue(signature: str, body: str) -> dict[str, Any]:
    ensure_labels(signature)
    existing = json.loads(sh([
        "gh", "issue", "list", "--label", signature, "--state", "open",
        "--json", "number", "--limit", "20",
    ]).stdout or "[]")
    if existing:
        number = str(existing[0]["number"])
        sh(["gh", "issue", "comment", number, "--body", body])
        return {"status": "updated", "number": int(number)}
    created = sh([
        "gh", "issue", "create",
        "--title", "[prod-feishu-config] production Feishu alert configuration drift",
        "--body", body,
        "--label", ",".join([*BASE_LABELS, signature]),
    ]).stdout.strip()
    return {"status": "created", "url": created}


def recover_issues(*, run_url: str) -> bool:
    rows = json.loads(sh([
        "gh", "issue", "list", "--label", "prod-feishu-config", "--state", "open",
        "--json", "number,labels", "--limit", "100",
    ]).stdout or "[]")
    recovered = False
    for row in rows:
        labels = {label["name"] for label in row.get("labels") or []}
        if not any(label.startswith("feishu-config-sig:") for label in labels):
            continue
        sh(["gh", "issue", "close", str(row["number"]), "--comment",
            f"Configuration check recovered on {run_url or 'the latest scheduled run'}."])
        recovered = True
    return recovered


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--probe-file", type=pathlib.Path, required=True)
    parser.add_argument("--probe-exit-code", type=int, default=0)
    args = parser.parse_args(argv)

    try:
        probe = load_probe(args.probe_file.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError, ValueError) as exc:
        probe = {}
        if args.probe_exit_code == 0:
            args.probe_exit_code = 1
        print(f"probe output unavailable: {type(exc).__name__}", file=sys.stderr)

    codes = findings(probe, args.probe_exit_code)
    run_url = (
        os.environ.get("GITHUB_SERVER_URL", "https://github.com")
        + "/" + os.environ.get("GITHUB_REPOSITORY", "")
        + "/actions/runs/" + os.environ.get("GITHUB_RUN_ID", "")
    )
    if not codes:
        recover_issues(run_url=run_url)
        print("feishu config check passed")
        return 0

    webhook = os.environ.get("TK_FEISHU_WEBHOOK_URL", "")
    secret = os.environ.get("TK_FEISHU_SIGNING_SECRET", "")
    if not webhook or not secret:
        print("Feishu delivery credentials are missing", file=sys.stderr)
        return 2
    try:
        post_feishu(
            "生产飞书告警配置异常\n"
            f"发现项：{', '.join(codes)}\n"
            f"GitHub Actions：{run_url}",
            webhook_url=webhook,
            signing_secret=secret,
        )
    except DeliveryError as exc:
        print(f"Feishu delivery failed: {exc}", file=sys.stderr)
        return 2
    body = issue_body(
        codes,
        run_url=run_url,
        run_id=os.environ.get("GITHUB_RUN_ID", ""),
        probe_exit_code=args.probe_exit_code,
    )
    result = open_issue(signature_label(codes), body)
    print(json.dumps({"findings": codes, "issue": result}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
