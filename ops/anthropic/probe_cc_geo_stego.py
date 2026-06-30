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
import re
import sys
from pathlib import Path

DATE_LINE = re.compile(
    r"(Today.?s date is(?: now)?|The current date is|Current date:?)\s*(.+)",
    re.IGNORECASE,
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
    body = rec.get("body") or {}
    system = body.get("system")
    system_texts = extract_system_texts(system)
    date_lines = _date_line_records(system_texts, "system")
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


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("jsonl", type=Path)
    ap.add_argument("--json", action="store_true")
    ap.add_argument(
        "--check",
        action="store_true",
        help="exit 1 if any captured scenario still needs TokenKey normalize",
    )
    args = ap.parse_args()

    rows = []
    for line in args.jsonl.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line:
            continue
        rows.append(analyze_record(json.loads(line)))

    if args.json:
        print(json.dumps(rows, ensure_ascii=False, indent=2))
    else:
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
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
