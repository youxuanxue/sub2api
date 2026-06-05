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
    out: dict[str, set[str]] = {"anthropic": set(), "openai": set()}
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
    final = {p: dedup(servable.get(p, set())) for p in ("anthropic", "openai")}
    for plat in ("anthropic", "openai"):
        text = splice_go(text, plat, final[plat])
    GO_FILE.write_text(text, encoding="utf-8")
    subprocess.run(["gofmt", "-w", str(GO_FILE)], check=True)
    return final


# ----- batching -----
def chunk(items: list[str], size: int) -> list[list[str]]:
    return [items[i : i + size] for i in range(0, len(items), size)] if items else []


# ----- live probe -----
ENV_BY_FAMILY = (
    ("ANTHROPIC_MODELS", "anthropic"),
    ("OPENAI_CHAT_MODELS", "openai_chat"),
    ("OPENAI_RESPONSES_MODELS", "openai_responses"),
    ("OPENAI_IMAGE_MODELS", "openai_image"),
)


def _run_probe_batch(env_args: list[str]) -> str:
    cmd = [
        "bash", str(RUN_PROBE), "--target", "prod", "--script", str(PROBE),
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
    batches: list[list[str]] = []
    for env_key, fam in ENV_BY_FAMILY:
        for c in chunk(candidates[fam], BATCH_SIZE):
            batches.append([env_key, c])  # type: ignore[list-item]
    total = sum(len(candidates[f]) for _, f in ENV_BY_FAMILY)
    print(f"[refresh] probing {total} models in {len(batches)} batch(es) of <= {BATCH_SIZE} …", file=sys.stderr)
    rows: list[str] = []
    for i, (env_key, models) in enumerate(batches, 1):
        print(f"[refresh] batch {i}/{len(batches)} ({env_key}: {len(models)}) …", file=sys.stderr)
        out = _run_probe_batch(["--env", f"{env_key}={' '.join(models)}"])
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
        f"openai ({len(final['openai'])}): {', '.join(final['openai'])}\n\n"
        "Regenerated by `ops/pricing/refresh-servable-allowlist.py run --open-pr`\n"
        "(live prod probe; kept verdict==servable, de-duplicated dated snapshots).\n\n"
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
    assert "gemini-2.5-pro" not in sum(c.values(), []), "non-curated vendor not a candidate"

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

    # parse
    tsv = "anthropic\tclaude-opus-4-8\t200\tservable\nopenai\tgpt-4o\t400\tunsupported\nopenai\t*\t000\tauth_error"
    p = parse_results(tsv)
    assert p == {"anthropic": {"claude-opus-4-8"}, "openai": set()}, p

    # splice round-trips between markers and is idempotent
    sample = (
        "x{\n\t// servable-allowlist:begin anthropic\n\t\"old\": {},\n"
        "\t// servable-allowlist:end anthropic\n}\n"
    )
    out = splice_go(sample, "anthropic", ["claude-opus-4-8", "claude-sonnet-4-6"])
    assert '"claude-opus-4-8": {},' in out and '"old"' not in out, out
    assert splice_go(out, "anthropic", ["claude-opus-4-8", "claude-sonnet-4-6"]) == out, "not idempotent"

    print("refresh-servable-allowlist selftest: PASS")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("candidates")
    sub.add_parser("probe")
    ap_apply = sub.add_parser("apply")
    ap_apply.add_argument("--results", required=True, help="TSV results file (- for stdin)")
    ap_run = sub.add_parser("run")
    ap_run.add_argument("--open-pr", action="store_true")
    sub.add_parser("selftest")
    args = ap.parse_args()

    if args.cmd == "selftest":
        return selftest()

    if args.cmd == "candidates":
        cands = derive_candidates(json.loads(CATALOG.read_text(encoding="utf-8")))
        print(json.dumps(cands, indent=2, ensure_ascii=False))
        return 0

    if args.cmd == "probe":
        cands = derive_candidates(json.loads(CATALOG.read_text(encoding="utf-8")))
        print(live_probe(cands))
        return 0

    if args.cmd == "apply":
        text = sys.stdin.read() if args.results == "-" else Path(args.results).read_text(encoding="utf-8")
        final = write_allowlists(parse_results(text))
        print(f"[refresh] anthropic={final['anthropic']}\n[refresh] openai={final['openai']}", file=sys.stderr)
        return 0

    if args.cmd == "run":
        cands = derive_candidates(json.loads(CATALOG.read_text(encoding="utf-8")))
        final = write_allowlists(parse_results(live_probe(cands)))
        print(f"[refresh] anthropic={final['anthropic']}\n[refresh] openai={final['openai']}", file=sys.stderr)
        if args.open_pr:
            open_pr(final)
        return 0

    return 2


if __name__ == "__main__":
    sys.exit(main())
