#!/usr/bin/env python3
"""Finite, deployment-owned capability matrix for prod replay.

This module is deliberately independent of the request path.  The manifest is
the denominator for replay; live gateway traffic is never captured for it.
"""
from __future__ import annotations

from dataclasses import dataclass
import json
from pathlib import Path


@dataclass(frozen=True)
class Capability:
    model: str
    protocol: str
    request_type: str
    key_type: str


def load(path: Path) -> tuple[Capability, ...]:
    raw = json.loads(path.read_text(encoding="utf-8"))
    entries = raw.get("capabilities")
    if not isinstance(entries, list) or not entries:
        raise ValueError("capability manifest is empty")
    out: list[Capability] = []
    seen: set[tuple[str, str, str, str]] = set()
    for item in entries:
        if not isinstance(item, dict):
            raise ValueError("invalid capability entry")
        value = tuple(item.get(k, "") for k in ("model", "protocol", "request_type", "key_type"))
        if not all(isinstance(v, str) and v for v in value):
            raise ValueError("capability fields are required")
        if value in seen:
            raise ValueError("duplicate capability entry")
        seen.add(value)
        out.append(Capability(*value))
    return tuple(out)
