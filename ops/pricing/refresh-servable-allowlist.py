#!/usr/bin/env python3
"""refresh-servable-allowlist.py — re-runnable refresh of the public-catalog
servable model allowlists.

Pipeline (operator runs locally with AWS creds; the probe needs prod SSM):

    derive candidates from the litellm catalog
      -> live-probe each through prod (ops/pricing/probe-servable-models.sh via
         ops/observability/run-probe.sh)
      -> keep verdict==servable, de-duplicate dated snapshots
      -> splice the two Go maps in
         backend/internal/service/pricing_catalog_supported_models_tk.go
      -> optionally open a PR

The classification engine is the probe script; this orchestrator owns the
deterministic glue (candidate derivation, de-dup, Go splice) — all covered by
`selftest` so preflight can verify it without touching prod.

Subcommands:
  candidates           print the per-family candidate model lists (no prod)
  probe                live-probe; print the raw TSV results (needs prod SSM)
  apply --results F    de-dup F's servable rows and splice the Go maps
  run [--open-pr]      probe + apply in one shot
  selftest             deterministic unit checks (no prod); used by preflight

Verdict reminder: a model is kept iff the probe saw a real 200. canonical /
advertised status is irrelevant (operator directive 实测通过的才行).
"""
from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import re
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
CATALOG = REPO / "backend/resources/model-pricing/model_prices_and_context_window.json"
GO_FILE = REPO / "backend/internal/service/pricing_catalog_supported_models_tk.go"
PROBE = REPO / "ops/pricing/probe-servable-models.sh"
PROBE_LIB = REPO / "ops/pricing/probe_reserved_resources.sh"
TRAFFIC_PROBE = REPO / "ops/pricing/probe-traffic-proven-models.sh"
RUN_PROBE = REPO / "ops/observability/run-probe.sh"
REPROBE_LEDGER = REPO / "ops/pricing/servable-reprobe-ledger.json"

# 24h-traffic short-circuit (additive optimization, gated by a flag/env so the
# default stays the conservative full probe). A candidate model that already
# served real successful traffic in this window is proven servable and skips the
# SSM probe batch entirely. Absence of traffic is NOT a negative signal — those
# candidates are still probed normally.
DEFAULT_TRAFFIC_HOURS = 24

# Dated snapshot suffix, both fleet conventions: anthropic "-YYYYMMDD"
# (claude-opus-4-5-20251101) and openai "-YYYY-MM-DD" (gpt-5.5-2026-04-23).
DATED_RE = re.compile(r"^(.+)-(?:\d{8}|\d{4}-\d{2}-\d{2})$")

# Models per SSM invocation. The full catalog is ~145 candidates; a single
# run-probe command that takes longer than the `aws ssm wait command-executed`
# window (~100s) is reported as still-InProgress (failure). At ~6s/model
# (request + REQ_SLEEP) a batch of 12 stays well under that window, so the probe
# is chunked into several run-probe invocations and the TSV concatenated.
BATCH_SIZE = 12

# ----- gemini / Vertex (newapi fifth platform) -----
# Live Vertex capacity now serves from PROD group_id=16 (current display name `Google-Vertex`, ids 47/57/58/59/74);
# the old edge-us6 `google` group was emptied (account soft-deleted). gemini batches
# therefore target prod and reach it through the public gateway (external curl), same as
# the other newapi families. (The probe script's PROBE_GEMINI_SOURCE_GROUP_ID default is
# 16 to match.)
GEMINI_TARGET = "prod"
# Predict-API media models are NOT in upstream /v1/models discovery; seed them
# explicitly (they ride the model_mapping today). Imagen -> images, veo -> video.
GEMINI_PREDICT_MODELS = (
    "imagen-4.0-generate-001",
    "imagen-4.0-fast-generate-001",
    "imagen-4.0-ultra-generate-001",
    "veo-3.1-generate-001",
)
# Catch-all SCOPE = core generative families only (chat / image / video). Exotic
# families are excluded from the candidate set so an empty model_mapping never
# silently $0-serves them: gemma open-weight, lyria music, deep-research,
# robotics, antigravity, computer-use, tts/audio.
GEMINI_EXCLUDE_RE = re.compile(r"gemma-|lyria-|deep-research|robotics|antigravity|computer-use|tts")

PROBE_FAMILIES_BY_PLATFORM = {
    "anthropic": ("anthropic",),
    "openai": ("openai_chat", "openai_responses", "openai_image"),
    # gemini_chat_image: the generateContent image models (gemini-*-image,
    # nano-banana) ride the /v1/chat/completions surface, NOT the imagen
    # /v1/images/generations predict API — a distinct family so they probe the
    # right endpoint. imagen-* stays in gemini_image.
    "gemini": ("gemini_chat", "gemini_chat_image", "gemini_image", "gemini_video"),
}
# Inverse of PROBE_FAMILIES_BY_PLATFORM: probe-family -> allowlist platform.
FAMILY_PLATFORM = {
    family: platform
    for platform, families in PROBE_FAMILIES_BY_PLATFORM.items()
    for family in families
}
GO_ALLOWLIST_PLATFORMS = ("anthropic", "openai", "gemini", "antigravity", "grok")
# Probe families that submit a REAL paid generation task (video). --skip-video
# excludes them from live probing AND carries their current allowlist entries
# forward un-probed, so a chat/image refresh never drops already-servable priced
# veo/seedance ids. Derived from the family table so a future *_video family is
# covered automatically.
VIDEO_FAMILIES = frozenset(f for f in FAMILY_PLATFORM if f.endswith("_video"))
REPROBE_LISTS = ("watchlist", "skiplist", "deadlist")


# ----- vendor → platform (mirrors service.inferPlatformFromVendor) -----
def platform_of(vendor: str) -> str:
    if vendor in ("openai", "azure_openai"):
        return "openai"
    if vendor == "anthropic":
        return "anthropic"
    return ""


# ----- candidate derivation (deterministic, no prod) -----
def derive_candidates(catalog: dict) -> dict[str, list[str]]:
    """Split the priced anthropic/openai catalog entries into probe families.
    OpenAI codex ids go to /v1/responses, image ids to /v1/images, the rest to
    /v1/chat/completions."""
    out = {"anthropic": [], "openai_chat": [], "openai_responses": [], "openai_image": []}
    for mid, entry in catalog.items():
        if mid == "sample_spec" or not isinstance(entry, dict):
            continue
        if entry.get("input_cost_per_token") is None and entry.get("output_cost_per_token") is None:
            continue
        plat = platform_of(entry.get("litellm_provider", ""))
        if plat == "anthropic":
            out["anthropic"].append(mid)
        elif plat == "openai":
            if "image" in mid:
                out["openai_image"].append(mid)
            elif "codex" in mid:
                out["openai_responses"].append(mid)
            else:
                out["openai_chat"].append(mid)
    for k in out:
        out[k] = sorted(set(out[k]))
    return out


