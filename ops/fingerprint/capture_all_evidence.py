#!/usr/bin/env python3
"""Validate capture provenance before the umbrella accepts a lane's exit code.

Platform engines still own capture mechanics and semantic comparison. This gate
only requires current evidence, an observed version, and the existing pin owner.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from datetime import datetime
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT / "scripts/fingerprint"))
from client_release_watch import PIN_READERS, normalize_version  # noqa: E402


class MissingEvidence(ValueError):
    pass


def observed_version(identity: str, log: str, started_ns: int) -> tuple[str, str]:
    if identity == "codex":
        match = re.search(r"^Codex fingerprint diff \(installed=([^\)]+)\):$", log, re.M)
        if not match or match[1] == "-":
            raise MissingEvidence("installed Codex version NOT_OBSERVED")
        return match[1], "current engine log (installed binary check)"

    paths = re.findall(r"^bundle=(.+)$", log, re.M)
    if not paths:
        raise MissingEvidence("capture bundle NOT_OBSERVED")
    path = Path(paths[-1])
    if not path.is_file():
        raise MissingEvidence(f"capture bundle missing: {path}")
    if path.stat().st_mtime_ns < started_ns:
        raise MissingEvidence(f"capture bundle predates this lane: {path}")
    bundle = json.loads(path.read_text(encoding="utf-8"))
    # Schema semantics belong to each engine (Kiro currently emits v3).
    if not isinstance(bundle, dict) or type(bundle.get("schema_version")) is not int or bundle["schema_version"] < 1:
        raise ValueError(f"invalid capture bundle: {path}")
    stamp = str(bundle.get("captured_at") or "")
    if not stamp:
        raise MissingEvidence(f"capture time NOT_OBSERVED: {path}")
    captured = datetime.fromisoformat(stamp.replace("Z", "+00:00"))
    if captured.tzinfo is None:
        raise ValueError(f"capture time has no timezone: {path}")
    if captured.timestamp() < started_ns // 1_000_000_000:
        raise MissingEvidence(f"capture time predates this lane: {path}")

    if identity == "antigravity":
        sys.path.insert(0, str(REPO_ROOT / "ops/antigravity"))
        from capture_antigravity_fingerprint import parse_ua
        version, _ = parse_ua(str(bundle.get("http", {}).get("user_agent") or ""))
    else:
        key = {
            "claude-code": "cc_version",
            "kiro-cli": "kiro_cli_version",
            "codex-tls": "codex_cli_version",
            "gemini-cli": "installed_version",
            "grok-cli": "installed_version",
        }[identity]
        version = str(bundle.get(key) or "")
    if not re.fullmatch(r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?", version):
        raise MissingEvidence(f"actual client version NOT_OBSERVED: {path}")
    return version, str(path)


def validate(identity: str, log: str, started_ns: int, engine_code: int) -> int:
    if engine_code not in (0, 1):
        return 3 if engine_code == 3 else 2
    try:
        version, source = observed_version(identity, log, started_ns)
        owner = "codex" if identity == "codex-tls" else identity
        pinned = PIN_READERS[owner]()
        if not pinned:
            raise ValueError(f"pin owner returned no version: {owner}")
    except MissingEvidence as exc:
        print(f"{identity}: incomplete — {exc}")
        return 3
    except (OSError, ValueError, KeyError, TypeError, AttributeError) as exc:
        print(f"{identity}: error — invalid evidence: {exc}")
        return 2
    print(f"{identity}: observed_version={version} pin={pinned} evidence={source}")
    return 1 if normalize_version(version) != pinned else engine_code


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--identity", required=True)
    parser.add_argument("--log", type=Path, required=True)
    parser.add_argument("--started-ns", type=int, required=True)
    parser.add_argument("--engine-code", type=int, required=True)
    args = parser.parse_args()
    log = args.log.read_text(encoding="utf-8")
    return validate(args.identity, log, args.started_ns, args.engine_code)


if __name__ == "__main__":
    raise SystemExit(main())
