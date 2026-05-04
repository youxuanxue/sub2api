#!/usr/bin/env python3
"""
check-traj-dataset.py — validate trajectory export dataset quality gates.

Accepts either a trajectory export zip (containing trajectory.jsonl) or a raw
JSONL file. It enforces trajectory export quality gates aligned with
`docs/global/tokenkey-opc-transformation-plan.md` (Evidence / traj：§5、§8.1) —
Phase-C5 style checks retained as mechanical validators:
- H1: effective turns >= 2
- H2: structured tool calls >= 1
- H3: tool pairing ratio > 0.3
- D1: exact+subset duplicate turn ratio < 20%
- JSONL / JSON parse validity
- session assembly completeness
- tool naming convention
- single-call grouping consistency

Exit codes:
  0  — dataset passed all checks.
  1  — dataset parsed, but at least one gate failed.
  2  — usage, IO, or archive-format error.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
import zipfile
from collections import Counter, defaultdict
from pathlib import Path
from typing import Any

TOOL_NAME_RE = re.compile(r"^[A-Za-z0-9_.:-]+$")
VALID_MESSAGE_KINDS = {"request", "response", "tool_schema", "tool_call", "tool_result"}
VALID_ROLES = {"user", "assistant", "tool"}


def fatal(message: str) -> int:
    print(f"FATAL: {message}", file=sys.stderr)
    return 2


def canonical_json(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def read_dataset(path: Path) -> tuple[str, str, list[str]]:
    if not path.is_file():
        raise FileNotFoundError(path)
    if path.suffix.lower() == ".zip":
        with zipfile.ZipFile(path) as archive:
            names = archive.namelist()
            member = "trajectory.jsonl" if "trajectory.jsonl" in names else ""
            if not member:
                jsonl_members = [name for name in names if name.endswith(".jsonl")]
                if len(jsonl_members) == 1:
                    member = jsonl_members[0]
            if not member:
                raise ValueError("zip must contain trajectory.jsonl")
            with archive.open(member) as handle:
                raw = handle.read().decode("utf-8")
            return "zip", member, raw.splitlines()
    raw = path.read_text(encoding="utf-8")
    return "jsonl", path.name, raw.splitlines()


def normalize_int(value: Any) -> int | None:
    if isinstance(value, bool):
        return None
    if isinstance(value, int):
        return value
    if isinstance(value, float) and value.is_integer():
        return int(value)
    return None


def validate_rows(lines: list[str]) -> tuple[list[dict[str, Any]], list[str]]:
    rows: list[dict[str, Any]] = []
    failures: list[str] = []
    for index, raw_line in enumerate(lines, start=1):
        line = raw_line.strip()
        if not line:
            continue
        try:
            row = json.loads(line)
        except json.JSONDecodeError as exc:
            failures.append(f"jsonl parse check: line {index} is not valid JSON ({exc})")
            continue
        if not isinstance(row, dict):
            failures.append(f"jsonl parse check: line {index} must be a JSON object")
            continue

        session_id = str(row.get("session_id", "")).strip()
        request_id = str(row.get("request_id", "")).strip()
        trajectory_id = str(row.get("trajectory_id", "")).strip()
        role = str(row.get("role", "")).strip()
        message_kind = str(row.get("message_kind", "")).strip()
        turn_index = normalize_int(row.get("turn_index"))

        if not session_id:
            failures.append(f"session assembly check: line {index} missing non-empty session_id")
        if not request_id:
            failures.append(f"session assembly check: line {index} missing non-empty request_id")
        if not trajectory_id:
            failures.append(f"session assembly check: line {index} missing non-empty trajectory_id")
        if turn_index is None or turn_index < 1:
            failures.append(f"session assembly check: line {index} has invalid turn_index={row.get('turn_index')!r}")
        if role not in VALID_ROLES:
            failures.append(f"session assembly check: line {index} has invalid role={role!r}")
        if message_kind not in VALID_MESSAGE_KINDS:
            failures.append(f"session assembly check: line {index} has invalid message_kind={message_kind!r}")

        if message_kind in {"tool_schema", "tool_call", "tool_result"}:
            tool_name = str(row.get("tool_name", "")).strip()
            if not tool_name:
                failures.append(f"tool pairing check: line {index} missing tool_name for {message_kind}")
            elif not TOOL_NAME_RE.match(tool_name):
                failures.append(f"tool naming check: line {index} has invalid tool_name={tool_name!r}")

        rows.append(row)
    return rows, failures


def turn_signature(row: dict[str, Any]) -> str:
    payload = {
        "role": row.get("role"),
        "message_kind": row.get("message_kind"),
        "tool_name": row.get("tool_name"),
        "tool_call_id": row.get("tool_call_id"),
        "tool_schema_json": row.get("tool_schema_json"),
        "tool_call_json": row.get("tool_call_json"),
        "tool_result_json": row.get("tool_result_json"),
        "content_json": row.get("content_json"),
        "model": row.get("model"),
    }
    return canonical_json(payload)


def tool_key(row: dict[str, Any]) -> tuple[str, str]:
    session_id = str(row.get("session_id", "")).strip()
    tool_call_id = str(row.get("tool_call_id", "")).strip()
    tool_name = str(row.get("tool_name", "")).strip()
    if tool_call_id:
        return session_id, f"id:{tool_call_id}"
    return session_id, f"name:{tool_name}"


def analyze_rows(rows: list[dict[str, Any]]) -> tuple[dict[str, Any], list[str]]:
    failures: list[str] = []
    turns: dict[tuple[str, int], list[dict[str, Any]]] = defaultdict(list)
    session_turns: dict[str, set[int]] = defaultdict(set)
    tool_calls: Counter[tuple[str, str]] = Counter()
    tool_results: Counter[tuple[str, str]] = Counter()

    for row in rows:
        session_id = str(row.get("session_id", "")).strip()
        turn_index = normalize_int(row.get("turn_index"))
        if not session_id or turn_index is None or turn_index < 1:
            continue
        turns[(session_id, turn_index)].append(row)
        session_turns[session_id].add(turn_index)
        if row.get("message_kind") == "tool_call":
            tool_calls[tool_key(row)] += 1
        elif row.get("message_kind") == "tool_result":
            tool_results[tool_key(row)] += 1

    effective_turns = 0
    exact_duplicates = 0
    subset_duplicates = 0
    seen_turn_signatures: list[set[str]] = []

    for session_id, indices in session_turns.items():
        ordered = sorted(indices)
        expected = list(range(1, len(ordered) + 1))
        if ordered != expected:
            failures.append(
                f"session assembly check: session {session_id!r} has non-contiguous turn indexes {ordered}, expected {expected}"
            )

    for (session_id, turn_index), turn_rows in sorted(turns.items()):
        request_rows = [row for row in turn_rows if row.get("message_kind") == "request"]
        response_rows = [row for row in turn_rows if row.get("message_kind") == "response"]
        if len(request_rows) != 1 or len(response_rows) != 1:
            failures.append(
                f"session assembly check: session {session_id!r} turn {turn_index} must contain exactly one request and one response row"
            )
        else:
            effective_turns += 1

        request_ids = {str(row.get("request_id", "")).strip() for row in turn_rows}
        if "" in request_ids or len(request_ids) != 1:
            failures.append(
                f"session assembly check: session {session_id!r} turn {turn_index} must contain a single stable request_id"
            )

        trajectory_ids = {str(row.get("trajectory_id", "")).strip() for row in turn_rows}
        if "" in trajectory_ids or len(trajectory_ids) != 1:
            failures.append(
                f"session assembly check: session {session_id!r} turn {turn_index} must contain a single stable trajectory_id"
            )

        call_rows = [row for row in turn_rows if row.get("message_kind") == "tool_call"]
        tool_rows = [row for row in turn_rows if row.get("message_kind") in {"tool_schema", "tool_call", "tool_result"}]
        if len(call_rows) == 1 and tool_rows:
            tool_names = {str(row.get("tool_name", "")).strip() for row in tool_rows if str(row.get("tool_name", "")).strip()}
            tool_call_ids = {
                str(row.get("tool_call_id", "")).strip()
                for row in tool_rows
                if str(row.get("tool_call_id", "")).strip()
            }
            if len(tool_names) > 1:
                failures.append(
                    f"single-call grouping check: session {session_id!r} turn {turn_index} mixes multiple tool_name values {sorted(tool_names)}"
                )
            if len(tool_call_ids) > 1:
                failures.append(
                    f"single-call grouping check: session {session_id!r} turn {turn_index} mixes multiple tool_call_id values {sorted(tool_call_ids)}"
                )

        current_signature = {turn_signature(row) for row in turn_rows}
        if current_signature:
            if any(current_signature == previous for previous in seen_turn_signatures):
                exact_duplicates += 1
            elif any(current_signature < previous or previous < current_signature for previous in seen_turn_signatures):
                subset_duplicates += 1
            seen_turn_signatures.append(current_signature)

    total_tool_calls = sum(tool_calls.values())
    paired_tool_calls = sum(min(count, tool_results.get(key, 0)) for key, count in tool_calls.items())
    pairing_ratio = (paired_tool_calls / total_tool_calls) if total_tool_calls else 0.0
    total_turns = len(turns)
    dedupe_ratio = ((exact_duplicates + subset_duplicates) / total_turns) if total_turns else 0.0

    if effective_turns < 2:
        failures.append(f"H1 failed: effective turns={effective_turns}, require >= 2")
    if total_tool_calls < 1:
        failures.append(f"H2 failed: structured tool calls={total_tool_calls}, require >= 1")
    if pairing_ratio <= 0.3:
        failures.append(f"H3 failed: tool pairing ratio={pairing_ratio:.3f}, require > 0.300")
    if dedupe_ratio >= 0.2:
        failures.append(f"D1 failed: exact+subset duplicate turn ratio={dedupe_ratio:.3f}, require < 0.200")

    metrics = {
        "row_count": len(rows),
        "session_count": len(session_turns),
        "turn_count": total_turns,
        "effective_turns": effective_turns,
        "structured_tool_calls": total_tool_calls,
        "tool_results": sum(tool_results.values()),
        "paired_tool_calls": paired_tool_calls,
        "tool_pairing_ratio": pairing_ratio,
        "exact_duplicate_turns": exact_duplicates,
        "subset_duplicate_turns": subset_duplicates,
        "duplicate_turn_ratio": dedupe_ratio,
    }
    return metrics, failures


def render_human(report: dict[str, Any], quiet: bool) -> None:
    if quiet and not report["failures"]:
        return
    if not quiet:
        print(f"traj dataset check: {report['input']}")
        print(f"  artifact: {report['artifact_kind']}:{report['artifact_member']}")
        print("  metrics:")
        for key, value in report["metrics"].items():
            if isinstance(value, float):
                print(f"    - {key}: {value:.3f}")
            else:
                print(f"    - {key}: {value}")
    if report["failures"]:
        print("  FAIL: trajectory dataset gate failed")
        for failure in report["failures"]:
            print(f"        - {failure}")
    elif not quiet:
        print("  ok: trajectory dataset passed H1/H2/H3/D1 and structural checks")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("path", help="path to trajectory export zip or raw JSONL")
    parser.add_argument("--json", action="store_true", help="emit machine-readable JSON report")
    parser.add_argument("--quiet", action="store_true", help="only print failures")
    args = parser.parse_args()

    dataset_path = Path(args.path)
    try:
        artifact_kind, artifact_member, lines = read_dataset(dataset_path)
    except FileNotFoundError:
        return fatal(f"dataset file not found: {dataset_path}")
    except zipfile.BadZipFile as exc:
        return fatal(f"invalid zip archive: {exc}")
    except ValueError as exc:
        return fatal(str(exc))
    except OSError as exc:
        return fatal(str(exc))

    rows, failures = validate_rows(lines)
    metrics, analysis_failures = analyze_rows(rows)
    failures.extend(analysis_failures)

    report = {
        "input": str(dataset_path),
        "artifact_kind": artifact_kind,
        "artifact_member": artifact_member,
        "metrics": metrics,
        "failures": failures,
    }

    if args.json:
        json.dump(report, sys.stdout, indent=2)
        sys.stdout.write("\n")
    else:
        render_human(report, args.quiet)

    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