def split_gemini_families(discovered: list[str]) -> dict[str, list[str]]:
    """Split discovered Vertex model ids (+ the predict-API seed) into probe
    families, keeping ONLY core generative families. chat -> /v1/chat/completions,
    image -> /v1/images/generations, video -> /v1/video/generations. Exotic
    families (see GEMINI_EXCLUDE_RE) are dropped so the catch-all never $0-serves
    an unpriced niche model."""
    fams: dict[str, list[str]] = {"gemini_chat": [], "gemini_chat_image": [], "gemini_image": [], "gemini_video": []}
    seen: set[str] = set()
    for mid in list(discovered) + list(GEMINI_PREDICT_MODELS):
        mid = mid.strip()
        if not mid or mid in seen:
            continue
        seen.add(mid)
        if GEMINI_EXCLUDE_RE.search(mid):
            continue
        if mid.startswith("veo-"):
            fams["gemini_video"].append(mid)
        elif mid.startswith("imagen-"):
            # imagen-* uses the /v1/images/generations predict API.
            fams["gemini_image"].append(mid)
        elif "image" in mid or "nano-banana" in mid:
            # gemini-*-image / nano-banana generate via the chat/generateContent
            # surface, not the images predict API → probe through /v1/chat/completions.
            fams["gemini_chat_image"].append(mid)
        elif mid.startswith("gemini-"):
            fams["gemini_chat"].append(mid)
        # other-vendor ids are ignored (not part of the google catch-all scope)
    for k in fams:
        fams[k] = sorted(set(fams[k]))
    return fams


def load_discovered(path: str | None) -> list[str]:
    """Load the discovered Vertex model universe for gemini candidates. Accepts a
    JSON object (account.credentials.model_pricing_status — keys used), a JSON
    list, or a newline list. None/empty -> [] (media-only seed still applies)."""
    if not path:
        return []
    raw = Path(path).read_text(encoding="utf-8").strip()
    if not raw:
        return []
    if raw.lstrip()[:1] in ("[", "{"):
        data = json.loads(raw)
        return list(data.keys()) if isinstance(data, dict) else list(data)
    return [ln.strip() for ln in raw.splitlines() if ln.strip()]


def build_candidates(catalog: dict, discovered: list[str]) -> dict[str, list[str]]:
    """anthropic/openai families from the litellm catalog + gemini families from
    the discovered Vertex universe (+ predict seed)."""
    c = derive_candidates(catalog)
    c.update(split_gemini_families(discovered))
    return c


def _probe_family_for(platform: str, model: str, probe_family: str | None = None) -> str:
    if probe_family:
        allowed = PROBE_FAMILIES_BY_PLATFORM.get(platform, ())
        if probe_family not in allowed:
            raise ValueError(f"{platform}/{model}: probe_family {probe_family!r} is not valid for {platform}")
        return probe_family
    if platform == "anthropic":
        return "anthropic"
    if platform == "openai":
        if "image" in model:
            return "openai_image"
        if "codex" in model:
            return "openai_responses"
        return "openai_chat"
    if platform == "gemini":
        if model.startswith("veo-"):
            return "gemini_video"
        if model.startswith("imagen-"):
            return "gemini_image"
        if "image" in model or "nano-banana" in model:
            return "gemini_chat_image"
        return "gemini_chat"
    raise ValueError(f"{platform}/{model}: refresh tool cannot probe this platform")


def load_reprobe_ledger(path: Path = REPROBE_LEDGER) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def _parse_date(value: str, label: str) -> dt.date:
    try:
        return dt.date.fromisoformat(value)
    except ValueError as exc:
        raise ValueError(f"{label}: expected YYYY-MM-DD, got {value!r}") from exc


def _known_allowlist_members(text: str) -> set[tuple[str, str]]:
    out: set[tuple[str, str]] = set()
    for platform in GO_ALLOWLIST_PLATFORMS:
        begin = f"\t// servable-allowlist:begin {platform}\n"
        end = f"\t// servable-allowlist:end {platform}"
        bi = text.find(begin)
        ei = text.find(end)
        if bi < 0 or ei < 0 or ei < bi:
            continue
        block = text[bi + len(begin) : ei]
        for match in re.finditer(r'^\s*"([^"]+)":\s*\{\},', block, flags=re.MULTILINE):
            out.add((platform, match.group(1)))
    return out


def carried_forward_rows(text: str, skip_families: set[str]) -> str:
    """Emit current Go-allowlist members belonging to a SKIPPED probe family as
    synthetic `servable` TSV rows (code 000). --skip-video uses this so a chat/image
    refresh preserves already-servable video ids (veo …) verbatim instead of the
    per-platform splice dropping them. Reuses _probe_family_for so the family
    classification matches the live-probe path exactly."""
    rows: list[str] = []
    for platform, model in sorted(_known_allowlist_members(text)):
        try:
            fam = _probe_family_for(platform, model)
        except ValueError:
            continue  # platform the refresh tool does not probe (antigravity/grok)
        if fam in skip_families:
            rows.append(f"{platform}\t{model}\t000\tservable")
    return "\n".join(rows)


def _candidate_members(candidates: dict[str, list[str]]) -> set[tuple[str, str]]:
    out: set[tuple[str, str]] = set()
    for family, platform in FAMILY_PLATFORM.items():
        out.update((platform, model) for model in candidates.get(family, []))
    return out


def _ledger_entries(ledger: dict, list_name: str) -> list[dict]:
    entries = ledger.get(list_name, [])
    if not isinstance(entries, list):
        raise ValueError(f"{list_name}: expected list")
    return entries


def validate_reprobe_ledger(
    ledger: dict,
    *,
    today: dt.date | None = None,
    allowlist_members: set[tuple[str, str]] | None = None,
    candidates: dict[str, list[str]] | None = None,
) -> None:
    today = today or dt.date.today()
    seen: dict[tuple[str, str], str] = {}
    watch_keys: set[tuple[str, str]] = set()
    skip_keys: set[tuple[str, str]] = set()
    dead_keys: set[tuple[str, str]] = set()
    candidate_keys = _candidate_members(candidates) if candidates is not None else set()

    for list_name in REPROBE_LISTS:
        for idx, entry in enumerate(_ledger_entries(ledger, list_name)):
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
            if list_name == "watchlist":
                watch_keys.add(key)
                if entry.get("auto_probe", False):
                    _probe_family_for(platform, model, entry.get("probe_family"))
                last_probe = entry.get("last_probe")
                expires = entry.get("expires")
                freshness_days = entry.get("freshness_days")
                if last_probe:
                    probed_at = _parse_date(str(last_probe), f"watchlist {platform}/{model} last_probe")
                    if probed_at > today:
                        raise ValueError(f"watchlist {platform}/{model}: last_probe {probed_at} is in the future")
                    if freshness_days is not None:
                        if not isinstance(freshness_days, int) or freshness_days < 1:
                            raise ValueError(f"watchlist {platform}/{model}: freshness_days must be a positive integer")
                        expires_at = probed_at + dt.timedelta(days=freshness_days)
                        if today > expires_at:
                            raise ValueError(
                                f"watchlist {platform}/{model}: last_probe {probed_at} is stale "
                                f"(freshness_days={freshness_days}, expired {expires_at})"
                            )
                elif not expires:
                    raise ValueError(f"watchlist {platform}/{model}: last_probe or expires is required")
                if expires:
                    expires_at = _parse_date(str(expires), f"watchlist {platform}/{model} expires")
                    if today > expires_at:
                        raise ValueError(f"watchlist {platform}/{model}: expires {expires_at} is stale")
                if entry.get("auto_probe") and candidates is not None and key not in candidate_keys:
                    raise ValueError(f"watchlist {platform}/{model}: auto_probe entry missing from candidates")
            elif list_name == "skiplist":
                skip_keys.add(key)
            else:
                dead_keys.add(key)

    blocked = skip_keys | dead_keys
    overlap = watch_keys & blocked
    if overlap:
        rendered = ", ".join(f"{platform}/{model}" for platform, model in sorted(overlap))
        raise ValueError(f"watchlist cannot overlap skiplist/deadlist: {rendered}")
    if allowlist_members:
        conflicts = allowlist_members & blocked
        if conflicts:
            rendered = ", ".join(f"{platform}/{model}" for platform, model in sorted(conflicts))
            raise ValueError(f"servable allowlist cannot overlap skiplist/deadlist: {rendered}")


