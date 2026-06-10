#!/usr/bin/env bash
#
# preflight.sh — sub2api project wrapper.
#
# Per CLAUDE.md § 10, the dev-rules submodule template
# (`dev-rules/templates/preflight.sh`) covers all the generic checks
# (branch naming, submodule pointer, .cursor/rules drift, agent contract
# drift, story/test alignment, docs/approved discipline, approved-doc
# invariants R1-R5, doc-stat drift, cloud-agent env consistency). This
# wrapper exists ONLY because sub2api has project-specific checks that
# do not belong in the shared template:
#
#   newapi compat-pool drift     — guards the P0 regression that
#        triggered docs/approved/newapi-as-fifth-platform.md (any new
#        scheduler/gateway caller must use IsOpenAICompatPoolMember /
#        OpenAICompatPlatforms instead of bare PlatformOpenAI / IsOpenAI).
#   newapi sentinel registry     — guards the recurring upstream-merge
#        regression where load-bearing fifth-platform files / symbols get
#        silently deleted. Driven by `scripts/sentinels/newapi.json`
#        (single source of truth) via `scripts/sentinels/check-newapi.py`.
#        The same script is invoked by
#        `.github/workflows/upstream-merge-pr-shape.yml`.
#   brand sentinel registry      — guards outward TokenKey brand surfaces
#        (browser title, deploy/operator surfaces, image metadata,
#        fifth-platform display label) from drifting back apart. Driven by
#        `scripts/sentinels/brand.json` via `scripts/sentinels/check-brand.py`;
#        intentionally separate from `newapi` semantics / routing truth.
#   frontend TK sentinel registry — guards load-bearing TokenKey-only frontend
#        surfaces (sidebar geometry, fluid admin-accounts table mode, sticky
#        edge-hints opt-out) from being silently reverted by upstream merges
#        on common Vue components. Driven by `scripts/sentinels/frontend-tk.json`
#        via `scripts/sentinels/check-frontend-tk.py`. The same script is
#        invoked by `.github/workflows/upstream-merge-pr-shape.yml`.
#   gateway TK sentinel registry — guards small TokenKey-only gateway/service
#        hooks in upstream-shaped hotspot files from being silently reverted by
#        upstream merges. Driven by `scripts/sentinels/gateway-tk.json` via
#        `scripts/sentinels/check-gateway-tk.py`. The same script is invoked by
#        `.github/workflows/upstream-merge-pr-shape.yml`.
#   redaction version contract   — guards Evidence Spine contract drift:
#        changing the default sensitive-key set in logredact must bump the
#        outward QA `redaction_version` contract in the same commit. Driven by
#        `scripts/sentinels/redaction.json` via `scripts/sentinels/check-redaction-version.py`.
#   trajectory hook registry     — guards the request-evidence hook contract:
#        main gateway scopes must keep `trajectory_id` + `qaCapture` wiring, and
#        the QA middleware must still terminate in `CaptureFromContext`. Driven by
#        `scripts/sentinels/trajectory.json` via `scripts/sentinels/check-trajectory-hooks.py`.
#   terminal event registry      — guards stream terminal semantics: OpenAI /
#        Anthropic terminal helpers, `[DONE]` emission, and focused terminal-path
#        assertions must remain intact so evidence capture keeps stable completion
#        signals. Driven by `scripts/sentinels/terminal.json` via
#        `scripts/sentinels/check-terminal-events.py`.
#   engine facade registry      — guards Engine Spine dispatch semantics: key
#        gateway dispatch paths must keep routing bridge eligibility through
#        shared engine facade helpers instead of drifting back into hotspot
#        service files. Driven by `scripts/sentinels/engine-facade.json` via
#        `scripts/sentinels/check-engine-facade.py`.
#   OpenAI upstream capability truth — guards Responses probe status semantics:
#        probe call sites must use `internal/pkg/openai_compat` as the owner
#        instead of reintroducing local status-code truth in service files.
#   buffered Content-Type leak gate — generalized forward-drift guard for
#        upstream Wei-Shaw/sub2api#1311: any new site in backend/internal/service
#        that calls `responseheaders.WriteFilteredHeaders` and then `c.JSON` or
#        `c.Data(_, "application/json...", _)` without an explicit
#        `c.Writer.Header().Set("Content-Type", ...)` override leaks the upstream
#        SSE Content-Type onto a JSON body. Driven by
#        `scripts/checks/buffered-content-type-leak.py`. Mechanical replacement
#        for "review notices the antipattern" + sentinel pinning of known sites.
#   QA evidence dataset validator — guards the exported QA evidence dataset contract:
#        exported `trajectory.jsonl` artifacts must keep H1/H2/H3/D1 and structural
#        acceptance semantics reachable through the standalone validator script,
#        so projection/export drift is caught mechanically instead of by eyeballing.
##
# Usage:  ./scripts/preflight.sh [--fix]
# Exit 0 = all sections passed.  Non-zero = at least one failed.
#
set -u

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# ---- TK: clear worktree git-config debt before any submodule traversal -------
# When this script runs inside a git worktree that contains nested submodules
# (e.g. `dev-rules/`), `git submodule status` and similar walkers will silently
# set `core.bare=true` on the worktree-local git config. The next `git status`
# or `git commit` in the same shell then fails with "fatal: this operation
# must be run in a work tree". A regular preflight cycle calls submodule-aware
# checks several times, so the only safe place to clear this debt is both
# up-front (to defend against debt left by an earlier process) AND on EXIT
# (to make sure the next `git commit` step after preflight returns is clean).
# This is mechanical R-004 fix; previously the workaround lived only in memory
# ("when in doubt, unset core.bare and retry") which is OPC anti-pattern.
git config --local --unset core.bare >/dev/null 2>&1 || true
_preflight_bg_dir="$(mktemp -d "${TMPDIR:-/tmp}/preflight-bg.XXXXXX")"
# On exit: clear the worktree core.bare debt, kill any still-running background
# gate (e.g. when the dev-rules template fails and we exit before the joins),
# then drop the scratch dir.
trap 'git config --local --unset core.bare >/dev/null 2>&1 || true; { cat "$_preflight_bg_dir"/*.pid 2>/dev/null | xargs kill; wait; } 2>/dev/null; rm -rf "$_preflight_bg_dir"' EXIT

# ---- TK: background-job helpers ----------------------------------------------
# The most expensive sub2api gates (QA go test, the unittest-discover suites,
# the dockerized Caddyfile adapt) are independent of every other section, so
# they are spawned in the background right after the dev-rules template returns
# and joined at their original section position. Wall-clock for the expensive
# block becomes max(job) instead of sum(job) while output/FAIL semantics stay
# byte-equivalent per section.
_bg_spawn() {  # _bg_spawn <key> <cmd...>
    local key="$1"; shift
    ( "$@" >"$_preflight_bg_dir/$key.out" 2>&1; echo "$?" >"$_preflight_bg_dir/$key.rc" ) &
    echo "$!" >"$_preflight_bg_dir/$key.pid"
}
_bg_join() {  # _bg_join <key> → sets _bg_rc to the captured exit code; output in $_preflight_bg_dir/<key>.out
    # Must run in the MAIN shell (never inside $(...)): `wait` can only reap
    # children of the shell that spawned them.
    local key="$1"
    wait "$(cat "$_preflight_bg_dir/$key.pid")" 2>/dev/null
    _bg_rc="$(cat "$_preflight_bg_dir/$key.rc")"
    return 0
}
_bg_spawned() { [ -f "$_preflight_bg_dir/$1.pid" ]; }

# ---- Sections 1-8: delegate to dev-rules template ----------------------------
if [ ! -x ./dev-rules/templates/preflight.sh ]; then
    echo "FAIL: dev-rules submodule not initialized."
    echo "      Run: git submodule update --init --recursive"
    exit 1
fi

has_merge_base_with_head() {
    local ref="$1"
    git rev-parse --verify "$ref" >/dev/null 2>&1 && git merge-base "$ref" HEAD >/dev/null 2>&1
}

template_base="${PREFLIGHT_BASE:-}"
if [ -n "$template_base" ] && ! has_merge_base_with_head "$template_base"; then
    template_base=""
fi

if [ -z "$template_base" ]; then
    for candidate in \
        "origin/main" \
        "main" \
        "origin/${GITHUB_BASE_REF:-}" \
        "${GITHUB_BASE_REF:-}" \
        "HEAD^1" \
        "HEAD^"; do
        if [ -n "$candidate" ] && has_merge_base_with_head "$candidate"; then
            template_base="$candidate"
            break
        fi
    done
fi

if [ -z "$template_base" ] && [ -n "${CI:-}" ] && [ -n "${GITHUB_BASE_REF:-}" ]; then
    if git fetch --no-tags --depth=64 origin "${GITHUB_BASE_REF}:refs/remotes/origin/${GITHUB_BASE_REF}" >/dev/null 2>&1 && \
       has_merge_base_with_head "origin/${GITHUB_BASE_REF}"; then
        template_base="origin/${GITHUB_BASE_REF}"
    fi
fi

if [ -z "$template_base" ] && has_merge_base_with_head HEAD; then
    template_base="HEAD"
fi

# ---- TK: spawn the expensive independent gates early --------------------------
# Spawned BEFORE the dev-rules template so the heavy jobs (QA go test, the
# unittest-discover suites, the dockerized Caddyfile adapt) overlap with the
# template's own ~25s of generic + network checks. Joined at their original
# section positions below. Spawn guards mirror each section's prerequisite
# checks; a missing prerequisite leaves the job unspawned and the section
# falls back to its serial FAIL/skip path.
_qa_evidence_gate_run() {
    cd backend && go test -tags=unit -v ./internal/observability/qa -run 'TestUS077_QAEvidenceDatasetCheck_'
}
if command -v python3 >/dev/null 2>&1 && command -v go >/dev/null 2>&1; then
    _bg_spawn qa_evidence _qa_evidence_gate_run
