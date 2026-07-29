#!/usr/bin/env python3
"""Sync CodeBuddy / WorkBuddy models.json from a TokenKey universal Gateway Key.

Source of truth for model ids: GET /v1/models with the universal key (live access).
Chat-capable filter: exclude image/video-only ids; enrich from /api/v1/public/pricing.

Writes ~/.codebuddy/models.json (SSOT) and symlinks ~/.workbuddy/models.json by default.
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
MATRIX_SCRIPT = REPO_ROOT / "ops/test/gateway_model_ssot_matrix.py"

DEFAULT_BASE_URL = os.environ.get("TK_FULLTEST_BASE_URL", "https://api.tokenkey.dev")
DEFAULT_ENV_VAR = "TK_FULLTEST_KEY"
DEFAULT_CANONICAL = Path.home() / ".codebuddy" / "models.json"
DEFAULT_SYMLINK = Path.home() / ".workbuddy" / "models.json"
CHAT_COMPLETIONS_PATH = "/v1/chat/completions"

IMAGE_VIDEO_HINTS = (
    "-image",
    "imagen-",
    "seedream",
    "seedance",
    "veo-",
    "grok-imagine",
    "vision-preview",
)

VENDOR_LABEL = {
    "anthropic": "Anthropic",
    "openai": "OpenAI",
    "xai": "xAI",
    "x-ai": "xAI",
    "zhipu": "Zhipu",
    "dashscope": "Alibaba",
    "volcengine": "Volcengine",
    "deepseek": "DeepSeek",
    "moonshot": "Moonshot",
    "antigravity": "Google",
    "vertex_ai-language-models": "Google",
    "google": "Google",
}


def fetch_json(url: str, *, headers: dict[str, str] | None = None, timeout: float = 60) -> Any:
    req = urllib.request.Request(url, headers=headers or {})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.load(resp)


def is_media_only(model_id: str) -> bool:
    low = model_id.lower()
    return any(hint in low for hint in IMAGE_VIDEO_HINTS)


def vendor_label(row: dict[str, Any] | None, meta: dict[str, Any]) -> str:
    raw = (row or {}).get("vendor") or meta.get("vendor") or "TokenKey"
    return VENDOR_LABEL.get(raw, str(raw).replace("_", " ").title())


def capability_flags(caps: list[str] | None) -> dict[str, bool]:
    values = set(caps or [])
    return {
        "supportsToolCall": bool(values & {"tool_use", "tools"}),
        "supportsImages": "vision" in values,
        "supportsReasoning": "reasoning" in values,
    }


def load_chat_matrix_rows(base_url: str, timeout: float) -> dict[str, dict[str, Any]]:
    if not MATRIX_SCRIPT.is_file():
        raise FileNotFoundError(f"missing matrix script: {MATRIX_SCRIPT}")
    raw = subprocess.check_output(
        [
            sys.executable,
            str(MATRIX_SCRIPT),
            "list",
            "--source",
            "live-pricing",
            "--only-protocol",
            "chat",
            "--format",
            "json",
            "--base-url",
            base_url.rstrip("/"),
            "--timeout",
            str(timeout),
        ],
        text=True,
    )
    payload = json.loads(raw)
    return {row["model"]: row for row in payload.get("rows", [])}


def list_live_model_ids(base_url: str, api_key: str, timeout: float) -> set[str]:
    payload = fetch_json(
        f"{base_url.rstrip('/')}/v1/models",
        headers={"Authorization": f"Bearer {api_key}"},
        timeout=timeout,
    )
    return {item["id"] for item in payload.get("data", []) if item.get("id")}


def load_pricing_by_id(base_url: str, timeout: float) -> dict[str, dict[str, Any]]:
    payload = fetch_json(f"{base_url.rstrip('/')}/api/v1/public/pricing", timeout=timeout)
    rows = payload.get("data") or payload.get("models") or []
    return {row["model_id"]: row for row in rows if row.get("model_id")}


def build_model_entry(
    model_id: str,
    *,
    base_url: str,
    env_var: str,
    chat_rows: dict[str, dict[str, Any]],
    pricing_by_id: dict[str, dict[str, Any]],
) -> dict[str, Any]:
    row = chat_rows.get(model_id)
    meta = pricing_by_id.get(model_id, {})
    flags = capability_flags(meta.get("capabilities"))
    entry: dict[str, Any] = {
        "id": model_id,
        "name": model_id,
        "vendor": vendor_label(row, meta),
        "apiKey": f"${{{env_var}}}",
        "url": f"{base_url.rstrip('/')}{CHAT_COMPLETIONS_PATH}",
        **flags,
    }
    if not entry["supportsToolCall"]:
        entry["supportsToolCall"] = True
    context_window = meta.get("context_window")
    max_output = meta.get("max_output_tokens")
    if context_window:
        entry["maxInputTokens"] = int(context_window)
    if max_output:
        entry["maxOutputTokens"] = int(max_output)
    return entry


def build_payload(
    *,
    base_url: str,
    api_key: str,
    env_var: str,
    timeout: float,
) -> dict[str, Any]:
    live_ids = list_live_model_ids(base_url, api_key, timeout)
    chat_rows = load_chat_matrix_rows(base_url, timeout)
    pricing_by_id = load_pricing_by_id(base_url, timeout)
    selected = sorted(mid for mid in live_ids if not is_media_only(mid))
    models = [
        build_model_entry(
            model_id,
            base_url=base_url,
            env_var=env_var,
            chat_rows=chat_rows,
            pricing_by_id=pricing_by_id,
        )
        for model_id in selected
    ]
    return {"models": models, "availableModels": [m["id"] for m in models]}


def atomic_write_json(path: Path, payload: dict[str, Any], *, mode: int = 0o600) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=str(path.parent))
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, indent=2)
            handle.write("\n")
        os.chmod(tmp_name, mode)
        os.replace(tmp_name, path)
    finally:
        if os.path.exists(tmp_name):
            os.unlink(tmp_name)


def ensure_symlink(link_path: Path, target_path: Path) -> None:
    target_path = target_path.resolve()
    if link_path.is_symlink():
        if link_path.resolve() == target_path:
            return
        link_path.unlink()
    elif link_path.exists():
        link_path.unlink()
    link_path.parent.mkdir(parents=True, exist_ok=True)
    link_path.symlink_to(target_path)


def sync_models_json(
    *,
    base_url: str,
    api_key: str,
    env_var: str,
    canonical_path: Path,
    symlink_path: Path | None,
    timeout: float,
    dry_run: bool,
) -> dict[str, Any]:
    payload = build_payload(base_url=base_url, api_key=api_key, env_var=env_var, timeout=timeout)
    if dry_run:
        return payload
    atomic_write_json(canonical_path, payload)
    if symlink_path is not None:
        ensure_symlink(symlink_path, canonical_path)
    return payload


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Sync CodeBuddy/WorkBuddy models.json from TokenKey universal key /v1/models",
    )
    parser.add_argument("--key", default="", help="Gateway key (default: TK_FULLTEST_KEY env)")
    parser.add_argument("--base-url", default=DEFAULT_BASE_URL, help="TokenKey base URL")
    parser.add_argument("--env-var", default=DEFAULT_ENV_VAR, help="apiKey env var name in models.json")
    parser.add_argument(
        "--output",
        type=Path,
        default=DEFAULT_CANONICAL,
        help="Canonical models.json path (default: ~/.codebuddy/models.json)",
    )
    parser.add_argument(
        "--symlink",
        type=Path,
        default=DEFAULT_SYMLINK,
        help="WorkBuddy symlink path (default: ~/.workbuddy/models.json); pass --no-symlink to skip",
    )
    parser.add_argument("--no-symlink", action="store_true", help="Do not create/update WorkBuddy symlink")
    parser.add_argument("--timeout", type=float, default=60, help="HTTP timeout seconds")
    parser.add_argument("--dry-run", action="store_true", help="Print JSON to stdout; do not write files")
    parser.add_argument(
        "--check",
        action="store_true",
        help="Compare generated payload to --output; exit 1 on drift",
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    api_key = args.key or os.environ.get(DEFAULT_ENV_VAR, "")
    if not api_key:
        print(f"ERROR: --key or {DEFAULT_ENV_VAR} is required", file=sys.stderr)
        return 2

    symlink_path = None if args.no_symlink else args.symlink
    try:
        payload = sync_models_json(
            base_url=args.base_url,
            api_key=api_key,
            env_var=args.env_var,
            canonical_path=args.output,
            symlink_path=symlink_path,
            timeout=args.timeout,
            dry_run=args.dry_run or args.check,
        )
    except urllib.error.HTTPError as exc:
        print(f"ERROR: HTTP {exc.code} fetching TokenKey catalog: {exc.reason}", file=sys.stderr)
        return 1
    except subprocess.CalledProcessError as exc:
        print(f"ERROR: gateway_model_ssot_matrix failed: {exc}", file=sys.stderr)
        return 1

    if args.dry_run:
        json.dump(payload, sys.stdout, indent=2)
        sys.stdout.write("\n")
        return 0

    if args.check:
        if not args.output.is_file():
            print(f"ERROR: missing {args.output}", file=sys.stderr)
            return 1
        existing = json.loads(args.output.read_text(encoding="utf-8"))
        if existing != payload:
            print(f"ERROR: {args.output} drift from live TK_FULLTEST_KEY catalog", file=sys.stderr)
            return 1
        print(f"ok: {args.output} matches live catalog ({len(payload['models'])} models)")
        return 0

    count = len(payload["models"])
    print(f"wrote {args.output} ({count} models)")
    if symlink_path is not None:
        print(f"symlink {symlink_path} -> {args.output.resolve()}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
