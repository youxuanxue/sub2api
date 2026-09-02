"""Pure schema and projection helpers for the servable reprobe ledger."""

from __future__ import annotations

import datetime as dt
import json
import sys
from pathlib import Path
from typing import Callable

LEDGER_LISTS = ("probe_candidates", "watchlist", "skiplist", "structurally_gone")
VOLATILE_CANDIDATE_FIELDS = {"auto_probe", "last_probe", "freshness_days", "expires"}


def load(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def entries(ledger: dict, list_name: str) -> list[dict]:
    rows = ledger.get(list_name, [])
    if not isinstance(rows, list):
        raise ValueError(f"{list_name}: expected list")
    return rows


def _parse_date(value: str, label: str) -> dt.date:
    try:
        return dt.date.fromisoformat(value)
    except ValueError as exc:
        raise ValueError(f"{label}: expected YYYY-MM-DD, got {value!r}") from exc


def validate(
    ledger: dict,
    *,
    probe_family_for: Callable[[str, str, str | None], str],
    candidate_members: set[tuple[str, str]] | None = None,
    today: dt.date | None = None,
    allowlist_members: set[tuple[str, str]] | None = None,
) -> None:
    today = today or dt.date.today()
    seen: dict[tuple[str, str], str] = {}
    list_keys = {name: set() for name in LEDGER_LISTS}

    for list_name in LEDGER_LISTS:
        for idx, entry in enumerate(entries(ledger, list_name)):
            if not isinstance(entry, dict):
                raise ValueError(f"{list_name}[{idx}]: expected object")
            platform = entry.get("platform")
            model = entry.get("model")
            reason = entry.get("reason")
            if not platform or not model:
                raise ValueError(f"{list_name}[{idx}]: platform and model are required")
            if not reason:
                raise ValueError(f"{list_name}[{idx}] {platform}/{model}: reason is required")
            key = (platform, model)
            if key in seen:
                raise ValueError(f"{platform}/{model}: appears in both {seen[key]} and {list_name}")
            seen[key] = list_name
            list_keys[list_name].add(key)

            if list_name == "probe_candidates":
                volatile = sorted(VOLATILE_CANDIDATE_FIELDS & set(entry))
                if volatile:
                    raise ValueError(
                        f"probe_candidates {platform}/{model}: volatile fields are not allowed: {volatile}"
                    )
                probe_family_for(platform, model, entry.get("probe_family"))
                if candidate_members is not None and key not in candidate_members:
                    raise ValueError(f"probe_candidates {platform}/{model}: entry missing from candidates")
            elif list_name == "watchlist":
                last_probe = entry.get("last_probe")
                expires = entry.get("expires")
                freshness_days = entry.get("freshness_days")
                if freshness_days is not None and (
                    not isinstance(freshness_days, int) or freshness_days < 1
                ):
                    raise ValueError(
                        f"watchlist {platform}/{model}: freshness_days must be a positive integer"
                    )
                if last_probe:
                    probed_at = _parse_date(
                        str(last_probe), f"watchlist {platform}/{model} last_probe"
                    )
                    if probed_at > today:
                        raise ValueError(
                            f"watchlist {platform}/{model}: last_probe {probed_at} is in the future"
                        )
                elif not expires:
                    raise ValueError(f"watchlist {platform}/{model}: last_probe or expires is required")
                if expires:
                    _parse_date(str(expires), f"watchlist {platform}/{model} expires")
                elif freshness_days is None:
                    raise ValueError(
                        f"watchlist {platform}/{model}: freshness_days or expires is required"
                    )

    blocked = list_keys["skiplist"] | list_keys["structurally_gone"]
    tracked = list_keys["probe_candidates"] | list_keys["watchlist"]
    overlap = tracked & blocked
    if overlap:
        rendered = ", ".join(f"{platform}/{model}" for platform, model in sorted(overlap))
        raise ValueError(
            "probe_candidates/watchlist cannot overlap skiplist/structurally_gone: " + rendered
        )
    if allowlist_members:
        conflicts = allowlist_members & list_keys["skiplist"]
        if conflicts:
            rendered = ", ".join(f"{platform}/{model}" for platform, model in sorted(conflicts))
            raise ValueError(f"servable allowlist cannot overlap skiplist: {rendered}")


def watchlist_freshness(
    ledger: dict, *, today: dt.date | None = None
) -> list[dict[str, object]]:
    today = today or dt.date.today()
    stale: list[dict[str, object]] = []
    for entry in entries(ledger, "watchlist"):
        expires = entry.get("expires")
        last_probe = entry.get("last_probe")
        freshness_days = entry.get("freshness_days")
        if expires:
            deadline = _parse_date(
                str(expires), f"watchlist {entry.get('platform')}/{entry.get('model')} expires"
            )
        elif last_probe and isinstance(freshness_days, int):
            deadline = _parse_date(
                str(last_probe),
                f"watchlist {entry.get('platform')}/{entry.get('model')} last_probe",
            ) + dt.timedelta(days=freshness_days)
        else:
            continue
        if today > deadline:
            stale.append(
                {
                    "platform": entry["platform"],
                    "model": entry["model"],
                    "deadline": deadline.isoformat(),
                    "days_stale": (today - deadline).days,
                }
            )
    return sorted(stale, key=lambda row: (str(row["platform"]), str(row["model"])))


def warn_stale_watchlist(ledger: dict, *, today: dt.date | None = None) -> None:
    stale = watchlist_freshness(ledger, today=today)
    if not stale:
        return
    oldest = max(int(row["days_stale"]) for row in stale)
    print(
        f"[refresh] WARN: {len(stale)} watchlist entr{'y is' if len(stale) == 1 else 'ies are'} "
        f"stale (oldest={oldest}d); candidates remain usable. "
        "Run `refresh-servable-allowlist.py watchlist-status` for details.",
        file=sys.stderr,
    )


def augment_candidates(
    candidates: dict[str, list[str]],
    ledger: dict,
    *,
    probe_families_by_platform: dict[str, tuple[str, ...]],
    family_platform: dict[str, str],
    probe_family_for: Callable[[str, str, str | None], str],
) -> dict[str, list[str]]:
    out = {key: list(values) for key, values in candidates.items()}
    for entry in entries(ledger, "probe_candidates"):
        platform = entry["platform"]
        model = entry["model"]
        family = probe_family_for(platform, model, entry.get("probe_family"))
        for peer_family in probe_families_by_platform[platform]:
            out[peer_family] = [mid for mid in out.get(peer_family, []) if mid != model]
        out.setdefault(family, []).append(model)

    blocked = {
        (entry["platform"], entry["model"])
        for list_name in ("skiplist", "structurally_gone")
        for entry in entries(ledger, list_name)
    }
    for family, platform in family_platform.items():
        if family in out:
            out[family] = [model for model in out[family] if (platform, model) not in blocked]
    for family in out:
        out[family] = sorted(set(out[family]))
    return out


def validate_results(servable: dict[str, set[str]], ledger: dict) -> None:
    blocked = {
        (entry["platform"], entry["model"])
        for list_name in ("skiplist", "structurally_gone")
        for entry in entries(ledger, list_name)
    }
    observed = {(platform, model) for platform, models in servable.items() for model in models}
    conflicts = observed & blocked
    if conflicts:
        rendered = ", ".join(f"{platform}/{model}" for platform, model in sorted(conflicts))
        raise SystemExit(
            f"FATAL: probe results mark skiplist/structurally_gone model as servable: {rendered}"
        )