fi
if command -v python3 >/dev/null 2>&1; then
    _bg_spawn anthropic_unittest \
        python3 -m unittest discover -s ops/anthropic -p 'test_*.py' -t ops/anthropic
    for _det_dir in ops/observability ops/stage0 scripts deploy/aws/stage0 deploy/aws/lightsail; do
        _bg_spawn "det_$(echo "$_det_dir" | tr '/' '_')" \
            env -u GIT_DIR -u GIT_INDEX_FILE -u GIT_WORK_TREE -u GIT_OBJECT_DIRECTORY -u GIT_COMMON_DIR \
            python3 -m unittest discover -s "$_det_dir" -p 'test_*.py' -t "$_det_dir"
    done
    unset _det_dir
fi
if [ -x ./scripts/checks/caddyfile-syntax.sh ]; then
    _bg_spawn caddyfile ./scripts/checks/caddyfile-syntax.sh
fi
# Git-free self-contained checks (file walkers / sandboxed selftests / unittest
# suites) also overlap with the template. Checks that read repo git state
# (git log / diff / merge-base) deliberately stay serial below to avoid racing
# the template's submodule walkers on worktree-local git config (core.bare debt).
if command -v python3 >/dev/null 2>&1; then
    _bg_spawn script_ref python3 ./scripts/checks/script-ref-existence.py
    _bg_spawn script_ref_test bash ./scripts/checks/script-ref-existence_test.sh
    _bg_spawn newapi_sibling_test bash ./scripts/checks/ensure-new-api-sibling_test.sh
    _bg_spawn redactor_test bash ./scripts/agent/redact-stream_test.sh
    _bg_spawn smoke_unittest python3 -m unittest scripts.test_smoke_suite \
        scripts.test_edge_smoke_phase_contract scripts.test_smoke_env \
        scripts.test_load_smoke_github_env -q
fi
_bg_spawn ssm_parse bash ./scripts/checks/check-stage0-ssm-host-parse.sh

PREFLIGHT_BASE="$template_base" PREFLIGHT_REPO_ROOT="$REPO_ROOT" ./dev-rules/templates/preflight.sh "$@"
dev_status=$?
if [ "$dev_status" -ne 0 ]; then
    exit "$dev_status"
fi

# ---- sub2api: newapi compat-pool drift --------------------------------------
# Source of truth: docs/approved/newapi-as-fifth-platform.md §5.1.
# Both checks deliberately use POSIX `grep -rnE` (not ripgrep) so they work
# in CI runners without rg installed.
echo ""
echo "=== sub2api: newapi compat-pool drift ==="
errors=0

# Check A — candidate-pool fetch must go through the TK helper
# (IsOpenAICompatPoolMember / OpenAICompatPlatforms). A new caller passing
# PlatformOpenAI directly to ListSchedulableAccounts would silently exclude
# newapi accounts and re-introduce the original P0 regression.
drift1_hits="$(grep -rnE 'ListSchedulableAccounts\([^)]*PlatformOpenAI' \
    backend/internal/service \
    --include='*.go' \
    --exclude='*_test.go' \
    --exclude='*_tk_*.go' 2>/dev/null || true)"
if [ -n "$drift1_hits" ]; then
    echo "  FAIL: direct PlatformOpenAI bucket usage outside TK helpers"
    echo "        (use OpenAICompatPlatforms / IsOpenAICompatPoolMember instead):"
    echo "$drift1_hits" | sed 's/^/    /'
    errors=$((errors + 1))
else
    echo "  ok: no direct PlatformOpenAI ListSchedulableAccounts callers outside TK helpers"
fi

# Check B — scheduler/gateway filters must not regress to bare
# `!account.IsOpenAI()`; the canonical predicate is
# `!account.IsOpenAICompatPoolMember(groupPlatform)`. The bare form silently
# rejects newapi accounts even for newapi groups.
#
# Exemption: lines annotated `// compat-pool-exempt:` are platform-specific
# predicates that are NOT pool-membership scheduling filters — e.g. the OpenAI
# quota auto-pause gate keys off codex 5h/7d usage windows that only exist on
# `openai` accounts (newapi accounts carry no such fields and must never be
# auto-paused by this path). The exemption is line-scoped and self-documenting.
drift2_hits="$(grep -nE '!\s*account\.IsOpenAI\(\)' \
    backend/internal/service/openai_account_scheduler.go \
    backend/internal/service/openai_gateway_service.go 2>/dev/null \
    | grep -v 'compat-pool-exempt' || true)"
if [ -n "$drift2_hits" ]; then
    echo "  FAIL: scheduling filter still uses bare !account.IsOpenAI()"
    echo "        — switch to !account.IsOpenAICompatPoolMember(groupPlatform):"
    echo "$drift2_hits" | sed 's/^/    /'
    errors=$((errors + 1))
else
    echo "  ok: scheduler / gateway filters use IsOpenAICompatPoolMember predicate"
fi

# ---- sub2api: engine dispatch / capability sentinels -------------------------
# Source of truth: backend/internal/engine/*. Capability truth should live in
# the engine registry, and non-bridge callers must not preflight video support
# against bridge-local helpers. Direct bridge.Dispatch* calls are only allowed
# in the approved service boundary files that funnel requests through the
# engine/service gate first.
echo ""
echo "=== sub2api: engine dispatch / capability sentinels ==="

# Check C — external callers must use engine.IsVideoSupportedChannelType rather
# than bridge-local truth. Otherwise capability semantics drift back into the
# relay layer and the Engine spine becomes nominal only.
drift3_hits="$(grep -rnE 'bridge\.IsVideoSupportedChannelType\(' \
    backend/internal/handler \
    backend/internal/service \
    --include='*.go' \
    --exclude='*_test.go' 2>/dev/null || true)"
if [ -n "$drift3_hits" ]; then
    echo "  FAIL: external callers still use bridge.IsVideoSupportedChannelType()"
    echo "        — switch to engine.IsVideoSupportedChannelType():"
    echo "$drift3_hits" | sed 's/^/    /'
    errors=$((errors + 1))
else
    echo "  ok: external video capability callers use engine truth"
fi

# Check D — endpoint capability truth must not regress to duplicate
# BridgeEndpointEnabled definitions outside engine/capability.go.
drift4_hits="$(grep -rnE '^func BridgeEndpointEnabled\(' \
    backend/internal/engine \
    --include='*.go' \
    --exclude='capability.go' 2>/dev/null || true)"
if [ -n "$drift4_hits" ]; then
    echo "  FAIL: duplicate BridgeEndpointEnabled truth source detected"
    echo "        — keep endpoint capability truth only in backend/internal/engine/capability.go:"
    echo "$drift4_hits" | sed 's/^/    /'
    errors=$((errors + 1))
else
    echo "  ok: endpoint capability truth is centralized in engine/capability.go"
fi

# Check E — direct bridge.Dispatch* calls must stay confined to the approved
# service bridge-boundary files, not spread into handlers or unrelated helpers.
drift5_hits="$(grep -rnE 'bridge\.Dispatch(ChatCompletions|Responses|Embeddings|ImageGenerations|VideoSubmit|VideoFetch)\(' \
    backend/internal/handler \
    backend/internal/service \
    --include='*.go' \
    --exclude='*_test.go' \
    --exclude='gateway_bridge_dispatch.go' \
    --exclude='openai_gateway_bridge_dispatch.go' \
    --exclude='openai_gateway_bridge_dispatch_tk_video.go' \
    --exclude='openai_gateway_bridge_dispatch_tk_anthropic.go' 2>/dev/null || true)"
if [ -n "$drift5_hits" ]; then
    echo "  FAIL: direct bridge.Dispatch* call escaped approved service boundary files"
    echo "        — route dispatch through the service/engine boundary instead:"
    echo "$drift5_hits" | sed 's/^/    /'
    errors=$((errors + 1))
else
    echo "  ok: direct bridge.Dispatch* calls stay inside approved boundary files"
fi

# ---- sub2api: OpenAI upstream capability truth -------------------------------
# Source of truth: backend/internal/pkg/openai_compat/upstream_capability.go.
# Responses endpoint probe status semantics belong in openai_compat so account
# creation, gateway routing, and tests cannot grow parallel interpretations.
echo ""
echo "=== sub2api: OpenAI upstream capability truth ==="
probe_owner_hits="$(grep -rnE '^func isResponsesEndpointSupportedByStatus\(' \
    backend/internal \
    --include='*.go' 2>/dev/null || true)"
if [ -n "$probe_owner_hits" ]; then
    echo "  FAIL: local Responses probe status truth detected"
    echo "        — use openai_compat.ResponsesEndpointSupportedByStatus instead:"
    echo "$probe_owner_hits" | sed 's/^/    /'
    errors=$((errors + 1))
elif ! grep -q 'openai_compat.ResponsesEndpointSupportedByStatus(resp.StatusCode)' backend/internal/service/openai_apikey_responses_probe.go; then
    echo "  FAIL: OpenAI API key probe no longer uses openai_compat status truth"
    errors=$((errors + 1))
elif ! grep -q 'func ResponsesEndpointSupportedByStatus(status int) bool {' backend/internal/pkg/openai_compat/upstream_capability.go; then
    echo "  FAIL: openai_compat Responses endpoint status owner is missing"
    errors=$((errors + 1))
else
    echo "  ok: Responses probe status truth is centralized in openai_compat"
fi

# ---- sub2api: buffered Content-Type leak gate --------------------------------
# Source of truth: scripts/checks/buffered-content-type-leak.py. Guards against
# the upstream Wei-Shaw/sub2api#1311 bug pattern: WriteFilteredHeaders
# propagates the upstream SSE Content-Type, and gin's c.JSON / c.Data render
# (which only sets Content-Type when the header map is empty) silently leaves
# the SSE Content-Type on a JSON response body. This is a forward-drift guard
# matching the project doctrine on declarative sentinels (CLAUDE.md §5.x) —
# the next refactor or upstream merge that re-introduces the antipattern fails
# preflight instead of shipping a regression to prod.
echo ""
echo "=== sub2api: buffered Content-Type leak gate ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to run check-buffered-content-type-leak.py)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/buffered-content-type-leak.py --quiet; then
    # check-buffered-content-type-leak.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: no buffered SSE->JSON Content-Type leak antipattern"
