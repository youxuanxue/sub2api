#!/usr/bin/env python3
"""Deterministic GitHub Issue lifecycle decisions for Prod Ops findings."""
from __future__ import annotations

import datetime as dt
from typing import Any

ISSUE_COOLDOWN = dt.timedelta(days=7)


def _timestamp(value: Any) -> dt.datetime | None:
    text = str(value or "").strip()
    if not text:
        return None
    try:
        parsed = dt.datetime.fromisoformat(text.replace("Z", "+00:00"))
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=dt.timezone.utc)
    return parsed.astimezone(dt.timezone.utc)


def decide_issue_action(
    issues: list[dict[str, Any]],
    *,
    now: dt.datetime | None = None,
    cooldown: dt.timedelta = ISSUE_COOLDOWN,
) -> dict[str, Any]:
    """Choose update, suppress, or create for one stable finding signature."""
    current = now or dt.datetime.now(dt.timezone.utc)
    if current.tzinfo is None:
        current = current.replace(tzinfo=dt.timezone.utc)
    current = current.astimezone(dt.timezone.utc)

    open_issues = [item for item in issues if str(item.get("state") or "").upper() == "OPEN"]
    if open_issues:
        existing = max(open_issues, key=lambda item: int(item.get("number") or 0))
        return {"action": "update", "number": int(existing.get("number") or 0)}

    cutoff = current - cooldown
    recent_closed: list[tuple[dt.datetime, dict[str, Any]]] = []
    unknown_closed: list[dict[str, Any]] = []
    for item in issues:
        if str(item.get("state") or "").upper() != "CLOSED":
            continue
        closed_at = _timestamp(item.get("closedAt"))
        if closed_at is None:
            unknown_closed.append(item)
        elif closed_at >= cutoff:
            recent_closed.append((closed_at, item))
    if recent_closed:
        closed_at, existing = max(recent_closed, key=lambda pair: pair[0])
        return {
            "action": "suppress",
            "number": int(existing.get("number") or 0),
            "closed_at": closed_at.isoformat().replace("+00:00", "Z"),
        }
    if unknown_closed:
        existing = max(unknown_closed, key=lambda item: int(item.get("number") or 0))
        return {
            "action": "suppress",
            "number": int(existing.get("number") or 0),
            "closed_at": "unknown",
        }

    return {"action": "create"}
