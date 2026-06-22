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
import json
import re
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
CATALOG = REPO / "backend/resources/model-pricing/model_prices_and_context_window.json"
GO_FILE = REPO / "backend/internal/service/pricing_catalog_supported_models_tk.go"
PROBE = REPO / "ops/pricing/probe-servable-models.sh"
RUN_PROBE = REPO / "ops/observability/run-probe.sh"

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
# The google group lives on edge us6 (not prod), so gemini batches target it.
GEMINI_TARGET = "edge:us6"
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
    fams: dict[str, list[str]] = {"gemini_chat": [], "gemini_image": [], "gemini_video": []}
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
        elif mid.startswith("imagen-") or "image" in mid or "nano-banana" in mid:
            fams["gemini_image"].append(mid)
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


def write_allowlists(servable: dict[str, set[str]]) -> dict[str, list[str]]:
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
ENV_BY_FAMILY = (
    ("ANTHROPIC_MODELS", "anthropic", "prod"),
    ("OPENAI_CHAT_MODELS", "openai_chat", "prod"),
    ("OPENAI_RESPONSES_MODELS", "openai_responses", "prod"),
    ("OPENAI_IMAGE_MODELS", "openai_image", "prod"),
    ("GEMINI_CHAT_MODELS", "gemini_chat", GEMINI_TARGET),
    ("GEMINI_IMAGE_MODELS", "gemini_image", GEMINI_TARGET),
    ("GEMINI_VIDEO_MODELS", "gemini_video", GEMINI_TARGET),
)


def _run_probe_batch(env_args: list[str], target: str = "prod") -> str:
    cmd = [
        "bash", str(RUN_PROBE), "--target", target, "--script", str(PROBE),
        "--timeout-seconds", "600", *env_args,
    ]
    proc = subprocess.run(cmd, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    sys.stderr.write(proc.stderr)
    if proc.returncode != 0:
        raise SystemExit(f"FATAL: probe batch failed (exit {proc.returncode}) — see stderr above")
    # run-probe prefixes wrapper lines; keep only TSV rows (4 tab fields).
    return "\n".join(ln for ln in proc.stdout.splitlines() if ln.count("\t") == 3)


def live_probe(candidates: dict[str, list[str]]) -> str:
    if not RUN_PROBE.exists() or not PROBE.exists():
        raise SystemExit("FATAL: run-probe.sh or probe-servable-models.sh missing")
    # One run-probe invocation per family-batch: a single command spanning the
    # whole catalog would exceed the SSM waiter window and be reported failed.
    batches: list[tuple[str, list[str], str]] = []
    for env_key, fam, target in ENV_BY_FAMILY:
        for c in chunk(candidates.get(fam, []), BATCH_SIZE):
            batches.append((env_key, c, target))
    total = sum(len(candidates.get(f, [])) for _, f, _ in ENV_BY_FAMILY)
    print(f"[refresh] probing {total} models in {len(batches)} batch(es) of <= {BATCH_SIZE} …", file=sys.stderr)
    rows: list[str] = []
    for i, (env_key, models, target) in enumerate(batches, 1):
        print(f"[refresh] batch {i}/{len(batches)} ({env_key}@{target}: {len(models)}) …", file=sys.stderr)
        out = _run_probe_batch(["--env", f"{env_key}={' '.join(models)}"], target=target)
        if out:
            rows.append(out)
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
        "gpt-image-2": {"input_cost_per_token": 1, "litellm_provider": "openai"},
        "gpt-4o": {"litellm_provider": "openai"},  # unpriced -> skipped
        "gemini-2.5-pro": {"input_cost_per_token": 1, "litellm_provider": "vertex_ai"},
    }
    c = derive_candidates(cat)
    assert c["anthropic"] == ["claude-3-haiku-20240307", "claude-opus-4-8"], c["anthropic"]
    assert c["openai_chat"] == ["gpt-5.4"], c["openai_chat"]
    assert c["openai_responses"] == ["gpt-5.3-codex"], c["openai_responses"]
    assert c["openai_image"] == ["gpt-image-2"], c["openai_image"]
    assert "gpt-4o" not in sum(c.values(), []), "unpriced must be skipped"
    assert "gemini-2.5-pro" not in sum(c.values(), []), "vertex not a litellm-catalog candidate"

    # gemini family split: core families kept, exotic dropped, predict seed merged
    g = split_gemini_families([
        "gemini-2.5-pro", "gemini-3-pro-preview", "gemini-2.5-flash-image",
        "gemini-3-pro-image", "nano-banana-pro-preview",
        "gemma-4-31b-it", "lyria-3-pro-preview", "deep-research-pro-preview-12-2025",
        "gemini-2.5-pro-preview-tts", "gemini-robotics-er-1.6-preview",
        "gemini-2.5-computer-use-preview-10-2025",
    ])
    assert g["gemini_chat"] == ["gemini-2.5-pro", "gemini-3-pro-preview"], g["gemini_chat"]
    # discovered image models classified into the image family (alongside imagen seed)
    for img in ("gemini-2.5-flash-image", "gemini-3-pro-image", "nano-banana-pro-preview"):
        assert img in g["gemini_image"], (img, g["gemini_image"])
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

    print("refresh-servable-allowlist selftest: PASS")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    DISC_HELP = (
        "gemini discovered-models source: JSON object (account.model_pricing_status "
        "— keys), JSON list, or newline list. Omit to probe only the imagen/veo seed."
    )
    ap_cand = sub.add_parser("candidates")
    ap_cand.add_argument("--discovered", help=DISC_HELP)
    ap_probe = sub.add_parser("probe")
    ap_probe.add_argument("--discovered", help=DISC_HELP)
    ap_apply = sub.add_parser("apply")
    ap_apply.add_argument("--results", required=True, help="TSV results file (- for stdin)")
    ap_run = sub.add_parser("run")
    ap_run.add_argument("--open-pr", action="store_true")
    ap_run.add_argument("--discovered", help=DISC_HELP)
    sub.add_parser("selftest")
    args = ap.parse_args()

    def _report(final: dict[str, list[str]]) -> None:
        print(
            f"[refresh] anthropic={final['anthropic']}\n[refresh] openai={final['openai']}\n"
            f"[refresh] gemini={final.get('gemini', [])}",
            file=sys.stderr,
        )

    if args.cmd == "selftest":
        return selftest()

    if args.cmd == "candidates":
        cands = build_candidates(json.loads(CATALOG.read_text(encoding="utf-8")), load_discovered(args.discovered))
        print(json.dumps(cands, indent=2, ensure_ascii=False))
        return 0

    if args.cmd == "probe":
        cands = build_candidates(json.loads(CATALOG.read_text(encoding="utf-8")), load_discovered(args.discovered))
        print(live_probe(cands))
        return 0

    if args.cmd == "apply":
        text = sys.stdin.read() if args.results == "-" else Path(args.results).read_text(encoding="utf-8")
        _report(write_allowlists(parse_results(text)))
        return 0

    if args.cmd == "run":
        cands = build_candidates(json.loads(CATALOG.read_text(encoding="utf-8")), load_discovered(args.discovered))
        final = write_allowlists(parse_results(live_probe(cands)))
        _report(final)
        if args.open_pr:
            open_pr(final)
        return 0

    return 2


if __name__ == "__main__":
    sys.exit(main())