fi

# ---- sub2api: anthropic claude-code issue cache JSON --------------------------
# The .cache/anthropic/cc-*.json triage ledger (refreshed by
# anthropic-cc-issue-watchdog.yml, hand-curated for cc-fixes / cc-fact-checks) must
# stay valid JSON with its required top-level keys. The workflow json-validates
# inline; this gate catches a hand-edit before push. Source: scripts/checks/cc-issue-cache-json.py.
echo ""
echo "=== sub2api: anthropic cc-issue cache JSON ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to run cc-issue-cache-json.py)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/cc-issue-cache-json.py --quiet; then
    # cc-issue-cache-json.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: anthropic cc-issue cache files valid"
fi

# ---- sub2api: OpsUpstreamErrorEvent.Kind suffix antipattern ----------------
# Guards against the regression pattern that surfaced in prod ops_error_logs:
# storage metadata (e.g. ":request_body_truncated") appended onto the
# categorical Kind enum, splitting the same root cause into multiple buckets
# based purely on body size. Kind must stay a clean short label; per-event
# storage flags belong on dedicated boolean fields of OpsUpstreamErrorEvent.
# See ops_upstream_context.go Kind comment for the contract.
echo ""
echo "=== sub2api: OpsUpstreamErrorEvent.Kind suffix antipattern ==="
kind_suffix_hits=$(grep -rEn '(out|ev)\.Kind[[:space:]]*=[[:space:]]*(out|ev)\.Kind[[:space:]]*\+' \
    backend/internal/service/ops*.go 2>/dev/null | grep -v '_test\.go' || true)
if [ -n "$kind_suffix_hits" ]; then
    echo "  fail: Kind enum must stay categorical — never append storage metadata."
    echo "        Use a dedicated boolean field on OpsUpstreamErrorEvent instead."
    echo "$kind_suffix_hits" | sed 's/^/        /'
    errors=$((errors + 1))
else
    echo "  ok: no Kind enum suffix antipattern"
fi

# ---- sub2api: newapi sentinel registry --------------------------------------
# Source of truth: scripts/sentinels/newapi.json. Verifies that every
# load-bearing surface of the fifth platform (`newapi`) — TK companion files,
# canonical predicates, frontend platform enumerations — is still present.
# This catches the failure mode that triggered this guard: an upstream merge
# silently dropping a file or a switch-case branch and the regression only
# surfacing weeks later in production.
echo ""
echo "=== sub2api: newapi sentinel registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read newapi-sentinels.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-newapi.py --quiet; then
    # check-newapi-sentinels.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: all newapi sentinels intact"
fi

# ---- sub2api: kiro sentinel registry ----------------------------------------
# Source of truth: scripts/sentinels/kiro.json. Verifies that every load-bearing
# surface of the sixth platform (`kiro`, AWS Kiro / CodeWhisperer) — the vendored
# protocol layer (internal/integration/kiro), PlatformKiro identity, the
# KiroGatewayService Forward injection point, IsKiro, the KiroTokenRefresher, and
# the ToS gate — is still present. Same failure mode as the newapi guard: an
# upstream merge silently dropping a file or injection branch.
echo ""
echo "=== sub2api: kiro sentinel registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read kiro.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-kiro.py --quiet; then
    # check-kiro.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: all kiro sentinels intact"
fi

# ---- sub2api: brand sentinel registry ---------------------------------------
# Source of truth: scripts/sentinels/brand.json. Verifies that outward TokenKey
# brand surfaces (default title, deploy/operator docs, image metadata,
# fifth-platform display label) stay converged without turning compat identities
# like `sub2api` / `newapi` into banned strings across the repo.
echo ""
echo "=== sub2api: brand sentinel registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read brand-sentinels.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-brand.py --quiet; then
    # check-brand-sentinels.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: all brand sentinels intact"
fi

# ---- sub2api: pricing-availability sentinel registry ------------------------
# Source of truth: scripts/sentinels/pricing-availability.json. Verifies that
# the 1-line TK availability-tap injections in upstream-shaped handler and
# service files (TkRecordFailureFromErr call sites, RecordOutcome hook) are
# still present after any upstream merge. Without these taps the model_availability
# table receives no data and the /pricing availability decoration silently stops
# working. See docs/approved/pricing-availability-source-of-truth.md §4 and
# CLAUDE.md §「升级原则」.
echo ""
echo "=== sub2api: pricing-availability sentinel registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read pricing-availability-sentinels.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-pricing-availability.py --quiet; then
    # check-pricing-availability-sentinels.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: all pricing-availability sentinels intact"
fi

# ---- sub2api: pricing overlay -----------------------------------------------
# Source of truth: backend/internal/service/tk_pricing_overlay.json. The
# production price source (Wei-Shaw mirror) is a trimmed litellm that drops
# provider-prefixed + token-less media entries (and litellm itself lags new
# provider models like deepseek-v4-*), so those models resolve to a wrong
# default or $0 unless this fill-only overlay supplies them. Assert the overlay
# is non-empty, anchors are present, and no entry ships a $0 price. CLAUDE.md
# §「升级原则」: a soft rule that bit us once becomes a mechanical gate.
echo ""
echo "=== sub2api: pricing overlay ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to validate tk_pricing_overlay.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/pricing-overlay.py --quiet; then
    # pricing-overlay.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: pricing overlay valid (anchors present, no \$0)"
fi

# ---- sub2api: pricing-hotfix runbook selftest -------------------------------
# ops/pricing/apply-pricing-hotfix.py is the runbook the PricingMissingNotifier
# Feishu card points operators at (lookup / apply channel pricing / stage
# overlay). Its selftest is offline and covers all pure logic — run it so a
# refactor of the overlay file format or upsert rules fails preflight instead
# of failing the operator mid-incident.
echo ""
echo "=== sub2api: pricing-hotfix runbook selftest ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for pricing-hotfix selftest)"
    errors=$((errors + 1))
elif ! python3 ./ops/pricing/apply-pricing-hotfix.py selftest >/dev/null 2>&1; then
    echo "  FAIL: pricing-hotfix runbook selftest"
    echo "        — run: python3 ops/pricing/apply-pricing-hotfix.py selftest"
    errors=$((errors + 1))
else
    echo "  ok: pricing-hotfix runbook selftest"
fi

# ---- sub2api: frontend TK sentinel registry ---------------------------------
# Source of truth: scripts/sentinels/frontend-tk.json. Verifies that load-bearing
# TokenKey-only frontend surfaces (sidebar geometry, fluid table mode, sticky
# edge-hints opt-out on the admin accounts page) are still present. These are
# small, additive divergences from upstream that compile cleanly with or
# without the TK touches, so without a literal-content guard a bad upstream
# merge can silently revert them and the regression only shows up visually.
echo ""
echo "=== sub2api: frontend TK sentinel registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read frontend-tk-sentinels.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-frontend-tk.py --quiet; then
    # check-frontend-tk-sentinels.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: all frontend TK sentinels intact"
fi

# ---- sub2api: gateway TK sentinel registry ----------------------------------
# Source of truth: scripts/sentinels/gateway-tk.json. Verifies that small
# TokenKey-only gateway/service injections in upstream-shaped hotspot files are
# still present after merges. These hooks compile cleanly if dropped but cause
# production routing / rate-limit regressions later.
echo ""
echo "=== sub2api: gateway TK sentinel registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read gateway-tk-sentinels.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-gateway-tk.py --quiet; then
    # check-gateway-tk-sentinels.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: all gateway TK sentinels intact"
fi

# ---- sub2api: anthropic baseline ↔ ratelimit constants sync -----------------
# Anthropic OAuth tier baseline JSON documents the cooldown ladder
# (30s / 2min / 10min) and 30-min tier TTL that the Go runtime owns. If the
# JSON drifts from the Go constants, ops dashboards mislead operators about
# what the production ratelimit_service.go actually does. See PR #337 +
# scripts/sentinels/check-anthropic-baseline-sync.py header for the
# 2026-05-21 incident that motivated this guard.
echo ""
echo "=== sub2api: anthropic baseline sync ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for anthropic baseline sync check)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-anthropic-baseline-sync.py --quiet; then
    # check-anthropic-baseline-sync.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: anthropic baseline JSON in sync with ratelimit_service.go constants"
fi

# ---- sub2api: embedded tier/stub baseline ↔ deploy source single-source ------
# The backend embeds the tier baseline + stub-pool policy (backend/internal/
# baseline/) so the in-process ApplyTier UI action and the per-node config
# reconciler derive desired account config without an operator laptop / SSM
# round-trip. go:embed cannot reach outside the backend module, so those JSONs
# are COPIES of the canonical deploy/aws/stage0 sources. This guard makes any
# drift between copy and source a hard failure (single-source discipline,
# CLAUDE.md §10 / memory "anthropic tier baseline 单一源").
echo ""
echo "=== sub2api: embedded tier baseline single-source ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for tier baseline embed check)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-tier-baseline-embed.py --quiet; then
    # check-tier-baseline-embed.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: embedded tier/stub baselines match deploy/aws/stage0 sources"
fi

# ---- sub2api: cc version string single-source -------------------------------
# A cc CLI patch bump (e.g. 2.1.158 -> 2.1.159) used to require hand-editing
# ~10 files; PR #482 shipped 5 wrong dead-value edits because nothing checked
# them. Source of truth is anthropic-http-mimicry-baselines.json cc_version;
# check-cc-version-sync.py proves every Go compile default + dead snapshot
# agrees, and its --selftest keeps the guard's own parse/write logic honest.
echo ""
echo "=== sub2api: cc version string single-source ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for cc version sync check)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-cc-version-sync.py --selftest >/dev/null; then
    echo "  FAIL: check-cc-version-sync.py self-test failed"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-cc-version-sync.py --quiet; then
    # check-cc-version-sync.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: cc_version in anthropic-http-mimicry-baselines.json synced to all copies"
