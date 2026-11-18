#!/usr/bin/env python3
"""Load JSON documents from run-probe / TAT stdout.

Remote probes emit one JSON document, but fan-out across multiple apps or
wrappers (status frames, NDJSON echoes, transport banners) can leave multiple
documents or trailing text. Callers that ``json.loads`` the whole stdout then
see ``Extra data``.
"""
from __future__ import annotations

import json
from typing import Any


def iter_probe_json(raw: str) -> list[Any]:
    """Return every complete JSON value found in ``raw`` (NDJSON or concatenated)."""
    text = raw.strip()
    if not text:
        return []
    decoder = json.JSONDecoder()
    values: list[Any] = []
    idx = 0
    while idx < len(text):
        while idx < len(text) and text[idx].isspace():
            idx += 1
        if idx >= len(text):
            break
        try:
            obj, nxt = decoder.raw_decode(text, idx)
        except json.JSONDecodeError:
            nl = text.find("\n", idx)
            if nl < 0:
                break
            idx = nl + 1
            continue
        values.append(obj)
        idx = nxt
    return values


def loads_probe_json(raw: str) -> Any:
    """Parse probe stdout into one JSON value.

    - Single document (with optional trailing noise) → that document.
    - Multiple identical documents (fan-out duplicates) → the shared value.
    - Multiple conflicting documents → last successfully decoded value is NOT
      silently preferred; raise so callers fail closed or merge explicitly.
    """
    text = raw.strip()
    if not text:
        raise json.JSONDecodeError("empty probe output", raw, 0)
    values = iter_probe_json(text)
    if not values:
        raise json.JSONDecodeError("no JSON value in probe output", text, 0)
    if len(values) == 1:
        return values[0]
    # Fan-out often duplicates the same DB snapshot across apps.
    serialized = [json.dumps(v, sort_keys=True, ensure_ascii=False) for v in values]
    if len(set(serialized)) == 1:
        return values[0]
    raise json.JSONDecodeError(
        f"conflicting probe JSON documents ({len(values)}); pin --node or merge",
        text,
        0,
    )
