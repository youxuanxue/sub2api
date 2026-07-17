#!/usr/bin/env python3
"""Analyze CC /v1/messages wire body for geo-steganography signals.

Reads mitm JSONL from probe_cc_geo_stego_mitm.py. Surfaces live in:
  - system[] text blocks (Agent SDK / billing banner — usually NOT #currentDate)
  - messages[].content[].text <system-reminder> # currentDate (primary CC ≥2.1.91)
  - messages[].content[].attachment.type=date_change newDate

TokenKey egress canonical shape (gateway_request_tk_cc_geo_stego.go):
  ASCII apostrophe U+0027 + ISO date YYYY-MM-DD.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
BACKEND_DIR = REPO_ROOT / "backend"
GEO_STEGO_GO = BACKEND_DIR / "internal/service/gateway_request_tk_cc_geo_stego.go"
GEO_STEGO_TEST = BACKEND_DIR / "internal/service/gateway_request_tk_cc_geo_stego_test.go"

DATE_LINE = re.compile(
    r"(Today.?s date is(?: now)?|The current date is|Current date:?)\s*(.+)",
    re.IGNORECASE,
)
CANONICAL_DATE_LINE_RE = re.compile(
    r"Today[''\u2019\u02bc\u02b9]s date is(?: now)? (\d{4})[/-](\d{2})[/-](\d{2})\."
)
APOSTROPHE_NAMES = {
    "\u0027": "ASCII_APOSTROPHE_U+0027",
    "\u2019": "RIGHT_SINGLE_QUOTATION_U+2019",
    "\u02bc": "MODIFIER_LETTER_APOSTROPHE_U+02BC",
    "\u02b9": "MODIFIER_LETTER_PRIME_U+02B9",
}
CANONICAL_APOSTROPHE = "ASCII_APOSTROPHE_U+0027"
CANONICAL_DATE_FORMAT = "ISO_DASH"


def classify_apostrophe_in_date_line(line: str) -> str | None:
    m = DATE_LINE.search(line)
    if not m:
        return None
    prefix = m.group(0)[: m.start(2) - m.start(0)]
    for ch, name in APOSTROPHE_NAMES.items():
        if ch in prefix:
            return name
    return "NO_SPECIAL_APOSTROPHE"


def classify_date_value(raw: str) -> dict:
    raw = raw.strip().rstrip(".")
    out = {"raw": raw[:80]}
    if re.fullmatch(r"\d{4}-\d{2}-\d{2}", raw):
        out["format"] = "ISO_DASH"
    elif re.fullmatch(r"\d{4}/\d{2}/\d{2}", raw):
        out["format"] = "SLASH"
    else:
        out["format"] = "OTHER"
    return out


def extract_system_texts(system) -> list[str]:
    texts: list[str] = []
    if isinstance(system, str):
        if system.strip():
            texts.append(system.strip())
        return texts
    if isinstance(system, list):
        for entry in system:
            if isinstance(entry, dict):
                t = str(entry.get("text") or "").strip()
                if t:
                    texts.append(t)
            elif isinstance(entry, str) and entry.strip():
                texts.append(entry.strip())
    return texts


def _date_line_records(texts: list[str], surface: str) -> list[dict]:
    rows: list[dict] = []
    for i, text in enumerate(texts):
        for line in text.splitlines():
            m = DATE_LINE.search(line)
            if not m:
                continue
            apostrophe = classify_apostrophe_in_date_line(line)
            date = classify_date_value(m.group(2))
            rows.append(
                {
                    "surface": surface,
                    "block_index": i,
                    "line": line.strip()[:200],
                    "apostrophe": apostrophe,
                    "date": date,
                    "needs_normalize": (
                        apostrophe not in (None, CANONICAL_APOSTROPHE, "NO_SPECIAL_APOSTROPHE")
                        or date.get("format") != CANONICAL_DATE_FORMAT
                    ),
                }
            )
    return rows


def body_wire_from_record(rec: dict) -> dict | None:
    wire = rec.get("body_wire")
    if isinstance(wire, dict) and (wire.get("system") is not None or wire.get("messages") is not None):
        return {"system": wire.get("system"), "messages": wire.get("messages")}
    return None


def _message_date_lines_from_wire(messages) -> list[dict]:
    rows: list[dict] = []
    if not isinstance(messages, list):
        return rows
    for mi, msg in enumerate(messages):
        if not isinstance(msg, dict):
            continue
        role = str(msg.get("role") or "")
        content = msg.get("content")
        if isinstance(content, str):
            texts = [content]
            block_indices = [0]
        elif isinstance(content, list):
            texts = []
            block_indices = []
            for ci, block in enumerate(content):
                if isinstance(block, dict) and block.get("type") == "text":
                    texts.append(str(block.get("text") or ""))
                    block_indices.append(ci)
                elif isinstance(block, dict):
                    att = block.get("attachment")
                    if isinstance(att, dict) and att.get("type") == "date_change":
                        raw = str(att.get("newDate") or "")
                        date = classify_date_value(raw)
                        rows.append(
                            {
                                "surface": f"messages[{mi}].content[{ci}].attachment.newDate",
                                "line": raw[:80],
                                "apostrophe": None,
                                "date": date,
                                "needs_normalize": date.get("format") != CANONICAL_DATE_FORMAT,
                            }
                        )
        else:
            continue
        for ti, text in enumerate(texts):
            ci = block_indices[ti]
            for line in text.splitlines():
                m = DATE_LINE.search(line)
                if not m:
                    continue
                apostrophe = classify_apostrophe_in_date_line(line)
                date = classify_date_value(m.group(2))
                rows.append(
                    {
                        "surface": f"messages[{mi}].content[{ci}].text",
                        "role": role,
                        "has_system_reminder": "<system-reminder>" in text.lower(),
                        "line": line.strip()[:200],
                        "apostrophe": apostrophe,
                        "date": date,
                        "needs_normalize": (
                            apostrophe not in (None, CANONICAL_APOSTROPHE, "NO_SPECIAL_APOSTROPHE")
                            or date.get("format") != CANONICAL_DATE_FORMAT
                        ),
                    }
                )
    return rows


def _message_date_lines(messages) -> list[dict]:
    rows: list[dict] = []
    if not isinstance(messages, list):
        return rows
    for msg in messages:
        if not isinstance(msg, dict):
            continue
        role = str(msg.get("role") or "")
        mi = msg.get("index")
        for block in msg.get("text_blocks") or []:
            for line in block.get("date_lines") or []:
                m = DATE_LINE.search(line)
                if not m:
                    continue
                apostrophe = classify_apostrophe_in_date_line(line)
                date = classify_date_value(m.group(2))
                rows.append(
                    {
                        "surface": f"messages[{mi}].content[{block.get('index')}].text",
                        "role": role,
                        "has_system_reminder": block.get("has_system_reminder"),
                        "line": line.strip()[:200],
                        "apostrophe": apostrophe,
                        "date": date,
                        "needs_normalize": (
                            apostrophe not in (None, CANONICAL_APOSTROPHE, "NO_SPECIAL_APOSTROPHE")
                            or date.get("format") != CANONICAL_DATE_FORMAT
                        ),
                    }
                )
        for att in msg.get("date_change_attachments") or []:
            raw = str(att.get("newDate") or "")
            date = classify_date_value(raw)
            rows.append(
                {
                    "surface": f"messages[{mi}].content[{att.get('content_index')}].attachment.newDate",
                    "line": raw[:80],
                    "apostrophe": None,
                    "date": date,
                    "needs_normalize": date.get("format") != CANONICAL_DATE_FORMAT,
                }
            )
    return rows


def analyze_record(rec: dict) -> dict:
    wire = body_wire_from_record(rec)
    body = rec.get("body") or {}
    if wire is not None:
        system = wire.get("system")
    else:
        system = body.get("system")
    system_texts = extract_system_texts(system)
    date_lines = _date_line_records(system_texts, "system")
    if wire is not None and wire.get("messages") is not None:
        date_lines.extend(_message_date_lines_from_wire(wire.get("messages")))
    else:
        date_lines.extend(_message_date_lines(body.get("messages")))
    needs_normalize = any(dl.get("needs_normalize") for dl in date_lines)
    return {
        "scenario": rec.get("scenario"),
        "tz": rec.get("tz"),
        "proxy": rec.get("proxy"),
        "base_url": rec.get("base_url"),
        "host": rec.get("host"),
        "system_block_count": len(system_texts),
        "system_heads": [t[:120].replace("\n", "\\n") for t in system_texts[:6]],
        "message_summaries": body.get("messages") or [],
        "date_lines": date_lines,
        "needs_normalize": needs_normalize,
        "billing_blocks": [
            t[:160]
            for t in system_texts
            if t.startswith("x-anthropic-billing-header")
        ],
    }


def load_records(jsonl: Path) -> list[dict]:
    records: list[dict] = []
    for line in jsonl.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line:
            continue
        records.append(json.loads(line))
    return records


def run_gateway_coverage(jsonl: Path) -> int:
    env = os.environ.copy()
    env["TOKENKEY_PROMPT_SURFACE_PROBE_JSONL"] = str(jsonl.resolve())
    env["TOKENKEY_CC_GEO_PROBE_JSONL"] = str(jsonl.resolve())
    proc = subprocess.run(
        ["go", "test", "-tags=unit", "./internal/service", "-run", "^TestTkProbePromptSurfaceGatewayCoverageJSONL$", "-count=1"],
        cwd=str(BACKEND_DIR),
        env=env,
        capture_output=True,
        text=True,
    )
    if proc.stdout:
        print(proc.stdout, end="")
    if proc.stderr:
        print(proc.stderr, end="", file=sys.stderr)
    return proc.returncode


def _unknown_apostrophe_in_line(line: str) -> str | None:
    m = re.search(r"Today(.)s date is", line, re.IGNORECASE)
    if not m:
        return None
    ch = m.group(1)
    if ch in APOSTROPHE_NAMES or ch == "'":
        return None
    return ch


def _extend_go_apostrophe_class(ch: str) -> bool:
    text = GEO_STEGO_GO.read_text(encoding="utf-8")
    esc = f"\\u{ord(ch):04x}"
    if esc in text:
        return False
    marker = "Today[''\\u2019\\u02bc\\u02b9]s date is"
    if marker not in text:
        return False
    new_marker = marker.replace("\\u02b9]s", f"{esc}\\u02b9]s", 1)
    updated = text.replace(marker, new_marker)
    if updated == text:
        return False
    GEO_STEGO_GO.write_text(updated, encoding="utf-8")
    print(f"patched {GEO_STEGO_GO.name}: added apostrophe {esc} (U+{ord(ch):04X})", file=sys.stderr)
    return True


def _canonicalize_probe_date_line(line: str) -> str:
    def repl_now(m: re.Match[str]) -> str:
        return f"Today's date is now {m.group(1)}-{m.group(2)}-{m.group(3)}."

    def repl(m: re.Match[str]) -> str:
        return f"Today's date is {m.group(1)}-{m.group(2)}-{m.group(3)}."

    out = re.sub(
        r"Today[''\u2019\u02bc\u02b9]s date is now (\d{4})[/-](\d{2})[/-](\d{2})\.",
        repl_now,
        line,
    )
    return CANONICAL_DATE_LINE_RE.sub(repl, out)


def _append_probe_test_case(line: str) -> bool:
    content = GEO_STEGO_TEST.read_text(encoding="utf-8")
    marker = "func TestTkNormalizeCCGeoStegoText(t *testing.T) {"
    if marker not in content:
        return False
    want = _canonicalize_probe_date_line(line.strip())
    if want == line.strip():
        return False
    safe_line = line.replace("\\", "\\\\").replace('"', '\\"')
    safe_want = want.replace("\\", "\\\\").replace('"', '\\"')
    if f'in:      "{safe_line}"' in content:
        return False
    case_name = "auto_probe_capture"
    needle = f'name:    "{case_name}",'
    if needle in content:
        return False
    insert = (
        f'\t\t{{\n'
        f'\t\t\tname:    "{case_name}",\n'
        f'\t\t\tin:      "{safe_line}",\n'
        f'\t\t\twant:    "{safe_want}",\n'
        f'\t\t\tchanged: true,\n'
        f'\t\t}},\n'
    )
    updated = content.replace("\t\t{\n\t\t\tname:    \"shanghai slash date ascii apostrophe\",", insert + "\t\t{\n\t\t\tname:    \"shanghai slash date ascii apostrophe\",", 1)
    if updated == content:
        return False
    GEO_STEGO_TEST.write_text(updated, encoding="utf-8")
    print(f"appended probe test case to {GEO_STEGO_TEST.name}", file=sys.stderr)
    return True


def auto_fix_gateway_gaps(jsonl: Path) -> bool:
    """Try mechanical fixes; return True if anything changed."""
    changed = False
    if run_gateway_coverage(jsonl) == 0:
        return False
    for rec in load_records(jsonl):
        row = analyze_record(rec)
        if not row.get("needs_normalize"):
            continue
        for dl in row.get("date_lines") or []:
            line = str(dl.get("line") or "")
            ch = _unknown_apostrophe_in_line(line)
            if ch and _extend_go_apostrophe_class(ch):
                changed = True
            if line and _append_probe_test_case(line):
                changed = True
    return changed


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("jsonl", type=Path)
    ap.add_argument("--json", action="store_true")
    ap.add_argument(
        "--check",
        action="store_true",
        help="exit 1 if any captured client wire still shows non-canonical geo signals",
    )
    ap.add_argument(
        "--check-gateway",
        action="store_true",
        help="exit 1 if tkNormalizeAnthropicCCGeoStego does not fully canonicalize captured bodies",
    )
    ap.add_argument(
        "--fix",
        action="store_true",
        help="when --check-gateway fails, attempt mechanical regex/test patches and re-check",
    )
    args = ap.parse_args()

    rows = [analyze_record(rec) for rec in load_records(args.jsonl)]

    if args.json:
        print(json.dumps(rows, ensure_ascii=False, indent=2))
    elif not args.check_gateway:
        for row in rows:
            print(
                f"=== scenario={row['scenario']} tz={row['tz']} "
                f"host={row.get('host')} needs_normalize={row['needs_normalize']} ==="
            )
            print(f"system_blocks={row['system_block_count']}")
            for dl in row["date_lines"]:
                surf = dl.get("surface", "?")
                print(
                    f"  [{surf}] apostrophe={dl.get('apostrophe')} "
                    f"format={dl['date']['format']} raw={dl['date']['raw']}"
                )
                print(f"    {dl.get('line')}")
            if not row["date_lines"]:
                print("  date_line: <none matched>")
            print()

    if args.check and any(r.get("needs_normalize") for r in rows):
        return 1

    if args.check_gateway or args.fix:
        code = run_gateway_coverage(args.jsonl)
        if code == 0:
            return 0
        if not args.fix:
            return code
        if auto_fix_gateway_gaps(args.jsonl):
            return run_gateway_coverage(args.jsonl)
        return code

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