fi

# ---- sub2api: cc system prompt single-source --------------------------------
# The CC system prompt is a load-bearing fingerprint dimension hardcoded in 3+
# Go copies (validator templates + gateway inject banner/prefixes). They can
# silently diverge, and a drift from real CC risks upstream
# `client_validation_error 403` for spoofed clients (linux.do 2.1.15 incident).
# Source of truth is scripts/sentinels/cc-system-prompt.json; the guard proves
# every Go copy still carries the anchors and the banner is byte-identical
# across files. Real CC drift is detected separately by the capture skill.
echo ""
echo "=== sub2api: cc system prompt single-source ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for cc system prompt check)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-cc-system-prompt.py --selftest >/dev/null; then
    echo "  FAIL: check-cc-system-prompt.py self-test failed"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-cc-system-prompt.py --quiet; then
    # check-cc-system-prompt.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: cc system prompt anchors consistent across Go copies"
fi

# ---- sub2api: sentinel registry update gate ---------------------------------
# Existing sentinel checks prove current guarded literals still exist. This gate
# proves PRs that modify guarded/hotspot files also update the matching registry,
# turning the recurring review ask "补充必要的 upstream merge 覆写防护门禁" into
# a hard preflight failure instead of human memory.
echo ""
echo "=== sub2api: sentinel registry update gate (advisory locally) ==="
# MARKER_GATE_ADVISORY=1: pre-commit/pre-push structurally cannot see the
# in-flight commit message or the PR body, so a hard block here is a false
# deadlock (benign pure-insertion / i18n PRs paid stash+force-push tax). The
# script prints guidance but exits 0; the HARD gate runs in CI against the PR
# body (.github/workflows/marker-acknowledgement-pr.yml + upstream-merge-pr-shape
# Check 12). Pure-insertion / i18n changes are auto-accepted by the script.
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for sentinel registry update gate)"
    errors=$((errors + 1))
elif ! MARKER_GATE_ADVISORY=1 python3 ./scripts/sentinels/check-registry-update-gate.py --quiet; then
    # advisory mode never returns non-zero; this branch only fires on a hard
    # script error (exit 2 — malformed registry / unresolved base).
    errors=$((errors + 1))
else
    echo "  ok: sentinel update gate evaluated (advisory; CI enforces on PR body)"
fi

# ---- sub2api: script ref existence ------------------------------------------
# Closes the OPC gap exposed by PR #307: when scripts/ files move, repo-wide
# literal `scripts/...|ops/...|tools/...` references can be left stale. The
# refactor's sed pass walked a limited path set and silently missed Dockerfile,
# frontend/package.json, and .goreleaser*.yaml — each was a CI build failure
# waiting to happen. This check fails preflight on any literal path of that
# shape that does not resolve to an existing file.
#
# The check has non-trivial regex + multi-step path resolution (Docker context,
# relative invocation, submodule-nested refs). Its 9-pattern self-test runs
# adjacent so any future regex/resolver edit that re-introduces v1's
# "blind to ../scripts/X" class of bug fires at preflight time.
echo ""
echo "=== sub2api: script ref existence ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for script ref existence check)"
    errors=$((errors + 1))
else
    for _sr_key in script_ref script_ref_test newapi_sibling_test; do
        _bg_rc=1
        if _bg_spawned "$_sr_key"; then
            _bg_join "$_sr_key"
            # The scripts print their own actionable failure lists.
            cat "$_preflight_bg_dir/$_sr_key.out"
        else
            echo "  FAIL: $_sr_key background job was not spawned (internal preflight bug)"
        fi
        if [ "$_bg_rc" -ne 0 ]; then
            errors=$((errors + 1))
        fi
    done
    unset _sr_key
fi

# ---- sub2api: upstream override marker --------------------------------------
# Whole-fork version of the same idea. The sentinel-registry-update-gate above
# only fires when a *currently registered* hotspot is touched. This check fires
# on *any* upstream-shaped path (backend/internal/handler|service|repository|...,
# frontend/src/views|components|api|...). Forces the author to either pin new
# anchors via sentinel registry OR carry an explicit marker in commit message
# (upstream-touch-guarded / upstream-touch-trivial / upstream-merge /
# no-upstream-touch). Same shape as `no-web-impact`: the marker is a forced
# acknowledgement that this PR's diff against an upstream merge has been
# considered.
echo ""
echo "=== sub2api: upstream override marker (advisory locally) ==="
# MARKER_GATE_ADVISORY=1 — advisory locally (cannot see in-flight commit /
# PR body); HARD gate is .github/workflows/marker-acknowledgement-pr.yml which
# reads the PR body. Pure-insertion / i18n diffs are auto-accepted.
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for upstream override marker check)"
    errors=$((errors + 1))
elif ! MARKER_GATE_ADVISORY=1 python3 ./scripts/checks/upstream-override-marker.py --quiet; then
    # advisory mode never returns non-zero; this branch only fires on a hard
    # script error (exit 2 — git failure).
    errors=$((errors + 1))
else
    echo "  ok: upstream override marker evaluated (advisory; CI enforces on PR body)"
fi

# ---- sub2api: redaction version contract ------------------------------------
# Source of truth: scripts/sentinels/redaction.json. Verifies that the default
# sensitive-key set in logredact and the outward QA redaction_version literals
# move together, so a changed evidence redaction policy cannot silently keep the
# old version string.
echo ""
echo "=== sub2api: redaction version contract ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read redaction-sentinels.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-redaction-version.py --quiet; then
    # check-redaction-version.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: redaction key snapshot and version sources are aligned"
fi

# ---- sub2api: trajectory hook registry --------------------------------------
# Source of truth: scripts/sentinels/trajectory.json. Verifies that the main
# gateway route scopes still carry trajectory_id + qaCapture wiring, and that
# the QA middleware still terminates in CaptureFromContext after teeing request /
# response bodies.
echo ""
echo "=== sub2api: trajectory hook registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read trajectory-sentinels.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-trajectory-hooks.py --quiet; then
    # check-trajectory-hooks.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: gateway trajectory hooks and QA terminal capture are aligned"
fi

# ---- sub2api: terminal event registry ---------------------------------------
# Source of truth: scripts/sentinels/terminal.json. Verifies that the stable
# terminal-event helpers, `[DONE]` emission, and focused terminal assertions stay
# intact so evidence capture keeps reliable completion markers.
echo ""
echo "=== sub2api: terminal event registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read terminal-sentinels.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-terminal-events.py --quiet; then
    # check-terminal-events.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: terminal-event helpers and focused assertions are aligned"
fi

# ---- sub2api: engine facade registry -----------------------------------------
# Source of truth: scripts/sentinels/engine-facade.json. Verifies that the key
# gateway dispatch paths still route bridge eligibility through the shared
# Engine facade helpers instead of reintroducing local provider branching.
echo ""
echo "=== sub2api: engine facade registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read engine-facade-sentinels.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-engine-facade.py --quiet; then
    # check-engine-facade-hooks.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: key dispatch paths still route through Engine facade truth"
fi

# ---- sub2api: QA evidence dataset validator ----------------------------------------
# Source of truth: scripts/checks/qa-evidence-dataset.py. Verifies that the standalone
# QA evidence dataset gate remains executable from repo root and the regression
# tests covering pass/fail fixtures stay green, so projection/export acceptance
# thresholds remain mechanically enforced.
echo ""
echo "=== sub2api: QA evidence dataset validator ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to run check-qa-evidence-dataset.py)"
    errors=$((errors + 1))
elif ! command -v go >/dev/null 2>&1; then
    echo "  FAIL: go not on PATH (required to run QA evidence dataset regression tests)"
    errors=$((errors + 1))
elif ! _bg_spawned qa_evidence; then
    echo "  FAIL: QA evidence gate background job was not spawned (internal preflight bug)"
    errors=$((errors + 1))
else
    _bg_join qa_evidence
    if [ "$_bg_rc" -ne 0 ]; then
        tail -40 "$_preflight_bg_dir/qa_evidence.out" | sed 's/^/    /'
        errors=$((errors + 1))
    elif ! grep -q -- '--- PASS: TestUS077_QAEvidenceDatasetCheck_' "$_preflight_bg_dir/qa_evidence.out"; then
        # `go test -run <regex>` exits 0 on ZERO matches — a rename/move would pass
        # vacuously. The -v output (replayed verbatim on cached runs too) must show
        # at least one matching top-level test PASS.
        echo "  FAIL: -run 'TestUS077_QAEvidenceDatasetCheck_' matched ZERO tests (renamed/moved?); go test -run exits 0 on no match"
        errors=$((errors + 1))
    else
        echo "  ok: QA evidence dataset validator accepts/rejects covered fixtures as expected"
    fi
fi

# ---- sub2api: frontend release asset contract -------------------------------
echo ""
echo "=== sub2api: frontend release asset contract ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to run frontend release checks)"
    errors=$((errors + 1))
elif ! python3 -m py_compile ./scripts/checks/frontend-release-assets.py ./scripts/checks/frontend-dist-freshness.py; then
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/frontend-dist-freshness.py --check ./backend/internal/web/dist; then
    errors=$((errors + 1))
elif [ -f ./backend/internal/web/dist/index.html ] && ls ./backend/internal/web/dist/assets/AccountsView-*.js >/dev/null 2>&1; then
    if ! python3 ./scripts/checks/frontend-release-assets.py --dist ./backend/internal/web/dist; then
        errors=$((errors + 1))
    else
        echo "  ok: embedded frontend dist is fresh and carries critical account-modal UI contracts"
    fi