def augment_candidates_with_watchlist(candidates: dict[str, list[str]], ledger: dict) -> dict[str, list[str]]:
    out = {k: list(v) for k, v in candidates.items()}
    for entry in _ledger_entries(ledger, "watchlist"):
        if not entry.get("auto_probe", False):
            continue
        platform = entry["platform"]
        model = entry["model"]
        family = _probe_family_for(platform, model, entry.get("probe_family"))
        for peer_family in PROBE_FAMILIES_BY_PLATFORM[platform]:
            out[peer_family] = [mid for mid in out.get(peer_family, []) if mid != model]
        out.setdefault(family, []).append(model)
    blocked = {
        (entry["platform"], entry["model"])
        for list_name in ("skiplist", "deadlist")
        for entry in _ledger_entries(ledger, list_name)
    }
    for family, platforms in (
        ("anthropic", ("anthropic",)),
        ("openai_chat", ("openai",)),
        ("openai_responses", ("openai",)),
        ("openai_image", ("openai",)),
        ("gemini_chat", ("gemini",)),
        ("gemini_chat_image", ("gemini",)),
        ("gemini_image", ("gemini",)),
        ("gemini_video", ("gemini",)),
    ):
        if family in out:
            platform = platforms[0]
            out[family] = [model for model in out[family] if (platform, model) not in blocked]
    for family in out:
        out[family] = sorted(set(out[family]))
    return out


def validate_results_against_reprobe_ledger(servable: dict[str, set[str]], ledger: dict) -> None:
    blocked = {
        (entry["platform"], entry["model"])
        for list_name in ("skiplist", "deadlist")
        for entry in _ledger_entries(ledger, list_name)
    }
    observed = {(platform, model) for platform, models in servable.items() for model in models}
    conflicts = observed & blocked
    if conflicts:
        rendered = ", ".join(f"{platform}/{model}" for platform, model in sorted(conflicts))
        raise SystemExit(f"FATAL: probe results mark skiplist/deadlist model as servable: {rendered}")


def build_probe_candidates(catalog: dict, discovered: list[str]) -> tuple[dict[str, list[str]], dict]:
    ledger = load_reprobe_ledger()
    cands = augment_candidates_with_watchlist(build_candidates(catalog, discovered), ledger)
    validate_reprobe_ledger(ledger, allowlist_members=_known_allowlist_members(GO_FILE.read_text(encoding="utf-8")), candidates=cands)
    return cands, ledger


# ----- 24h-traffic short-circuit (deterministic glue; transport is run-probe) -----
def parse_traffic_rows(text: str) -> dict[str, set[str]]:
    """probe-traffic-proven-models.sh TSV -> accounts.platform -> set of served
    model ids. Lines: platform\\tmodel\\thits. The platform column here is the
    SERVING account's platform (human context only); buckets are re-decided from
    the candidate set in proven_servable_from_traffic, so unknown platforms are
    harmless. Malformed lines are ignored."""
    out: dict[str, set[str]] = {}
    for line in text.splitlines():
        parts = line.rstrip("\n").split("\t")
        if len(parts) != 3:
            continue
        plat, model, _hits = (p.strip() for p in parts)
        if not plat or not model:
            continue
        out.setdefault(plat, set()).add(model)
    return out


def candidate_model_platforms(candidates: dict[str, list[str]]) -> dict[str, set[str]]:
    """model id -> set of allowlist platforms it is a candidate for."""
    out: dict[str, set[str]] = {}
    for platform, model in _candidate_members(candidates):
        out.setdefault(model, set()).add(platform)
    return out


def proven_servable_from_traffic(
    traffic: dict[str, set[str]], candidates: dict[str, list[str]]
) -> dict[str, set[str]]:
    """Intersect the 24h-traffic-served models with the candidate set, bucketing
    each by the CANDIDATE platform (NOT the serving-account platform — Vertex is
    served under accounts.platform='newapi', so account platform is unreliable).

    Properties enforced here (the correctness contract):
      * PURELY ADDITIVE — only models that BOTH appear in traffic AND are known
        candidates survive. A candidate absent from traffic is simply not returned
        (it stays in the probe set). A served model that is not a candidate is
        dropped (never injected into the allowlist).
      * Blocked models (skiplist/deadlist) are already absent from `candidates`
        (augment_candidates_with_watchlist removed them), so they can never appear
        here even with a real traffic hit — a deadlist model cannot revive on one
        successful request. validate_results_against_reprobe_ledger is still run on
        the result by callers as defense-in-depth.
    Returns platform -> set of proven-servable model ids."""
    model_platforms = candidate_model_platforms(candidates)
    served = {model for models in traffic.values() for model in models}
    out: dict[str, set[str]] = {}
    for model in served:
        for platform in model_platforms.get(model, ()):
            out.setdefault(platform, set()).add(model)
    return out


def remove_proven_from_candidates(
    candidates: dict[str, list[str]], proven: dict[str, set[str]]
) -> dict[str, list[str]]:
    """Drop proven (platform, model) pairs from the probe families so the SSM
    probe never re-tests a model the traffic already proved. Returns a new dict;
    the input is untouched."""
    proven_pairs = {(platform, model) for platform, models in proven.items() for model in models}
    out: dict[str, list[str]] = {}
    for family, models in candidates.items():
        platform = FAMILY_PLATFORM.get(family)
        out[family] = [model for model in models if (platform, model) not in proven_pairs]
    return out


def proven_as_tsv(proven: dict[str, set[str]]) -> str:
    """Render the traffic-proven set as servable probe rows (platform\\tmodel\\t200
    \\tservable) so the `probe` subcommand's TSV stays complete for a later
    `apply --results`."""
    return "\n".join(
        f"{platform}\t{model}\t200\tservable"
        for platform in sorted(proven)
        for model in sorted(proven[platform])
    )


def _log_proven_skip(proven: dict[str, set[str]], hours: int) -> None:
    total = sum(len(models) for models in proven.values())
    if total == 0:
        print(
            f"[refresh] no candidate model matched the last {hours}h of successful "
            "traffic — probing the full candidate set",
            file=sys.stderr,
        )
        return
    rendered = ", ".join(
        f"{platform}/{model}"
        for platform in sorted(proven)
        for model in sorted(proven[platform])
    )
    print(
        f"[refresh] skipping {total} models proven by {hours}h traffic: {rendered}",
        file=sys.stderr,
    )