else
    echo "  ok: embedded frontend source manifest is fresh; release workflow rebuilds full dist assets"
fi

# ---- sub2api: post-deploy smoke script (syntax only; no live HTTP) ----------
echo ""
echo "=== sub2api: post-deploy smoke script syntax ==="
_smoke_syntax_ok=true
for _smoke_script in \
  ./ops/stage0/smoke_env.sh \
  ./ops/stage0/load_smoke_github_env.sh \
  ./ops/stage0/smoke_lib.sh \
  ./ops/stage0/post_deploy_smoke.sh \
  ./ops/stage0/edge_post_deploy_smoke.sh \
  ./scripts/stage0/dispatch-edge-deploy.sh; do
  if ! bash -n "${_smoke_script}"; then
    echo "  FAIL: ${_smoke_script} has bash syntax errors"
    errors=$((errors + 1))
    _smoke_syntax_ok=false
  fi
done
if [[ "${_smoke_syntax_ok}" == "true" ]]; then
  echo "  ok: stage0 smoke scripts parse"
fi

# ---- sub2api: SSM host command script parses (deploy/sync primitives) --------
# The jq `commands` array these scripts send to AWS-RunShellScript is joined and
# run on the host; "JSON valid" locally does NOT catch host-shell syntax errors
# (e.g. unquoted parens in an echo — the #512 bug caught only by a us1 canary).
echo ""
echo "=== sub2api: SSM host command script parse ==="
_bg_rc=1
if _bg_spawned ssm_parse; then
    _bg_join ssm_parse
    cat "$_preflight_bg_dir/ssm_parse.out"
fi
if [ "$_bg_rc" -ne 0 ]; then
  echo "  FAIL: an SSM host command script has a shell syntax error"
  errors=$((errors + 1))
fi

echo ""
echo "=== sub2api: gateway smoke suite unit tests ==="
_bg_rc=1
if _bg_spawned smoke_unittest; then
    _bg_join smoke_unittest
fi
if [ "$_bg_rc" -ne 0 ]; then
  cat "$_preflight_bg_dir/smoke_unittest.out" 2>/dev/null
  echo "  FAIL: smoke suite contract tests"
  errors=$((errors + 1))
else
  echo "  ok: smoke suite contract tests"
fi

# ---- sub2api: run-probe.sh --env loop regression guard ----------------------
# PR #405 (c0ab27c9) silently dropped the `for kv in "${ENVS[@]+"${ENVS[@]}"}"; do`
# line while restructuring EC2 vs Lightsail target resolution. The env-prefix
# builder kept compiling but ran once with `kv` unbound under `set -u`, so every
# `--env KEY=VAL` invocation failed before any SSM transport. PR #407 restored
# the line; this static anchor prevents the same regression from recurring on
# future wrapper refactors. Mechanical guard (no execution) — minimal noise.
echo ""
echo "=== sub2api: run-probe.sh --env loop regression guard ==="
if ! grep -qE 'for[[:space:]]+kv[[:space:]]+in[[:space:]]+"\$\{ENVS\[@\]\+"\$\{ENVS\[@\]\}"\}"; do' ops/observability/run-probe.sh; then
    echo "  FAIL: ops/observability/run-probe.sh missing the \`for kv in \"\${ENVS[@]+...}\"\` env loop"
    echo "        (PR #405 c0ab27c9 regression shape — see PR #407 fix)"
    errors=$((errors + 1))
else
    echo "  ok: run-probe.sh --env loop anchored"
fi

# ---- sub2api: data-layer capacity verdict selftest -------------------------
# The capacity probe's threshold logic (green/approaching/trigger = #587 Trigger B)
# lives in data_layer_capacity_verdict.py and is consumed by ops-daily-diagnostics.
# Run its fixture selftest so a threshold/logic regression fails preflight instead
# of silently mis-verdicting prod capacity. Read-only, no AWS.
echo ""
echo "=== sub2api: data-layer capacity verdict selftest ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for data-layer capacity selftest)"
    errors=$((errors + 1))
elif ! python3 ./ops/observability/data_layer_capacity_verdict.py --selftest >/dev/null 2>&1; then
    echo "  FAIL: data-layer capacity verdict selftest"
    echo "        — run: python3 ops/observability/data_layer_capacity_verdict.py --selftest"
    errors=$((errors + 1))
else
    echo "  ok: data-layer capacity verdict green/approaching/trigger fixtures pass"
fi

# ---- sub2api: edge-health verdict selftest ---------------------------------
# The edge-health threshold logic (healthy/thin/degraded/down) lives in
# edge_health_verdict.py and turns probe-edge-health.sh output into the one signal
# that can tell a dead edge from a healthy one (prod's upstream-429 cannot — see the
# 2026-06-06 yace burst). Its fixtures pin the six real edges + boundaries, so a
# threshold/logic regression fails preflight instead of silently mis-verdicting edge
# health. Read-only, no AWS.
echo ""
echo "=== sub2api: edge-health verdict selftest ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for edge-health verdict selftest)"
    errors=$((errors + 1))
elif ! python3 ./ops/observability/edge_health_verdict.py --selftest >/dev/null 2>&1; then
    echo "  FAIL: edge-health verdict selftest"
    echo "        — run: python3 ops/observability/edge_health_verdict.py --selftest"
    errors=$((errors + 1))
else
    echo "  ok: edge-health verdict healthy/thin/degraded/down fixtures pass"
fi

# ---- sub2api: edge-health alert decision selftest --------------------------
# The edge-health-watch alert logic (actionable-set + state-diff dedup + Feishu
# message) lives in edge-health-alert.py and is the decision half of
# .github/workflows/edge-health-watch.yml. Its fixtures pin the 2026-06-07 incident
# shapes + the dedup behavior (no re-spam on a steady incident, alert on escalation /
# recovery, chronic thin does not trigger), so a logic regression fails preflight
# instead of silently re-spamming or going silent. Read-only, no AWS / no HTTP.
echo ""
echo "=== sub2api: edge-health alert decision selftest ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for edge-health alert selftest)"
    errors=$((errors + 1))
elif ! python3 ./ops/observability/edge-health-alert.py --selftest >/dev/null 2>&1; then
    echo "  FAIL: edge-health alert decision selftest"
    echo "        — run: python3 ops/observability/edge-health-alert.py --selftest"
    errors=$((errors + 1))
else
    echo "  ok: edge-health alert decision/dedup fixtures pass"
fi

# ---- sub2api: pgdump timer cadence parity ----------------------------------
# The tokenkey-pgdump systemd timer is defined in TWO places that must stay in
# sync: the first-boot copy in stage0-ec2-bootstrap.sh and the live-host refresh
# in ops/stage0/pg_dump_refresh_via_ssm.sh. A cadence change to one but not the
# other silently leaves prod on a stale schedule. Compare the pgdump timer's
# OnCalendar line across both (value-agnostic — extract + equal, so the cadence
# can change freely as long as both move together).
echo ""
echo "=== sub2api: pgdump timer cadence parity ==="
_pgdump_boot_cal="$(awk '/tokenkey-pgdump\.timer/{f=1} f&&/OnCalendar=/{print; exit}' deploy/aws/stage0/stage0-ec2-bootstrap.sh 2>/dev/null)"
_pgdump_refresh_cal="$(grep -m1 'OnCalendar=' ops/stage0/pg_dump_refresh_via_ssm.sh 2>/dev/null)"
if [ -z "${_pgdump_boot_cal}" ] || [ -z "${_pgdump_refresh_cal}" ]; then
    echo "  FAIL: could not extract pgdump timer OnCalendar from both sources"
    echo "        boot='${_pgdump_boot_cal}' refresh='${_pgdump_refresh_cal}'"
    errors=$((errors + 1))
elif [ "${_pgdump_boot_cal}" != "${_pgdump_refresh_cal}" ]; then
    echo "  FAIL: pgdump timer cadence drift between bootstrap and pg_dump_refresh"
    echo "        bootstrap:        ${_pgdump_boot_cal}"
    echo "        pg_dump_refresh:  ${_pgdump_refresh_cal}"
    errors=$((errors + 1))
else
    echo "  ok: pgdump timer cadence in sync (${_pgdump_boot_cal})"
fi

# ---- sub2api: workflow job-level if env-context drift -----------------------
# GitHub Actions does NOT allow env references in jobs.<name>.if expressions
# (env evaluates AFTER if). Such references make the entire workflow file
# fail to parse with HTTP 422 "Unrecognized named-value: 'env'", silently
# breaking every tag-push / workflow_dispatch trigger that depends on it.
# 2026-05-06 v1.7.17 prod release was blocked exactly this way (PR #120
# introduced; PR #122 fixed). This guard prevents recurrence.
echo ""
echo "=== sub2api: workflow job-level if env-context ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to parse .github/workflows/*.yml)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/workflow-job-if-env.py --quiet; then
    # check-workflow-job-if-env.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: no env references in job-level if expressions"
fi

# ---- sub2api: release.yml simple_release default = false --------------------
# Mechanizes CLAUDE.md §9.1. prod (api.tokenkey.dev) and Edge Stage0 hosts run
# on AWS Graviton (arm64). simple_release=true builds linux/amd64 only and
# overwrites the shared :latest / :X / :X.Y / :X.Y.Z tags — any ARM host
# pulling those tags crashes immediately with `exec format error`. The prose
# rule has been "depends on human memory" until this check; now flipping the
# at-rest default fails preflight + CI before merge. One-off amd64 releases
# still work via manual `gh workflow run release.yml -f simple_release=true`.
echo ""
echo "=== sub2api: release.yml simple_release default ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to parse release.yml)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/release-simple-release-default.py --quiet; then
    errors=$((errors + 1))
else
    echo "  ok: release.yml simple_release default = false (Graviton-safe)"
fi

# ---- sub2api: main ancestry anchor ------------------------------------------
# Mechanizes CLAUDE.md §5.y / §5.y.1 ("`main` is immutable history once
# pushed"). The previous enforcement stack worked on diff content; none of
# the gates noticed when an orphan-branch squash merge severed main from
# release tag v1.7.37 (commit 5a3c120d became unreachable from HEAD). The
# anchor is a single SHA pinned in repo-root .main-ancestry-anchor; this
# check fails if that SHA is no longer reachable from HEAD. Companion
# .github/workflows/main-ancestry-guard.yml catches the same failure mode
# at PR-merge time (PR.base must be ancestor of PR.head).
echo ""
echo "=== sub2api: main ancestry anchor ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read .main-ancestry-anchor)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/main-ancestry-anchor.py; then
    errors=$((errors + 1))
fi

# ---- sub2api: Caddyfile syntax gate ------------------------------------------
# Stage0 deploy path treats deploy/aws/stage0/Caddyfile(.edge) as source of
# truth for CloudFormation-embedded payloads. Parse failures here should block
# before merge rather than surfacing during deploy.
echo ""
echo "=== sub2api: Caddyfile syntax gate ==="
if [ ! -x ./scripts/checks/caddyfile-syntax.sh ] || ! _bg_spawned caddyfile; then
    echo "  FAIL: scripts/checks/caddyfile-syntax.sh missing or not executable"
    errors=$((errors + 1))
else
    _bg_join caddyfile
    sed 's/^/    /' "$_preflight_bg_dir/caddyfile.out"
    if [ "$_bg_rc" -ne 0 ]; then
        errors=$((errors + 1))
    else
        echo "  ok: Caddyfile syntax gate"
    fi
fi

# ---- sub2api: edge-ip-status doc / live AWS drift ---------------------------
# Source of truth: deploy/aws/stage0/edge-polluted-ips.json. The script's --check
# mode reconciles docs/deploy/tokenkey-edge-ip-history.md polluted table only.
echo ""
echo "=== sub2api: edge-ip-status doc / live AWS drift ==="
if [ ! -x ./scripts/edge-ip-status.sh ]; then
    echo "  FAIL: scripts/edge-ip-status.sh missing or not executable"
    errors=$((errors + 1))
elif ! ./scripts/edge-ip-status.sh --check; then
    errors=$((errors + 1))
fi

echo ""
echo "=== sub2api: Stage0 deployment primitive sharing ==="
# EC2 edge path removed 2026-06-07 (deploy-edge-stage0.yml deleted); edges are
# Lightsail-only. prod (deploy-stage0.yml) + Lightsail edge (deploy-edge-lightsail-stage0.yml)
# must share the SSM deploy primitive and never inline the compose deploy command.
if ! grep -q 'ops/stage0/deploy_via_ssm.sh' .github/workflows/deploy-stage0.yml; then
    echo "  FAIL: deploy-stage0.yml must use ops/stage0/deploy_via_ssm.sh"
    errors=$((errors + 1))
elif ! grep -q 'ops/stage0/deploy_via_ssm.sh' .github/workflows/deploy-edge-lightsail-stage0.yml; then
    echo "  FAIL: deploy-edge-lightsail-stage0.yml must use ops/stage0/deploy_via_ssm.sh"
    errors=$((errors + 1))
elif grep -q 'docker compose --env-file .* up -d --no-deps tokenkey' .github/workflows/deploy-stage0.yml .github/workflows/deploy-edge-lightsail-stage0.yml; then
    echo "  FAIL: Stage0 workflows must not inline tokenkey SSM deploy commands; use ops/stage0/deploy_via_ssm.sh"
    errors=$((errors + 1))
else
    echo "  ok: prod deploy-stage0 and Lightsail edge workflow share the Stage0 SSM deploy primitive"
fi

echo ""
echo "=== sub2api: Stage0 deploy tag-validation sharing ==="
# The X.Y.Z(-rc.N/-beta.N) release-tag gate is a single shared script so the
# three deploy workflows never grow divergent copies (the Lightsail copy had
# already drifted to a shorter error message). This sentinel fails closed if any
# deploy workflow stops calling it or re-inlines the regex.
_tag_script="ops/stage0/validate-deploy-tag.sh"
_tag_files=".github/workflows/deploy-stage0.yml .github/workflows/deploy-edge-lightsail-stage0.yml"
_tag_ok=1
if [ ! -x "$_tag_script" ]; then
    echo "  FAIL: $_tag_script missing or not executable (shared tag-format gate)"
    errors=$((errors + 1)); _tag_ok=0
fi
for _f in $_tag_files; do
    [ -f "$_f" ] || continue
    if ! grep -q "$_tag_script" "$_f"; then
        echo "  FAIL: $_f must call $_tag_script (do not re-inline the tag regex)"
        errors=$((errors + 1)); _tag_ok=0
    fi
    if grep -Fq '[0-9]+\.[0-9]+\.[0-9]+' "$_f"; then
        echo "  FAIL: $_f re-inlines the X.Y.Z tag regex; call $_tag_script instead"
        errors=$((errors + 1)); _tag_ok=0
    fi
done
if [ -x "$_tag_script" ]; then
    if ! bash "$_tag_script" 1.2.3 >/dev/null 2>&1 || ! bash "$_tag_script" 1.2.3-rc.4 >/dev/null 2>&1; then
        echo "  FAIL: $_tag_script rejected a well-formed tag (1.2.3 / 1.2.3-rc.4)"
        errors=$((errors + 1)); _tag_ok=0
    fi
    if bash "$_tag_script" 1.2 >/dev/null 2>&1 || bash "$_tag_script" "" >/dev/null 2>&1; then
        echo "  FAIL: $_tag_script accepted a malformed/empty tag"
        errors=$((errors + 1)); _tag_ok=0
    fi
fi
[ "$_tag_ok" = 1 ] && echo "  ok: prod + Edge deploy workflows share the tag-format gate ($_tag_script)"
unset _tag_script _tag_files _tag_ok _f

echo ""
echo "=== sub2api: Stage0 deploy job timeouts ==="
# Every Stage0 deploy job has cancel-in-progress:false concurrency, so a hung
# step (describe-stacks / health-poll) would hold the prod/edge lock to the 6h
# GHA default without a job-level timeout-minutes. Assert the cap stays present.
_dt_ok=1
for _df in .github/workflows/deploy-stage0.yml .github/workflows/deploy-edge-lightsail-stage0.yml; do
    [ -f "$_df" ] || continue
    if ! grep -q '^    timeout-minutes:' "$_df"; then
        echo "  FAIL: $_df job is missing timeout-minutes (a hang would hold the deploy concurrency lock up to the 6h GHA default)"
        errors=$((errors + 1)); _dt_ok=0
    fi
done
[ "$_dt_ok" = 1 ] && echo "  ok: all Stage0 deploy jobs declare a job-level timeout-minutes"
unset _dt_ok _df

# Anthropic OAuth tier baseline values now live in exactly one place: the JSON
# source of truth. The apply SQL is generated from it at runtime (orchestrator
# reuses the guard's generate_sql), so there is no second hand-aligned copy to
# drift. This guard fails if the retired dual-source SQL template reappears —
# re-introducing it would resurrect the "edit one, forget the other" failure
# mode this dedup removed. Single-source wiring itself is asserted by the
# ops/anthropic unittest suite below (render embeds the JSON values).
echo ""
echo "=== sub2api: anthropic tier baseline single-source ==="
if [ -f ./deploy/aws/stage0/anthropic-oauth-stability-tiered-apply-template.sql ]; then
    echo "  FAIL: retired SQL apply template reappeared — tier baseline values must"
    echo "        live only in anthropic-oauth-stability-baselines-tiered.json; the"
    echo "        orchestrator renders SQL from it via the guard's generate_sql."
    errors=$((errors + 1))
else
    echo "  ok: tier baseline values live only in the JSON source of truth"
fi

# Ops/Anthropic orchestrator coverage (stdlib only; no AWS / pytest).
# Runs one `unittest discover -s ops/anthropic`; pin set includes:
#   · manage-anthropic-config.py plan-edge-account-tier (fields_match noop,
#     --force-template-rewrite hatch; snapshot/verify don’t exercise those)
#   · rebalance-anthropic-priority.py plan/scoring exits + SQL name guard
echo ""
echo "=== sub2api: ops/anthropic orchestrators unittest ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by ops/anthropic unittest suite)"
    errors=$((errors + 1))
else
    _bg_rc=1
    if _bg_spawned anthropic_unittest; then
        _bg_join anthropic_unittest
    fi
    if [ "$_bg_rc" -ne 0 ]; then
        echo "  FAIL: ops/anthropic unittest failed (re-run: python3 -m unittest discover -s ops/anthropic -p 'test_*.py' -t ops/anthropic -v)"
        errors=$((errors + 1))
    else
        echo "  ok: ops/anthropic unittest suite (tier plan + oauth priority rebalance)"
    fi
fi

# Servable-model allowlist generator (ops/pricing). The deterministic glue
# (candidate split, dated-snapshot de-dup, Go map splice) has a stdlib-only
# selftest; the Go allowlist must keep its splice markers so the generator can
# always rewrite it. Same "no soft rule without a check" discipline as the ops
# SQL-generator gate.
echo ""
echo "=== sub2api: servable-allowlist generator selftest ==="
_servable_go="backend/internal/service/pricing_catalog_supported_models_tk.go"
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by servable-allowlist selftest)"
    errors=$((errors + 1))
elif ! python3 ops/pricing/refresh-servable-allowlist.py selftest >/dev/null 2>&1; then
    echo "  FAIL: ops/pricing/refresh-servable-allowlist.py selftest failed (re-run: python3 ops/pricing/refresh-servable-allowlist.py selftest)"
    errors=$((errors + 1))