def fetch_traffic_proven(hours: int = DEFAULT_TRAFFIC_HOURS, target: str = "prod") -> dict[str, set[str]]:
    """Pull the (platform, model) pairs that served successful traffic in the last
    `hours` hours from prod, via the same run-probe SSM transport the probe uses."""
    if not RUN_PROBE.exists() or not TRAFFIC_PROBE.exists():
        raise SystemExit("FATAL: run-probe.sh or probe-traffic-proven-models.sh missing")
    cmd = [
        "bash", str(RUN_PROBE), "--target", target, "--script", str(TRAFFIC_PROBE),
        "--timeout-seconds", "120", "--env", f"TRAFFIC_HOURS={hours}",
    ]
    proc = subprocess.run(cmd, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    sys.stderr.write(proc.stderr)
    if proc.returncode != 0:
        raise SystemExit(f"FATAL: traffic-proven query failed (exit {proc.returncode}) — see stderr above")
    return parse_traffic_rows(proc.stdout)


def short_circuit_by_traffic(
    candidates: dict[str, list[str]],
    ledger: dict,
    *,
    hours: int = DEFAULT_TRAFFIC_HOURS,
    target: str = "prod",
) -> tuple[dict[str, list[str]], dict[str, set[str]]]:
    """Fetch 24h traffic, derive the proven-servable candidates, validate them
    against the reprobe ledger, log the skips, and return (reduced_candidates,
    proven). reduced_candidates has the proven models removed so they are not
    re-probed; proven is merged into the servable results by the caller."""
    traffic = fetch_traffic_proven(hours=hours, target=target)
    proven = proven_servable_from_traffic(traffic, candidates)
    # Defense-in-depth: proven ⊆ candidates (which already exclude skiplist/deadlist),
    # so this never fires — but it guards against any future candidate-derivation drift.
    validate_results_against_reprobe_ledger(proven, ledger)
    _log_proven_skip(proven, hours)
    return remove_proven_from_candidates(candidates, proven), proven


# ----- de-duplication (operator rule) -----
def dedup(servable: set[str]) -> list[str]:
    """Drop a dated `<base>-YYYYMMDD` form when its non-dated base also serves,
    and drop `-thinking` pricing pseudo-entries. Returns sorted survivors."""
    kept = set()
    for mid in servable:
        if mid.endswith("-thinking"):
            continue
        m = DATED_RE.match(mid)
        if m and m.group(1) in servable:
            continue
        kept.add(mid)
    return sorted(kept)


# ----- results TSV parsing -----
def parse_results(text: str) -> dict[str, set[str]]:
    """platform -> set of servable model ids. Lines: platform\\tmodel\\tcode\\tverdict."""
    out: dict[str, set[str]] = {"anthropic": set(), "openai": set(), "gemini": set(), "grok": set()}
    for line in text.splitlines():
        parts = line.rstrip("\n").split("\t")
        if len(parts) != 4:
            continue
        plat, model, _code, verdict = parts
        if plat not in out or model == "*":
            continue
        if verdict == "servable":
            out[plat].add(model)
    return out


# ----- Go splice (deterministic) -----
def splice_go(text: str, platform: str, ids: list[str]) -> str:
    begin = f"\t// servable-allowlist:begin {platform}\n"
    end = f"\t// servable-allowlist:end {platform}"
    bi = text.find(begin)
    ei = text.find(end)
    if bi < 0 or ei < 0 or ei < bi:
        raise SystemExit(f"FATAL: splice markers for {platform} not found in {GO_FILE.name}")
    body = "".join(f'\t"{mid}": {{}},\n' for mid in ids)
    return text[: bi + len(begin)] + body + text[ei:]


def write_allowlists(servable: dict[str, set[str]], ledger: dict | None = None) -> dict[str, list[str]]:
    if ledger is not None:
        validate_results_against_reprobe_ledger(servable, ledger)
    text = GO_FILE.read_text(encoding="utf-8")
    platforms = ("anthropic", "openai", "gemini")
    final = {p: dedup(servable.get(p, set())) for p in platforms}
    for plat in platforms:
        if not final[plat]:
            # Empty => this platform was not probed in this run (a partial refresh,
            # e.g. gemini-only). Skip so we never WIPE an existing allowlist with a
            # subset probe. A genuine "all dropped" is rare; clear it by hand if so.
            print(f"[refresh] {plat}: 0 servable in results — leaving existing block untouched", file=sys.stderr)
            continue
        text = splice_go(text, plat, final[plat])
    GO_FILE.write_text(text, encoding="utf-8")
    subprocess.run(["gofmt", "-w", str(GO_FILE)], check=True)
    return final


# ----- batching -----
def chunk(items: list[str], size: int) -> list[list[str]]:
    return [items[i : i + size] for i in range(0, len(items), size)] if items else []


# ----- live probe -----
# anthropic is handled separately (edge rotation), not in this single-target table.
ENV_BY_FAMILY = (
    ("OPENAI_CHAT_MODELS", "openai_chat", "prod"),
    ("OPENAI_RESPONSES_MODELS", "openai_responses", "prod"),
    ("OPENAI_IMAGE_MODELS", "openai_image", "prod"),
    ("GEMINI_CHAT_MODELS", "gemini_chat", GEMINI_TARGET),
    ("GEMINI_CHATIMAGE_MODELS", "gemini_chat_image", GEMINI_TARGET),
    ("GEMINI_IMAGE_MODELS", "gemini_image", GEMINI_TARGET),
    ("GEMINI_VIDEO_MODELS", "gemini_video", GEMINI_TARGET),
)

# ----- anthropic: edge-native probe, rotated across deployable edges -----
# claude is served by the edges' own native OAuth pool; prod only holds cc-* mirror
# accounts that relay to those edges and cool down on any edge blip (a prod-gateway
# probe then empty-pools -> false "unservable"). So anthropic probes the edges directly
# and a model is servable if ANY edge serves it. One healthy edge is enough; unhealthy
# edges config_error cheaply (no schedulable native account) and we rotate past them.
FLEET_JSON = REPO / "deploy/aws/lightsail/edge-targets-lightsail.json"


def deployable_edges() -> list[str]:
    """Rotation order = edges marked deployable in the lightsail fleet manifest."""
    try:
        data = json.loads(FLEET_JSON.read_text(encoding="utf-8"))
        edges = [k for k, v in data.get("targets", {}).items() if v.get("deployable")]
        if edges:
            return edges
    except Exception as e:  # noqa: BLE001 — manifest optional; fall back to known edges
        print(f"[refresh] WARN: fleet manifest unreadable ({e}); using fallback edges", file=sys.stderr)
    return ["us3", "us4", "us5", "us6"]


def _run_probe_batch(env_args: list[str], target: str = "prod") -> str:
    cmd = [
        "bash", str(RUN_PROBE), "--target", target, "--script", str(PROBE),
        "--with", str(PROBE_LIB),
        "--timeout-seconds", "600", *env_args,
    ]
    proc = subprocess.run(cmd, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    sys.stderr.write(proc.stderr)
    if proc.returncode != 0:
        raise SystemExit(f"FATAL: probe batch failed (exit {proc.returncode}) — see stderr above")
    # run-probe prefixes wrapper lines; keep only TSV rows (4 tab fields).
    return "\n".join(ln for ln in proc.stdout.splitlines() if ln.count("\t") == 3)


def _servable_tag(tsv: str, tag: str) -> set[str]:
    """Servable model ids emitted under a specific platform tag (col 1)."""
    out: set[str] = set()
    for ln in tsv.splitlines():
        p = ln.split("\t")
        if len(p) == 4 and p[0] == tag and p[1] != "*" and p[3] == "servable":
            out.add(p[1])
    return out


# Prod mirror sub-pools (warning-only relay check): name prefix -> (emit tag, label).
ANTHROPIC_PROD_MIRRORS = (
    ("anthropic_prodmirror_cc", "prod cc-* (anthropic-OAuth) relay"),
    ("anthropic_prodmirror_kiro", "prod kiro-* (Kiro) relay"),
)


def live_probe(candidates: dict[str, list[str]], skip_video: bool = False) -> str:
    if not RUN_PROBE.exists() or not PROBE.exists():
        raise SystemExit("FATAL: run-probe.sh or probe-servable-models.sh missing")
    rows: list[str] = []

    # anthropic: rotate across deployable edges; a model is servable if ANY edge serves
    # it. Stop rotating a batch once every model is confirmed (one healthy edge usually
    # serves the whole shared OAuth pool). Edges with no schedulable native account
    # config_error (ignored by parse_results), so an all-cold fleet leaves the anthropic
    # allowlist untouched via the 0-servable guard rather than wiping it.
    anth = candidates.get("anthropic", [])
    if anth:
        edges = deployable_edges()
        abatches = chunk(anth, BATCH_SIZE)
        edge_rows: list[str] = []
        print(f"[refresh] anthropic: {len(anth)} models × up to {len(edges)} edge(s) {edges} (servable if ANY serves) …", file=sys.stderr)
        for ci, c in enumerate(abatches, 1):
            served: set[str] = set()
            for ei, edge in enumerate(edges, 1):
                print(f"[refresh] anthropic batch {ci}/{len(abatches)} via edge:{edge} ({ei}/{len(edges)}; {len(served)}/{len(c)} served) …", file=sys.stderr)
                out = _run_probe_batch(["--env", f"ANTHROPIC_MODELS={' '.join(c)}"], target=f"edge:{edge}")
                if out:
                    edge_rows.append(out)
                    served |= _servable_tag(out, "anthropic")
                if len(served) >= len(c):
                    break  # whole batch confirmed on some edge — no need to rotate further
            if len(served) < len(c):
                print(f"[refresh] anthropic batch {ci}: {len(served)}/{len(c)} served after all edges (rest inconclusive)", file=sys.stderr)
        rows.extend(edge_rows)

        # Prod relay-health (warning-only): the edges are the source of truth, but customers
        # reach claude through prod's mirror accounts (cc-* anthropic-OAuth + kiro-* Kiro).
        # Re-probe the edge-servable set via the prod gateway per mirror sub-pool and WARN
        # on any model an edge serves but a prod mirror does not (edge通 prod不通). These rows
        # carry distinct platform tags that parse_results ignores — never the allowlist.
        # NOTE: this only covers models actually edge-probed THIS run. A model short-
        # circuited by 24h traffic (--skip-proven-by-traffic) is removed from candidates
        # before live_probe, so it is absent from edge_servable and its prod relay health
        # is NOT checked here. That is acceptable (relay health is warning-only and the
        # model demonstrably served real traffic); run a full probe to relay-health-check
        # every served model.
        edge_servable = _servable_tag("\n".join(edge_rows), "anthropic")
        if edge_servable:
            mrows: list[str] = []
            for mb in chunk(sorted(edge_servable), max(1, BATCH_SIZE // 2)):
                out = _run_probe_batch(["--env", f"ANTHROPIC_PROD_MIRROR_MODELS={' '.join(mb)}"], target="prod")
                if out:
                    rows.append(out)
                    mrows.append(out)
            mtxt = "\n".join(mrows)
            for tag, label in ANTHROPIC_PROD_MIRRORS:
                missing = sorted(edge_servable - _servable_tag(mtxt, tag))
                if missing:
                    print(f"[refresh] WARNING: {len(missing)}/{len(edge_servable)} edge-servable model(s) NOT served via {label} (edge OK, prod relay not): {', '.join(missing)}", file=sys.stderr)
                else:
                    print(f"[refresh] {label}: all {len(edge_servable)} edge-servable models OK", file=sys.stderr)

    # Other families: one run-probe invocation per family-batch at a fixed target
    # (a single command spanning the whole catalog would exceed the SSM waiter window).
    # --skip-video drops video families here (a submit = a REAL paid task) and the
    # carried_forward_rows() tail below preserves their current allowlist entries.
    active_families = [
        (env_key, fam, target)
        for env_key, fam, target in ENV_BY_FAMILY
        if not (skip_video and fam in VIDEO_FAMILIES)
    ]
    batches: list[tuple[str, list[str], str]] = []
    for env_key, fam, target in active_families:
        for c in chunk(candidates.get(fam, []), BATCH_SIZE):
            batches.append((env_key, c, target))
    total = sum(len(candidates.get(f, [])) for _, f, _ in active_families)
    if skip_video:
        skipped = sum(len(candidates.get(f, [])) for _, f, _ in ENV_BY_FAMILY if f in VIDEO_FAMILIES)
        print(f"[refresh] --skip-video: NOT probing {skipped} video candidate(s) (real paid tasks); current video allowlist entries carried forward un-probed", file=sys.stderr)
    print(f"[refresh] probing {total} non-anthropic models in {len(batches)} batch(es) of <= {BATCH_SIZE} …", file=sys.stderr)
    for i, (env_key, models, target) in enumerate(batches, 1):
        print(f"[refresh] batch {i}/{len(batches)} ({env_key}@{target}: {len(models)}) …", file=sys.stderr)
        out = _run_probe_batch(["--env", f"{env_key}={' '.join(models)}"], target=target)
        if out:
            rows.append(out)
    if skip_video:
        cf = carried_forward_rows(GO_FILE.read_text(encoding="utf-8"), VIDEO_FAMILIES)
        if cf:
            rows.append(cf)
            print(f"[refresh] --skip-video: carried forward {len(cf.splitlines())} existing video allowlist entry(ies)", file=sys.stderr)
    return "\n".join(rows)


def open_pr(final: dict[str, list[str]]) -> None:
    import datetime

    stamp = datetime.datetime.now().strftime("%Y%m%d-%H%M")
    branch = f"chore/refresh-servable-allowlist-{stamp}"
    title = "chore(pricing-catalog): refresh servable model allowlist from live probe"
    body = (
        f"anthropic ({len(final['anthropic'])}): {', '.join(final['anthropic'])}\n"
        f"openai ({len(final['openai'])}): {', '.join(final['openai'])}\n"
        f"gemini ({len(final.get('gemini', []))}): {', '.join(final.get('gemini', []))}\n\n"
        "Regenerated by `ops/pricing/refresh-servable-allowlist.py run --open-pr`\n"
        "(live probe; kept verdict==servable, de-duplicated dated snapshots).\n\n"
        "no-web-impact\n"
    )
    def git(*a):
        subprocess.run(["git", *a], cwd=REPO, check=True)

    git("checkout", "-b", branch)
    git("add", str(GO_FILE))
    git("commit", "-m", f"{title}\n\n{body}")
    git("push", "-u", "origin", branch)
    pr = subprocess.run(
        ["gh", "pr", "create", "--base", "main", "--head", branch, "--title", title, "--body", body],
        cwd=REPO, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )
    sys.stderr.write(pr.stdout)
    if pr.returncode != 0:
        raise SystemExit("FATAL: gh pr create failed — branch pushed; create the PR manually.")
    print(f"[refresh] opened PR from {branch}", file=sys.stderr)


# ----- selftest (deterministic, no prod) -----
def selftest() -> int:
    cat = {
        "sample_spec": {"input_cost_per_token": 1.0},
        "claude-opus-4-8": {"input_cost_per_token": 1, "litellm_provider": "anthropic"},
        "claude-3-haiku-20240307": {"input_cost_per_token": 1, "litellm_provider": "anthropic"},
        "gpt-5.4": {"input_cost_per_token": 1, "litellm_provider": "openai"},
        "gpt-5.3-codex": {"input_cost_per_token": 1, "litellm_provider": "openai"},
        "gpt-image-dead": {"input_cost_per_token": 1, "litellm_provider": "openai"},
        "gpt-image-2": {"input_cost_per_token": 1, "litellm_provider": "openai"},
        "gpt-4o": {"litellm_provider": "openai"},  # unpriced -> skipped
        "gemini-2.5-pro": {"input_cost_per_token": 1, "litellm_provider": "vertex_ai"},
    }
    c = derive_candidates(cat)
    assert c["anthropic"] == ["claude-3-haiku-20240307", "claude-opus-4-8"], c["anthropic"]
    assert c["openai_chat"] == ["gpt-5.4"], c["openai_chat"]
    assert c["openai_responses"] == ["gpt-5.3-codex"], c["openai_responses"]
    assert c["openai_image"] == ["gpt-image-2", "gpt-image-dead"], c["openai_image"]
    assert "gpt-4o" not in sum(c.values(), []), "unpriced must be skipped"
    assert "gemini-2.5-pro" not in sum(c.values(), []), "vertex not a litellm-catalog candidate"

    # reprobe ledger: auto-probe watchlist items are first-class candidates, and
    # freshness/skip/dead conflicts fail before an operator can trust stale docs.
    ledger = {
        "watchlist": [
            {
                "platform": "openai",
                "model": "gpt-5.2",
                "reason": "capacity-sensitive backend set; keep reprobing",
                "last_probe": "2026-06-05",
                "freshness_days": 30,
                "auto_probe": True,
                "probe_family": "openai_chat",
            },
            {
                "platform": "gemini",
                "model": "gemini-3-pro-image-preview",
                "reason": "wrong-surface prior probe; reprobe via chat",
                "last_probe": "2026-06-09",
                "freshness_days": 30,
                "auto_probe": True,
                "probe_family": "gemini_chat",
            },
            {
                "platform": "antigravity",
                "model": "gemini-2.5-pro",
                "reason": "hand-maintained platform; separate probe runner",
                "expires": "2026-07-13",
                "auto_probe": False,
            },
        ],
        "skiplist": [
            {"platform": "openai", "model": "codex-mini-latest", "reason": "stable ChatGPT-account rejection"},
            {"platform": "openai", "model": "gpt-image-dead", "reason": "fixture skiplist exclusion"},
        ],
        "deadlist": [
            {"platform": "grok", "model": "grok-imagine-image-pro", "reason": "retired upstream"}
        ],
    }
    aug = augment_candidates_with_watchlist(build_candidates(cat, ["gemini-3-pro-image-preview"]), ledger)
    assert "gpt-5.2" in aug["openai_chat"], aug["openai_chat"]
    # watchlist probe_family override moves it from the base family (now
    # gemini_chat_image for *-image ids) to the overridden gemini_chat, and out of
    # all peers — proves the override wins over the systematic family routing.
    assert "gemini-3-pro-image-preview" in aug["gemini_chat"], aug["gemini_chat"]
    assert "gemini-3-pro-image-preview" not in aug["gemini_image"], aug["gemini_image"]
    assert "gemini-3-pro-image-preview" not in aug["gemini_chat_image"], aug["gemini_chat_image"]
    assert "gpt-image-dead" not in aug["openai_image"], aug["openai_image"]
    validate_reprobe_ledger(
        ledger,
        today=dt.date(2026, 6, 22),
        allowlist_members={("openai", "gpt-5.4"), ("antigravity", "gemini-2.5-pro")},
        candidates=aug,
    )
    stale = json.loads(json.dumps(ledger))
    stale["watchlist"][0]["last_probe"] = "2026-05-01"
    try:
        validate_reprobe_ledger(stale, today=dt.date(2026, 6, 22), candidates=aug)
        raise AssertionError("stale watchlist must fail")
    except ValueError as e:
        assert "stale" in str(e), e
    duplicate = json.loads(json.dumps(ledger))
    duplicate["skiplist"].append({"platform": "openai", "model": "gpt-5.2", "reason": "bad duplicate"})
    try:
        validate_reprobe_ledger(duplicate, today=dt.date(2026, 6, 22), candidates=aug)
        raise AssertionError("watchlist/skiplist duplicate must fail")
    except ValueError as e:
        assert "appears in both" in str(e), e
    conflict = json.loads(json.dumps(ledger))
    conflict["deadlist"].append({"platform": "openai", "model": "gpt-5.4", "reason": "bad allowlist conflict"})
    try:
        validate_reprobe_ledger(
            conflict,
            today=dt.date(2026, 6, 22),
            allowlist_members={("openai", "gpt-5.4")},
            candidates=aug,
        )
        raise AssertionError("allowlist/deadlist conflict must fail")
    except ValueError as e:
        assert "appears in both" in str(e) or "allowlist cannot overlap" in str(e), e
    try:
        validate_results_against_reprobe_ledger({"openai": {"codex-mini-latest"}}, ledger)
        raise AssertionError("servable skiplist result must fail")
    except SystemExit as e:
        assert "skiplist/deadlist" in str(e), e

    # gemini family split: core families kept, exotic dropped, predict seed merged
    g = split_gemini_families([
        "gemini-2.5-pro", "gemini-3-pro-preview", "gemini-2.5-flash-image",
        "gemini-3-pro-image", "nano-banana-pro-preview",
        "gemma-4-31b-it", "lyria-3-pro-preview", "deep-research-pro-preview-12-2025",
        "gemini-2.5-pro-preview-tts", "gemini-robotics-er-1.6-preview",
        "gemini-2.5-computer-use-preview-10-2025",
    ])
    assert g["gemini_chat"] == ["gemini-2.5-pro", "gemini-3-pro-preview"], g["gemini_chat"]
    # generateContent image models go to gemini_chat_image (chat surface), NOT the
    # imagen predict family; imagen-* stays in gemini_image.
    for img in ("gemini-2.5-flash-image", "gemini-3-pro-image", "nano-banana-pro-preview"):
        assert img in g["gemini_chat_image"], (img, g["gemini_chat_image"])
        assert img not in g["gemini_image"], (img, g["gemini_image"])
    # predict seed always merged into video/image even with empty discovery
    assert "veo-3.1-generate-001" in g["gemini_video"], g["gemini_video"]
    assert "imagen-4.0-generate-001" in g["gemini_image"], g["gemini_image"]
    assert g["gemini_image"] == sorted(g["gemini_image"]), "families must be sorted"
    for exotic in ("gemma-4-31b-it", "lyria-3-pro-preview", "deep-research-pro-preview-12-2025",
                   "gemini-2.5-pro-preview-tts", "gemini-robotics-er-1.6-preview",
                   "gemini-2.5-computer-use-preview-10-2025"):
        assert exotic not in sum(g.values(), []), f"exotic {exotic} must be excluded"
    # load_discovered accepts a model_pricing_status-shaped JSON object (keys)
    import os
    import tempfile
    tf = tempfile.NamedTemporaryFile("w", suffix=".json", delete=False)
    try:
        tf.write('{"gemini-2.5-pro": "priced", "gemini-3-pro-image": "missing"}')
        tf.close()
        disc = load_discovered(tf.name)
    finally:
        os.unlink(tf.name)
    assert set(disc) == {"gemini-2.5-pro", "gemini-3-pro-image"}, disc

    # de-dup: drop dated-with-base + -thinking; keep dated whose base is absent
    servable = {
        "claude-opus-4-5", "claude-opus-4-5-20251101", "claude-opus-4-6",
        "claude-opus-4-6-thinking", "claude-haiku-4-5-20251001", "gpt-5.5",
        "gpt-5.5-2026-04-23",
    }
    got = dedup(servable)
    assert got == ["claude-haiku-4-5-20251001", "claude-opus-4-5", "claude-opus-4-6", "gpt-5.5"], got

    # batching: keeps order, never exceeds size, handles empty + exact multiples
    assert chunk([], 12) == []
    assert chunk(["a", "b", "c"], 2) == [["a", "b"], ["c"]]
    assert chunk(["a", "b", "c", "d"], 2) == [["a", "b"], ["c", "d"]]
    big = [f"m{i}" for i in range(30)]
    cks = chunk(big, BATCH_SIZE)
    assert all(len(c) <= BATCH_SIZE for c in cks) and sum(cks, []) == big

    # parse: gemini/grok rows land in their buckets; non-servable/auth dropped.
    # Grok remains hand-maintained in pricing_catalog_supported_models_tk.go, so
    # write_allowlists still rewrites only anthropic/openai/gemini.
    tsv = (
        "anthropic\tclaude-opus-4-8\t200\tservable\n"
        "openai\tgpt-4o\t400\tunsupported\nopenai\t*\t000\tauth_error\n"
        "gemini\tgemini-2.5-pro\t200\tservable\ngemini\tgemma-4-31b-it\t400\tunsupported\n"
        # prod-mirror relay-health rows carry distinct tags and MUST NOT enter the allowlist
        # (the exact-dict assertion below fails if parse_results ever stops ignoring them).
        "anthropic_prodmirror_cc\tclaude-sonnet-4-6\t200\tservable\n"
        "anthropic_prodmirror_kiro\tclaude-sonnet-4-6\t429\tnot_allowlisted\n"
        "grok\tgrok-4.3\t200\tservable\ngrok\tgrok-4-0709\t429\tinconclusive"
    )
    p = parse_results(tsv)
    assert p == {
        "anthropic": {"claude-opus-4-8"},
        "openai": set(),
        "gemini": {"gemini-2.5-pro"},
        "grok": {"grok-4.3"},
    }, p

    # splice round-trips between markers and is idempotent (anthropic + gemini)
    sample = (
        "x{\n\t// servable-allowlist:begin anthropic\n\t\"old\": {},\n"
        "\t// servable-allowlist:end anthropic\n"
        "\t// servable-allowlist:begin gemini\n\t// servable-allowlist:end gemini\n}\n"
    )
    out = splice_go(sample, "anthropic", ["claude-opus-4-8", "claude-sonnet-4-6"])
    assert '"claude-opus-4-8": {},' in out and '"old"' not in out, out
    assert splice_go(out, "anthropic", ["claude-opus-4-8", "claude-sonnet-4-6"]) == out, "not idempotent"
    gout = splice_go(out, "gemini", ["gemini-2.5-pro", "imagen-4.0-generate-001"])
    assert '"gemini-2.5-pro": {},' in gout, gout
    assert splice_go(gout, "gemini", ["gemini-2.5-pro", "imagen-4.0-generate-001"]) == gout, "gemini not idempotent"

    # --skip-video carry-forward: a chat/image refresh must preserve existing video
    # (veo) ids verbatim. carried_forward_rows emits ONLY the video-family members
    # of the current allowlist as servable rows; chat/image ids are left to the probe.
    skipv_sample = (
        "x{\n\t// servable-allowlist:begin gemini\n"
        '\t"gemini-2.5-pro": {},\n\t"imagen-4.0-generate-001": {},\n\t"veo-3.1-generate-001": {},\n'
        "\t// servable-allowlist:end gemini\n}\n"
    )
    cf = carried_forward_rows(skipv_sample, VIDEO_FAMILIES)
    assert cf == "gemini\tveo-3.1-generate-001\t000\tservable", cf
    assert parse_results(cf)["gemini"] == {"veo-3.1-generate-001"}, parse_results(cf)
    assert "gemini_video" in VIDEO_FAMILIES and not any(f.endswith("_chat") for f in VIDEO_FAMILIES), VIDEO_FAMILIES

    # The real machine ledger is part of preflight, not just a runtime input.
    # Include discovered fixtures so skiplist removal and watchlist
    # probe_family overrides are both exercised without prod.
    real_catalog = json.loads(CATALOG.read_text(encoding="utf-8"))
    real_cands, _real_ledger = build_probe_candidates(
        real_catalog,
        ["gemini-3-pro-preview", "gemini-3-pro-image-preview", "gemini-3.1-flash-image"],
    )
    real_members = _candidate_members(real_cands)
    assert ("openai", "gpt-5.2") in real_members, "watchlist gpt-5.2 must be probed"
    assert ("openai", "codex-auto-review") in real_members, "watchlist codex-auto-review must be probed"
    assert ("gemini", "gemini-3-pro-preview") not in real_members, "skiplist gemini chat must be excluded"
    # gemini-*-image route to the gemini_chat_image family (chat/generateContent
    # surface), not gemini_chat (text) or gemini_image (imagen predict API).
    assert "gemini-3-pro-image-preview" in real_cands["gemini_chat_image"], real_cands["gemini_chat_image"]
    assert "gemini-3-pro-image-preview" not in real_cands["gemini_image"], real_cands["gemini_image"]
    assert "gemini-3-pro-image-preview" not in real_cands["gemini_chat"], real_cands["gemini_chat"]

    probe_lib = REPO / "ops/pricing/probe_reserved_resources.sh"
    probe_test = REPO / "ops/pricing/test_probe_reserved_resources.sh"
    probe_sh = PROBE
    for path in (probe_lib, probe_test, probe_sh):
        if not path.is_file():
            raise AssertionError(f"missing probe script: {path}")
    subprocess.run(["bash", "-n", str(probe_sh)], check=True)
    subprocess.run(["bash", str(probe_test)], check=True)

    # 24h-traffic short-circuit: proven-by-traffic candidates skip the probe batch
    # but still land in the servable set, while non-candidates / blocked / untrafficked
    # models are handled correctly (no injection, no revival, no false negative).
    traffic_cands = augment_candidates_with_watchlist(build_candidates(cat, ["gemini-2.5-pro"]), ledger)
    assert "gpt-image-dead" not in traffic_cands["openai_image"], "skiplist must pre-exclude"
    assert candidate_model_platforms(traffic_cands)["gemini-2.5-pro"] == {"gemini"}
    mock_tsv = (
        "anthropic\tclaude-opus-4-8\t42\n"      # candidate -> proven (anthropic)
        "openai\tgpt-5.4\t10\n"                 # candidate -> proven (openai)
        "newapi\tgemini-2.5-pro\t7\n"           # served as newapi, but candidate=gemini -> bucket by CANDIDATE platform
        "openai\tgpt-image-dead\t3\n"           # skiplisted -> not a candidate -> dropped, no revival
        "openai\tgpt-4o\t99\n"                  # unpriced -> not a candidate -> dropped, no injection
        "\n bad line with no tabs \n"            # malformed -> ignored
    )
    traffic = parse_traffic_rows(mock_tsv)
    assert traffic["newapi"] == {"gemini-2.5-pro"}, traffic
    proven = proven_servable_from_traffic(traffic, traffic_cands)
    assert proven == {
        "anthropic": {"claude-opus-4-8"},
        "openai": {"gpt-5.4"},
        "gemini": {"gemini-2.5-pro"},  # bucketed by candidate platform, NOT the serving 'newapi'
    }, proven
    flat_proven = {m for s in proven.values() for m in s}
    assert "gpt-image-dead" not in flat_proven, "blocked model must never revive via traffic"
    assert "gpt-4o" not in flat_proven, "non-candidate must never be injected via traffic"
    # proven ⊆ candidates (which exclude skiplist/deadlist) -> ledger validation passes.
    validate_results_against_reprobe_ledger(proven, ledger)
    # reduced candidates: proven removed (not re-probed), untrafficked candidates kept (still probed).
    reduced = remove_proven_from_candidates(traffic_cands, proven)
    assert "claude-opus-4-8" not in reduced["anthropic"], reduced["anthropic"]
    assert "claude-3-haiku-20240307" in reduced["anthropic"], "untrafficked candidate must still be probed"
    assert "gpt-5.4" not in reduced["openai_chat"], reduced["openai_chat"]
    assert "gpt-5.2" in reduced["openai_chat"], "untrafficked watchlist candidate must still be probed"
    assert "gemini-2.5-pro" not in reduced["gemini_chat"], reduced["gemini_chat"]
    assert "gemini-3-pro-image-preview" in reduced["gemini_chat"], "untrafficked candidate must still be probed"
    orig_count = sum(len(v) for v in traffic_cands.values())
    reduced_count = sum(len(v) for v in reduced.values())
    assert reduced_count == orig_count - 3, (orig_count, reduced_count)
    # empty traffic -> no short-circuit, candidates untouched (pure additive, never subtractive)
    assert proven_servable_from_traffic({}, traffic_cands) == {}
    assert remove_proven_from_candidates(traffic_cands, {}) == traffic_cands
    # a blocked model that somehow reached the proven set IS still caught by the ledger guard
    try:
        validate_results_against_reprobe_ledger({"openai": {"gpt-image-dead"}}, ledger)
        raise AssertionError("blocked proven model must fail ledger validation")
    except SystemExit as e:
        assert "skiplist/deadlist" in str(e), e
    # proven_as_tsv round-trips into servable probe rows the apply path understands
    tsv = proven_as_tsv({"anthropic": {"claude-opus-4-8"}, "openai": {"gpt-5.4"}})
    assert parse_results(tsv) == {
        "anthropic": {"claude-opus-4-8"}, "openai": {"gpt-5.4"}, "gemini": set(), "grok": set()
    }, tsv

    print("refresh-servable-allowlist selftest: PASS")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    DISC_HELP = (
        "gemini discovered-models source: JSON object (account.model_pricing_status "
        "— keys), JSON list, or newline list. Omit to probe only the imagen/veo seed."
    )
    SKIP_HELP = (
        "short-circuit: skip the SSM probe for any candidate already proven servable "
        "by the last --traffic-hours of successful prod traffic (additive; unmatched "
        "candidates are still probed). Default off (env REFRESH_SKIP_PROVEN_BY_TRAFFIC=1 "
        "also enables it) so the conservative full probe stays the default."
    )
    HOURS_HELP = f"traffic short-circuit look-back window in hours (default {DEFAULT_TRAFFIC_HOURS})"
    SKIP_VIDEO_HELP = (
        "do NOT live-probe video families (gemini_video) — a video submit creates a "
        "REAL paid generation task. Current video allowlist entries are carried "
        "forward un-probed so they are not dropped. Env REFRESH_SKIP_VIDEO=1 also enables."
    )

    def add_traffic_flags(p: argparse.ArgumentParser) -> None:
        p.add_argument("--skip-proven-by-traffic", action="store_true", help=SKIP_HELP)
        p.add_argument("--traffic-hours", type=int, default=DEFAULT_TRAFFIC_HOURS, help=HOURS_HELP)
        p.add_argument("--skip-video", action="store_true", help=SKIP_VIDEO_HELP)

    ap_cand = sub.add_parser("candidates")
    ap_cand.add_argument("--discovered", help=DISC_HELP)
    ap_probe = sub.add_parser("probe")
    ap_probe.add_argument("--discovered", help=DISC_HELP)
    add_traffic_flags(ap_probe)
    ap_apply = sub.add_parser("apply")
    ap_apply.add_argument("--results", required=True, help="TSV results file (- for stdin)")
    ap_run = sub.add_parser("run")
    ap_run.add_argument("--open-pr", action="store_true")
    ap_run.add_argument("--discovered", help=DISC_HELP)
    add_traffic_flags(ap_run)
    sub.add_parser("selftest")
    args = ap.parse_args()

    def skip_proven_enabled() -> bool:
        return bool(getattr(args, "skip_proven_by_traffic", False)) or \
            os.environ.get("REFRESH_SKIP_PROVEN_BY_TRAFFIC", "") not in ("", "0", "false", "False")

    def video_skip_enabled() -> bool:
        return bool(getattr(args, "skip_video", False)) or \
            os.environ.get("REFRESH_SKIP_VIDEO", "") not in ("", "0", "false", "False")

    def _report(final: dict[str, list[str]]) -> None:
        print(
            f"[refresh] anthropic={final['anthropic']}\n[refresh] openai={final['openai']}\n"
            f"[refresh] gemini={final.get('gemini', [])}",
            file=sys.stderr,
        )

    if args.cmd == "selftest":
        return selftest()

    if args.cmd == "candidates":
        cands, _ledger = build_probe_candidates(json.loads(CATALOG.read_text(encoding="utf-8")), load_discovered(args.discovered))
        print(json.dumps(cands, indent=2, ensure_ascii=False))
        return 0

    if args.cmd == "probe":
        cands, ledger = build_probe_candidates(json.loads(CATALOG.read_text(encoding="utf-8")), load_discovered(args.discovered))
        proven: dict[str, set[str]] = {}
        if skip_proven_enabled():
            cands, proven = short_circuit_by_traffic(cands, ledger, hours=args.traffic_hours)
        probe_tsv = live_probe(cands, skip_video=video_skip_enabled())
        if proven:
            tsv = proven_as_tsv(proven)
            probe_tsv = f"{tsv}\n{probe_tsv}".strip() if probe_tsv else tsv
        print(probe_tsv)
        return 0

    if args.cmd == "apply":
        ledger = load_reprobe_ledger()
        validate_reprobe_ledger(ledger, allowlist_members=_known_allowlist_members(GO_FILE.read_text(encoding="utf-8")))
        text = sys.stdin.read() if args.results == "-" else Path(args.results).read_text(encoding="utf-8")
        _report(write_allowlists(parse_results(text), ledger))
        return 0

    if args.cmd == "run":
        cands, ledger = build_probe_candidates(json.loads(CATALOG.read_text(encoding="utf-8")), load_discovered(args.discovered))
        proven: dict[str, set[str]] = {}
        if skip_proven_enabled():
            cands, proven = short_circuit_by_traffic(cands, ledger, hours=args.traffic_hours)
        servable = parse_results(live_probe(cands, skip_video=video_skip_enabled()))
        for platform, models in proven.items():
            servable.setdefault(platform, set()).update(models)
        final = write_allowlists(servable, ledger)
        _report(final)
        if args.open_pr:
            open_pr(final)
        return 0

    return 2


if __name__ == "__main__":
    sys.exit(main())