else
    _markers_ok=1
    for _m in "servable-allowlist:begin anthropic" "servable-allowlist:end anthropic" \
        "servable-allowlist:begin openai" "servable-allowlist:end openai" \
        "servable-allowlist:begin gemini" "servable-allowlist:end gemini"; do
        if ! grep -qF "$_m" "$_servable_go"; then
            echo "  FAIL: splice marker missing in $_servable_go: $_m"
            _markers_ok=0
        fi
    done
    if [ "$_markers_ok" -eq 1 ]; then
        echo "  ok: servable-allowlist generator selftest + Go splice markers intact"
    else
        errors=$((errors + 1))
    fi
fi

# Determinism-baseline observability/release/stage0 helpers (added 2026-05 to
# back the SKILL.md mechanization migration per dev-rules §"skill / command
# 确定性基线"). Same pattern as the ops/anthropic suite: stdlib-only unittest,
# no AWS/network; the directories are listed individually because each ships
# its own importlib-loaded scripts (filenames contain hyphens).
echo ""
echo "=== sub2api: determinism-baseline helper unittests ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by determinism-baseline suites)"
    errors=$((errors + 1))
else
    # When preflight runs inside a git pre-commit hook, git injects GIT_DIR /
    # GIT_INDEX_FILE / GIT_WORK_TREE into the environment. The Python tests
    # spin up tmpdir-isolated git repos via subprocess.run(["git", ...], cwd=…),
    # and the cwd= argument is ignored when GIT_DIR is set — so every test sees
    # the host repo's index instead of its tmpdir and fails with non-zero
    # CalledProcessError. The background spawn strips these vars at the boundary
    # so the suites behave identically inside the hook and standalone.
    _det_baseline_failed=0
    for _det_dir in ops/observability ops/stage0 scripts deploy/aws/stage0 deploy/aws/lightsail; do
        _det_key="det_$(echo "$_det_dir" | tr '/' '_')"
        _bg_rc=1
        if _bg_spawned "$_det_key"; then
            _bg_join "$_det_key"
        fi
        if [ "$_bg_rc" -ne 0 ]; then
            echo "  FAIL: $_det_dir unittest failed (re-run: env -u GIT_DIR -u GIT_INDEX_FILE -u GIT_WORK_TREE python3 -m unittest discover -s $_det_dir -p 'test_*.py' -t $_det_dir -v)"
            errors=$((errors + 1))
            _det_baseline_failed=1
        fi
    done
    if [ "$_det_baseline_failed" -eq 0 ]; then
        echo "  ok: determinism-baseline suites (observability / stage0 / scripts / deploy.stage0)"
    fi
fi

# ---- sub2api: CloudFormation template version --------------------------------
# AWS only accepts a single literal value for AWSTemplateFormatVersion. A typo
# (e.g. 2010-10-09 vs the canonical 2010-09-09) parses as valid YAML, passes
# local lint, but surfaces only at `aws cloudformation deploy / validate-template`
# time with a misleading "is not a supported value" error. Real incident:
# cicd-oidc-lightsail-addon.yaml shipped with 2010-10-09 in PR #380 and bypassed
# two review rounds; the typo only blocked the actual migration setup days later.
echo ""
echo "=== sub2api: CloudFormation template version ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for CFN template version check)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/cfn-template-version.py; then
    errors=$((errors + 1))
fi

# ---- sub2api: Lightsail OIDC perm coverage -----------------------------------
# Discover-by-failure on AWS implicit permission contracts is the OPC
# anti-pattern that PRs #397/#398/#399 each fixed in isolation. This gate
# text-checks the addon + base OIDC policies against the action list the
# Lightsail edge workflow actually issues. Any drift (workflow gains a new
# `aws <service> <command>` call without a matching policy update) fails
# preflight before a workflow dispatch can hit AccessDenied at runtime.
echo ""
echo "=== sub2api: Lightsail OIDC perm coverage ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for OIDC perm coverage check)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/lightsail-oidc-perm-coverage.py --quiet; then
    errors=$((errors + 1))
fi

# ---- sub2api: edge platform exclusivity -------------------------------------
# EC2 Edge and Lightsail Edge intentionally share the same <edge_id> namespace,
# the same GitHub Environment edge-<id>, and the same DNS domain
# api-<id>.tokenkey.dev. AWS resources are fully namespaced (stack name, SSM
# prefix, Static IP name), so the two stacks can co-exist without colliding
# inside AWS. The single hard conflict is DNS: only one A record can point at
# one IP. If both matrices declare the same edge_id as deployable=true at the
# same time, operators get undefined behaviour (whichever stack DNS currently
# points at "wins"; the other silently runs as a phantom). The README warning
# "不要对同一 edge 混跑两种 provision" is now this mechanical gate.
echo ""
echo "=== sub2api: edge platform exclusivity (EC2 ↔ Lightsail) ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for edge platform exclusivity check)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/edge-platform-exclusivity.py; then
    # edge-platform-exclusivity.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: no edge_id is deployable=true on both EC2 and Lightsail"
fi

# ---- sub2api: ops SQL generator coverage ------------------------------------
# Gate B (static half). Every SQL-generating symbol in an ops module must be
# enumerated in that module's iter_self_check_sql() (so the real-Postgres
# execution test ops/anthropic/test_ops_sql_execute.py runs it) or exempted with
# a reason. Forces the PR #563 class (generated SQL that Postgres rejects but
# mocked/substring tests pass) into a real-parser gate fleet-wide.
echo ""
echo "=== sub2api: ops SQL generator coverage ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for ops SQL coverage check)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/ops-sql-coverage.py; then
    # ops-sql-coverage.py already printed the actionable failure.
    errors=$((errors + 1))
fi

# ---- sub2api: ops/deploy SQL soft-delete filter ------------------------------
# Hand-written operational SQL (psql in ops/ + deploy/ .sh/.py/.sql) bypasses Ent's
# soft-delete interceptor; a query over a soft-delete table (accounts/users/groups
# /...) that omits `deleted_at IS NULL` resurrects ghost rows — and soft-delete does
# NOT reset status/schedulable, so a deleted account still reads active+schedulable
# and has repeatedly misled operators. This gate flags any FROM/JOIN over a
# soft-delete table whose statement lacks a deleted_at filter; intentional all-rows
# queries (reaper/audit/restore/verify) opt out with an `ops-allow-soft-deleted`
# comment. Source: scripts/checks/ops-sql-soft-delete.py (+ --selftest).
echo ""
echo "=== sub2api: ops/deploy SQL soft-delete filter ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for ops/deploy SQL soft-delete check)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/ops-sql-soft-delete.py --selftest >/dev/null; then
    echo "  FAIL: ops-sql-soft-delete selftest failed (gate logic regression)"
    python3 ./scripts/checks/ops-sql-soft-delete.py --selftest
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/ops-sql-soft-delete.py --quiet; then
    # ops-sql-soft-delete.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: ops/ + deploy/ soft-delete-table queries all filter deleted_at (or marked intentional)"
fi

# ---- sub2api: ops tool orphan check -----------------------------------------
# Every tool under ops/ must be wired — referenced from a skill / workflow /
# preflight / sibling script / deploy asset / doc. An orphan (referenced
# nowhere) is dead weight the next operator/agent never discovers and re-hand-
# writes. A god-view audit (PR #663) found 7 such orphans and wired them; this
# gate stops new ones. Source: scripts/checks/ops-tool-orphan.py (+ --selftest).
echo ""
echo "=== sub2api: ops tool orphan check ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for ops tool orphan check)"
    errors=$((errors + 1))
else
    if ! python3 ./scripts/checks/ops-tool-orphan.py; then
        # ops-tool-orphan.py already printed the actionable orphan list.
        errors=$((errors + 1))
    fi
    if ! python3 ./scripts/checks/ops-tool-orphan.py --selftest; then
        errors=$((errors + 1))
    fi
fi

# ---- sub2api: workflow edge coverage ----------------------------------------
# Gate C. Per-edge workflows carry hardcoded choice option lists (GitHub Actions
# cannot compute them dynamically); a new deployable edge in the matrices would
# silently become un-covered. Source: scripts/checks/workflow-edge-coverage.py
# + workflow-edge-coverage.json (registry + opt-outs).
echo ""
echo "=== sub2api: workflow edge coverage ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for workflow edge coverage check)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/workflow-edge-coverage.py; then
    # workflow-edge-coverage.py already printed the actionable failure.
    errors=$((errors + 1))
fi

# ---- sub2api: lightsail edge launch-script drift ----------------------------
# Source of truth: deploy/aws/lightsail/render-bootstrap.sh + the four Stage0
# inputs it embeds (docker-compose.yml, Caddyfile.edge, two ops shell scripts).
# render-bootstrap --check fails if the committed generated-launch-script.sh
# drifts from those sources, so an editor touching compose/Caddyfile cannot
# accidentally ship a Lightsail Edge instance running yesterday's bytes.
echo ""
echo "=== sub2api: lightsail edge launch-script drift ==="
if [ ! -x ./deploy/aws/lightsail/render-bootstrap.sh ]; then
    echo "  FAIL: deploy/aws/lightsail/render-bootstrap.sh missing or not executable"
    errors=$((errors + 1))
elif ! bash ./deploy/aws/lightsail/render-bootstrap.sh --check >/dev/null 2>&1; then
    echo "  FAIL: deploy/aws/lightsail/generated-launch-script.sh is missing or out of sync"
    echo "        — run: bash deploy/aws/lightsail/render-bootstrap.sh && git add"
    errors=$((errors + 1))
else
    echo "  ok: lightsail launch script in sync with Stage0 sources"
fi

# ---- sub2api: stage0 CFN base64 drift ---------------------------------------
# Source of truth: deploy/aws/stage0/build-cfn.sh + its inputs
# (docker-compose.yml, Caddyfile, Caddyfile.edge, tokenkey-qa-stale-cleanup.sh,
# tokenkey-prune-ghcr-app-tags.sh). build-cfn.sh --check fails if the
# embedded SSM blobs or thin UserData launcher drift. Also run
# python3 deploy/aws/stage0/test_build_cfn.py for the 16 KiB UserData gate.
# committed gzip|base64 segments in stage0-single-ec2.yaml and
# stage0-edge-ec2.yaml drift from those sources, so an editor touching
# compose/Caddyfile cannot accidentally ship a fresh EC2 stack running
# yesterday's bytes. Mirrors the lightsail launch-script drift gate above —
# deploy/aws/README.md §3.5 explicitly mandates this as the mechanical
# enforcement of "编辑后必须运行 build-cfn.sh".
echo ""
echo "=== sub2api: stage0 CFN base64 drift ==="
if [ ! -x ./deploy/aws/stage0/build-cfn.sh ]; then
    echo "  FAIL: deploy/aws/stage0/build-cfn.sh missing or not executable"
    errors=$((errors + 1))
elif ! bash ./deploy/aws/stage0/build-cfn.sh --check >/dev/null 2>&1; then
    echo "  FAIL: deploy/aws/cloudformation/stage0-single-ec2.yaml carries stale gzip|base64"
    echo "        — run: bash deploy/aws/stage0/build-cfn.sh && git add deploy/aws/cloudformation/"
    errors=$((errors + 1))
else
    echo "  ok: stage0 CFN gzip|base64 segments in sync with sources"
    if ! python3 ./deploy/aws/stage0/test_build_cfn.py >/dev/null 2>&1; then
        echo "  FAIL: stage0 EC2 UserData / bootstrap SSM size gate"
        echo "        — run: python3 deploy/aws/stage0/test_build_cfn.py"
        errors=$((errors + 1))
    else
        echo "  ok: stage0 EC2 UserData under 16 KiB (launcher + SSM bootstrap split)"
    fi
fi

# Headless agent stream redactor: scripts/agent/redact-stream.py sits between
# `claude -p` and `tee` in upstream-merge-agent-daily.yml / pr-repair-agent.yml
# /agent-draft-pr/action.yml, scrubbing secrets out of the agent's stdout
# before the bytes hit the artifact file. GitHub Actions live-log masking
# does NOT apply to bytes a step writes to disk via tee, so the artifact
# can leak secrets that the rendered log hides. Guard the redactor itself
# with a self-test so a bad refactor cannot silently disarm it.
echo ""
echo "=== sub2api: agent stream redactor self-test ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by redact-agent-stream.py)"
    errors=$((errors + 1))
else
    _bg_rc=1
    if _bg_spawned redactor_test; then
        _bg_join redactor_test
    fi
    if [ "$_bg_rc" -ne 0 ]; then
        echo "  FAIL: scripts/agent/redact-stream_test.sh failed (re-run for details)"
        errors=$((errors + 1))
    else
        echo "  ok: agent stream redactor self-test"
    fi
fi

echo ""
echo "=== sub2api: headless-agent composite sharing ==="
# The headless `claude -p` scaffold (CLI install + origin/main redactor staging +
# canonical thinking-env + claude->redact->tee) lives in ONE composite action so
# the four agent workflows can't re-grow divergent copies (they had already
# drifted: ops-daily-diagnostics ran MAX_THINKING_TOKENS=16000 + omitted
# set -o pipefail). Fail closed if any agent workflow re-inlines `claude -p` or
# stops using the action, or if the composite loses its single-source
# thinking-env / pipefail / fail-closed redactor.
_hac_action=".github/actions/run-headless-agent/action.yml"
_hac_files=".github/workflows/pr-repair-agent.yml .github/workflows/upstream-issue-watchdog.yml .github/workflows/upstream-merge-agent-daily.yml .github/workflows/ops-daily-diagnostics.yml"
_hac_ok=1
if [ ! -f "$_hac_action" ]; then
    echo "  FAIL: $_hac_action missing (the shared headless-agent scaffold)"
    errors=$((errors + 1)); _hac_ok=0
else
    grep -q 'MAX_THINKING_TOKENS: "31999"' "$_hac_action" || {
        echo "  FAIL: $_hac_action lost the canonical MAX_THINKING_TOKENS=31999 single source"
        errors=$((errors + 1)); _hac_ok=0; }
    grep -q 'set -o pipefail' "$_hac_action" || {
        echo "  FAIL: $_hac_action lost set -o pipefail (claude exit code would be masked by tee)"
        errors=$((errors + 1)); _hac_ok=0; }
    grep -q 'refusing to run the agent unredacted' "$_hac_action" || {
        echo "  FAIL: $_hac_action redactor lost its fail-closed refusal"
        errors=$((errors + 1)); _hac_ok=0; }
    if grep -q 'for line in sys.stdin' "$_hac_action"; then
        echo "  FAIL: $_hac_action redactor reintroduced a passthrough fallback (must stay fail-closed)"
        errors=$((errors + 1)); _hac_ok=0
    fi
fi
for _f in $_hac_files; do
    [ -f "$_f" ] || continue
    grep -q 'run-headless-agent' "$_f" || {
        echo "  FAIL: $_f must run the agent via ./.github/actions/run-headless-agent"
        errors=$((errors + 1)); _hac_ok=0; }
    if grep -q 'claude -p' "$_f"; then
        echo "  FAIL: $_f re-inlines 'claude -p'; use the run-headless-agent action instead"
        errors=$((errors + 1)); _hac_ok=0
    fi
done
[ "$_hac_ok" = 1 ] && echo "  ok: agent workflows share the run-headless-agent composite (single-source scaffold)"
unset _hac_action _hac_files _hac_ok _f

echo ""
echo "=== sub2api: skip-ci marker (local commits) ==="
# Local pre-catch of the §9.2 bracketed [skip ci] / [ci skip] marker in this
# branch's own commits, using the SAME matcher the two PR-gate workflows call
# (scripts/checks/skip-ci-marker.py). PR title/body are only visible to the
# workflows, so locally we scan commit messages only.
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by skip-ci-marker.py)"
    errors=$((errors + 1))
else
    _skipci_base="${PREFLIGHT_BASE:-origin/main}"
    python3 ./scripts/checks/skip-ci-marker.py --commits-range "${_skipci_base}..HEAD" --quiet
    _skipci_rc=$?
    if [ "$_skipci_rc" -eq 1 ]; then
        errors=$((errors + 1))
    elif [ "$_skipci_rc" -eq 2 ]; then
        echo "  skip: cannot resolve ${_skipci_base}..HEAD (fetch origin/main); the PR gate enforces this"
    else
        echo "  ok: no bracketed [skip ci] / [ci skip] in ${_skipci_base}..HEAD commits"
    fi
    unset _skipci_base _skipci_rc
fi

echo ""
echo "=== sub2api: fix-ledger consistency (Upstream-Fixes / Anthropic-Fixes) ==="
# Trailer-scoped drift gate for the issue-watchdog ledgers. When a commit in
# this branch declares it closes an upstream/anthropic issue via an
# `Upstream-Fixes:` / `Anthropic-Fixes:` trailer, require that the matching
# fact-check entry exists, its anchors resolve, and fixes/triage already
# reflect it (author ran `apply-fix-ledger.py --apply`). Unrelated PRs that do
# not carry the trailer are not gated — pre-existing cosmetic drift / anchor
# rot in OTHER ledger entries stays the daily watchdog's job. Same script runs
# in --apply mode to do the propagation, so固化 lands inside the fix PR rather
# than waiting for the next daily *-issue-watchdog.yml run.
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for apply-fix-ledger.py)"
    errors=$((errors + 1))
elif ! python3 ./scripts/upstream/apply-fix-ledger.py --selftest >/dev/null; then
    echo "  FAIL: apply-fix-ledger.py self-test failed"
    errors=$((errors + 1))
else
    _fl_base="${PREFLIGHT_BASE:-origin/main}"
    for _fl_ledger in upstream anthropic; do
        python3 ./scripts/upstream/apply-fix-ledger.py --ledger "$_fl_ledger" \
            --check --commits-range "${_fl_base}..HEAD" --quiet
        _fl_rc=$?
        if [ "$_fl_rc" -eq 1 ]; then
            # apply-fix-ledger.py already printed the actionable problem list.
            errors=$((errors + 1))
        elif [ "$_fl_rc" -eq 2 ]; then
            echo "  skip[$_fl_ledger]: cannot resolve ${_fl_base}..HEAD (fetch origin/main); the PR gate enforces this"
        else
            echo "  ok[$_fl_ledger]: declared fixes have consistent fact-checks"
        fi
    done
    unset _fl_base _fl_ledger _fl_rc
fi

echo ""
echo "=== sub2api: merge-gate sentinel parity ==="
# Keeps upstream-merge-pr-shape.yml checks 4-13 and preflight's sentinel set
# mechanically coupled (manifest: scripts/sentinels/merge-gate-parity.json) so a
# new merge-gate sentinel can't be wired into one file but not the other.
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by merge-gate-parity.py)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/merge-gate-parity.py --quiet; then
    errors=$((errors + 1))
else
    echo "  ok: merge-gate sentinels in sync (preflight <-> upstream-merge-pr-shape)"
fi

echo ""
echo "=== sub2api: release/warm Go cache key parity ==="
# Keeps the cross-arch Go build-cache contract between backend-ci.yml's
# `warm-release-cache` job (SAVES on main) and release.yml (RESTORE-ONLY on the
# tag ref) mechanically coupled: identical key prefix + correct save/restore
# directionality. Drift silently reverts the release speed-up to a cold arm64
# compile with no error (the exact class that went unnoticed before PR #576).
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by release-cache-key-parity.py)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/release-cache-key-parity.py --quiet; then
    errors=$((errors + 1))
else
    echo "  ok: release/warm cache key prefix + directionality in sync"
fi

echo ""
if [ "$errors" -eq 0 ]; then
    echo "=== preflight (with sub2api checks): PASS ==="
    exit 0
else
    echo "=== preflight (with sub2api checks): FAIL ($errors check(s) failed) ==="
    exit 1
fi
