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
#   Node version alignment       — CI frontend jobs must setup-node the same
#        major as the release Dockerfile (ARG NODE_IMAGE). Driven by
#        `scripts/checks/node-version-align.py`.
#   platform registry drift      — Go ↔ TS platform registry lockstep
#        (OpenAI-compat list, dispatch-config platforms, Platform universe,
#        Ent platform enum coverage, admin UI style maps). Driven by
#        `scripts/checks/platform-registry-drift.py`.
#   upstream deletion ledger     — every upstream-owned file deleted in TK must
#        appear verbatim in `docs/DEPRECATIONS.md` (CLAUDE.md §5.x). Driven by
#        `scripts/checks/upstream-deletion-ledger.py`. Skips when upstream remote
#        absent (CI without `upstream` fetch).
#   upstream conflict surface (advisory) — locally advisory (warn, never block);
#        CI blocks via `.github/workflows/upstream-conflict-surface.yml`. Reports
#        whether HEAD introduces new conflict files vs upstream/main. Skips when
#        upstream remote absent. `scripts/checks/upstream-conflict-surface.sh`.
#   upstream insertion invasiveness (advisory) — classifies hunk-level
#        invasiveness of upstream-shaped Go file changes. Never blocks. Skips when
#        upstream remote absent. `scripts/checks/upstream-insertion-invasiveness.py`.
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

_preflight_fast=0
case "${PREFLIGHT_FAST:-}" in 1|true|yes|TRUE|YES) _preflight_fast=1 ;; esac
if [ "${GITHUB_EVENT_NAME:-}" = "pull_request" ] && [ "${PREFLIGHT_FAST:-}" != "0" ]; then
    _preflight_fast=1
fi

# ---- TK: background-job helpers ----------------------------------------------
# The most expensive sub2api gates (QA go test, the unittest-discover suites,
# the dockerized Caddyfile adapt) are independent of every other section, so
# they are spawned in the background right after the dev-rules template returns
# and joined at their original section position. Wall-clock for the expensive
# block becomes max(job) instead of sum(job) while output/FAIL semantics stay
# byte-equivalent per section.
_bg_spawn() {  # _bg_spawn <key> <cmd...>
    local key="$1"; shift
    (
        set +e  # preflight-allow: swallow (rc is captured into <key>.rc and joined by section)
        "$@" >"$_preflight_bg_dir/$key.out" 2>&1
        printf '%s\n' "$?" >"$_preflight_bg_dir/$key.rc"
        exit 0
    ) &
    echo "$!" >"$_preflight_bg_dir/$key.pid"
}
_bg_join() {  # _bg_join <key> → sets _bg_rc to the captured exit code; output in $_preflight_bg_dir/<key>.out
    # Must run in the MAIN shell (never inside $(...)): `wait` can only reap
    # children of the shell that spawned them.
    local key="$1"
    wait "$(cat "$_preflight_bg_dir/$key.pid")" 2>/dev/null || true  # preflight-allow: swallow (use captured <key>.rc)
    if [ -f "$_preflight_bg_dir/$key.rc" ]; then
        _bg_rc="$(cat "$_preflight_bg_dir/$key.rc")"
    else
        _bg_rc=1
    fi
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

export PREFLIGHT_BASE="${template_base:-origin/main}"

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

# dev-rules template check 18a omits PREFLIGHT_BASE until submodule picks up the fix.
_dev_preflight_template="./dev-rules/templates/preflight.sh"
if ! grep -q 'check_deleted_file_refs.py --base' "$_dev_preflight_template" 2>/dev/null; then
    _dev_preflight_template="$(mktemp "${TMPDIR:-/tmp}/preflight-dev-rules.XXXXXX")"
    sed 's|check_deleted_file_refs\.py >|check_deleted_file_refs.py --base "${PREFLIGHT_BASE:-origin/main}" >|' \
        ./dev-rules/templates/preflight.sh > "$_dev_preflight_template"
    chmod +x "$_dev_preflight_template"
fi
PREFLIGHT_BASE="$template_base" PREFLIGHT_REPO_ROOT="$REPO_ROOT" "$_dev_preflight_template" "$@"
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
    backend/internal/service/openai_gateway_service.go \
    backend/internal/service/openai_ws_forwarder.go 2>/dev/null \
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

# ---- sub2api: perf query-shape sentinel registry ----------------------------
# Source of truth: scripts/sentinels/perf-query-shape.json. Guards
# performance-critical query SHAPES whose regression is semantically invisible
# (a revert returns identical results, so no test catches it; an EXPLAIN-plan
# test is unreliable on small fixtures since the planner seq-scans tiny tables
# regardless of shape). First entry: /admin/users GetLatestUsedAtByUserIDs must
# stay a per-user LATERAL index probe and not revert to the full-table
# `ANY($1) GROUP BY` seq scan (~1.3s on prod, 2.4M rows). See PR #877.
echo ""
echo "=== sub2api: perf query-shape sentinel registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read perf-query-shape.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-perf-query-shape.py --quiet; then
    # check-perf-query-shape.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: all perf query-shape sentinels intact"
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

# ---- sub2api: grok sentinel registry ----------------------------------------
# Source of truth: scripts/sentinels/grok.json. Verifies that every load-bearing
# surface of the seventh platform (`grok`, xAI / SuperGrok Heavy OAuth) is still
# present: PlatformGrok identity, the OpenAICompatPlatforms + AllSchedulingPlatforms
# membership, the pkg/xai OAuth refresh helper + GrokTokenRefresher, the two forward
# seams (grok OAuth forwards like the apikey branch, NOT the ChatGPT/Codex branch),
# the Heavy-403 honesty guard, and the pricing/enum rows. Same failure mode as the
# kiro/newapi guards: an upstream merge silently dropping a file or injection branch.
echo ""
echo "=== sub2api: grok sentinel registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read grok.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-grok.py --quiet; then
    # check-grok.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: all grok sentinels intact"
fi

# ---- sub2api: relay-invariant test registry ---------------------------------
# Source of truth: scripts/sentinels/relay-invariants.json. Unlike the per-
# platform sentinels above (which anchor PRODUCTION symbols against deletion),
# this registry anchors the CHARACTERIZATION TESTS that encode deliberate,
# TK-correct relay behaviors — G4 5h-window cooldown scoping, kiro/grok refresh
# candidates, SSE stream-error 502 (not 403), org-ban/bodyless/oauth401/usage-
# policy/TLS-fingerprint/request-owned-429 cooldown polarity, thinking model-
# reference routing. PR #835 showed the failure class: an upstream rewrite
# silently deletes a TK behavior OR overrides a deliberate TK choice with no
# conflict, no compile error, and a green CI — because the only proof of the
# choice was a test deleted in the same merge. Pinning each test's `func TestX(`
# definition turns "merge deleted/gutted the test" into a red check.
echo ""
echo "=== sub2api: relay-invariant test registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read relay-invariants.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-relay-invariants.py --quiet; then
    # check-relay-invariants.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: all relay-invariant tests intact"
fi

# ---- sub2api: antigravity fingerprint sentinel registry ---------------------
# Source of truth: scripts/sentinels/antigravity.json. Guards the TokenKey
# divergences in the Antigravity (cloudcode-pa) client fingerprint that an
# upstream merge would silently revert: the `/hub/` subclient segment in
# BuildUserAgent (oauth.go) and the gl-node X-Goog-Api-Client removal on the
# privacy calls (client.go). oauth.go/client.go are upstream-shaped, so a
# `git merge upstream/main` can clobber these with a trivial, test-passing diff
# — exactly the backward-drift this guard catches. Aligned to the real on-wire
# IDE 2.0.11 capture (2026-06-13); see docs/antigravity-fingerprint-changelog.md.
echo ""
echo "=== sub2api: antigravity fingerprint sentinel registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read antigravity.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-antigravity.py --quiet; then
    # check-antigravity.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: all antigravity fingerprint sentinels intact"
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

# ---- sub2api: catalog serving drift -----------------------------------------
# Source of truth: backend/internal/service/tk_served_models.json — the THIN intent
# manifest ("TK serves model M on platform P via an account credentials.model_mapping
# whitelist, at price π, display=yes/no") that must AGREE with (1) an explicit
# model_mapping write path (modelops activation for new floors; legacy migration/admin
# evidence remains supported), (2) tk_pricing_overlay.json, and (3) the Go servable-
# allowlist maps in pricing_catalog_supported_models_tk.go. Guards the
# #812-class regression where a model is priced + advertised-as-intended but never
# wired onto the serving account's model_mapping (=> empty pool 429/503). Selftest
# first (offline fixtures), then the real cross-file agreement check. CLAUDE.md
# §「升级原则」: a soft rule that bit us once becomes a mechanical gate.
echo ""
echo "=== sub2api: catalog serving drift ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to validate tk_served_models.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/catalog-serving-drift.py --selftest >/dev/null 2>&1; then
    echo "  FAIL: catalog-serving-drift.py selftest"
    echo "        — run: python3 scripts/checks/catalog-serving-drift.py --selftest"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/catalog-serving-drift.py --quiet; then
    # catalog-serving-drift.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: served-models manifest agrees with price/display/mapping declaration"
fi

# ---- sub2api: Studio media coverage -----------------------------------------
# Source of truth: backend media membership is catalog-driven (pricing overlay +
# Go servable allowlists + newapi served-models manifest). Studio presentation is
# allowed to be friendly, but every public servable image/video must have explicit
# frontend metadata (image size/aspect contract or video durations); otherwise the
# UI falls back to unsafe defaults and reopens the "catalog says usable, submit
# fails" class.
echo ""
echo "=== sub2api: Studio media coverage ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for Studio media coverage)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/studio-media-coverage.py --selftest >/dev/null 2>&1; then
    echo "  FAIL: studio-media-coverage.py selftest"
    echo "        — run: python3 scripts/checks/studio-media-coverage.py --selftest"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/studio-media-coverage.py --quiet; then
    errors=$((errors + 1))
else
    echo "  ok: Studio presentation covers public servable media models"
fi

# ---- sub2api: modelops plan/activation selftest ------------------------------
# plan remains read-only. activate validates bundle deltas and independent,
# digest-bound, fresh probe/pricing evidence before it can reach the explicit
# prod-only mapping apply. Keep both classifier and activation gates offline-
# tested so bad evidence never reaches SSM.
echo ""
echo "=== sub2api: modelops plan/activation selftest ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for modelops plan/activation)"
    errors=$((errors + 1))
elif ! python3 ./ops/pricing/modelops.py --selftest >/dev/null 2>&1; then
    echo "  FAIL: modelops plan/activation selftest"
    echo "        — run: python3 ops/pricing/modelops.py --selftest"
    errors=$((errors + 1))
elif ! python3 -m unittest ops/pricing/test_model_activation.py >/dev/null 2>&1; then
    echo "  FAIL: model activation behavior tests"
    echo "        — run: python3 -m unittest ops/pricing/test_model_activation.py"
    errors=$((errors + 1))
else
    echo "  ok: modelops plan/activation selftest + behavior tests"
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

# ---- sub2api: overlay-runtime hot-push tool selftest ------------------------
# ops/pricing/manage-overlay-runtime.py hot-pushes tk_pricing_overlay.json to the
# prod runtime (settings) so a model can be priced + surfaced without a release.
# Its selftest covers the pure drift logic (pending/shadow/orphan) offline — run
# it so a refactor of the drift rules fails preflight, not the operator.
echo ""
echo "=== sub2api: overlay-runtime hot-push tool selftest ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for overlay-runtime selftest)"
    errors=$((errors + 1))
elif ! python3 ./ops/pricing/manage-overlay-runtime.py --selftest >/dev/null 2>&1; then
    echo "  FAIL: overlay-runtime hot-push tool selftest"
    echo "        — run: python3 ops/pricing/manage-overlay-runtime.py --selftest"
    errors=$((errors + 1))
else
    echo "  ok: overlay-runtime hot-push tool selftest"
fi

# ---- sub2api: account model_mapping tool/SSOT contract selftest -------------
# Keep the pure Go-floor/policy-metadata parsing and diff/render logic under
# preflight. This validates the modelops tool contract independently of whether
# a caller uses check-accounts or an explicit model-activation precheck.
echo ""
echo "=== sub2api: account model_mapping tool/SSOT contract selftest ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for account model_mapping tool/SSOT contract selftest)"
    errors=$((errors + 1))
elif ! python3 ./ops/pricing/manage-account-model-mapping-runtime.py --selftest >/dev/null 2>&1; then
    echo "  FAIL: account model_mapping tool/SSOT contract selftest"
    echo "        — run: python3 ops/pricing/manage-account-model-mapping-runtime.py --selftest"
    errors=$((errors + 1))
else
    echo "  ok: account model_mapping tool/SSOT contract selftest"
fi

# ---- sub2api: generated model-surface bundle drift -------------------------
# Rollout consumes the generated bundle without compiling Go. Keep the checked-in
# artifact byte-identical to the Go owner so it cannot become a second hand-edited
# model list.
echo ""
echo "=== sub2api: generated model-surface bundle drift ==="
if ! command -v go >/dev/null 2>&1; then
    echo "  FAIL: go not on PATH (required for model-surface bundle drift check)"
    errors=$((errors + 1))
elif ! (cd backend && go run ./cmd/account-model-mapping bundle --check ../ops/pricing/model-surface-bundle.json); then
    echo "  FAIL: ops/pricing/model-surface-bundle.json drifted from the Go owner"
    echo "        — run: cd backend && go run ./cmd/account-model-mapping bundle --output ../ops/pricing/model-surface-bundle.json"
    errors=$((errors + 1))
else
    echo "  ok: model-surface bundle matches the Go owner"
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

# ---- sub2api: handler DI/Wire sentinel registry ----------------------------
# Source of truth: scripts/sentinels/handler-di-wire.json. Verifies that TK
# handler struct fields (in handler.go) and Wire provider functions (in wire.go)
# are still present. These are the sole compile-time link between TK handler
# implementations and the app — dropping them compiles cleanly but silently
# deregisters entire TK surfaces (admin tiers, edge accounts, pricing catalog,
# trial provisioning, compliance gate).
echo ""
echo "=== sub2api: handler DI/Wire sentinel registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read handler-di-wire.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-handler-di-wire.py --quiet; then
    # check-handler-di-wire.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: all handler DI/Wire sentinels intact"
fi

# ---- sub2api: priced-serving-gate sentinel registry -------------------------
# Source of truth: scripts/sentinels/priced-serving-gate.json. Pins the runtime
# 'priced-or-it-doesnt-ship' serving-admission gate (docs/approved/priced-or-it-doesnt-ship.md,
# issue #1016 v1): the gate companion + R3-consistency predicate, the DI wiring,
# every per-route hook (5 forwarders), the setting constant, and the enabled-set
# getter. Each per-route touch is one guarded call + early return that compiles
# clean if dropped, so an upstream merge can silently revert it and re-open the
# native catch-all $0-billing hole the gate exists to close.
echo ""
echo "=== sub2api: priced-serving-gate sentinel registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read priced-serving-gate.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-priced-serving-gate.py --quiet; then
    # check-priced-serving-gate.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: all priced-serving-gate sentinels intact"
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

# ---- sub2api: anthropic prompt surface registry ---------------------------
echo ""
echo "=== sub2api: anthropic prompt surface registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for prompt surface registry check)"
    errors=$((errors + 1))
elif ! python3 ./ops/anthropic/probe_prompt_surfaces.py --check-registry >/dev/null; then
    errors=$((errors + 1))
elif [ "$_preflight_fast" = "1" ]; then
    echo "  ok: prompt surface registry (fixture gateway runs in CI test-unit)"
elif [ "${PREFLIGHT_SKIP_PROMPT_FIXTURE_GATEWAY:-}" = "1" ]; then
    echo "  ok: prompt surface registry (fixture gateway deduped to CI test-unit)"
elif ! python3 ./ops/anthropic/probe_prompt_surfaces.py --check-fixture-gateway >/dev/null; then
    errors=$((errors + 1))
else
    echo "  ok: prompt surface registry + fixture gateway coverage"
fi

# ---- sub2api: cc geo-stego static anchors ---------------------------------
echo ""
echo "=== sub2api: cc geo-stego static anchors ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for cc geo-stego static check)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/check-cc-geo-stego-static.py --selftest >/dev/null; then
    echo "  FAIL: check-cc-geo-stego-static.py self-test failed"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/check-cc-geo-stego-static.py --quiet; then
    errors=$((errors + 1))
else
    echo "  ok: cc geo-stego static anchors + Go normalizer wiring"
fi

# ---- sub2api: prompt surface drift aggregate self-test --------------------
echo ""
echo "=== sub2api: prompt surface drift aggregate ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for prompt surface drift check)"
    errors=$((errors + 1))
elif ! bash -n ./ops/observability/check-prompt-surface-drift.sh; then
    echo "  FAIL: check-prompt-surface-drift.sh syntax"
    errors=$((errors + 1))
elif ! python3 -m unittest ops.observability.test_probe_prompt_surface_fingerprints -q; then
    echo "  FAIL: prompt surface fingerprint probe tests"
    errors=$((errors + 1))
elif ! python3 -m unittest ops.observability.test_open_prompt_surface_watch_issues -q; then
    echo "  FAIL: prompt surface watch issue helper tests"
    errors=$((errors + 1))
else
    echo "  ok: prompt surface drift tooling"
fi

# ---- sub2api: oauth mimic edge aggregate self-test -------------------------
echo ""
echo "=== sub2api: oauth mimic edge aggregate ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for oauth mimic aggregate check)"
    errors=$((errors + 1))
elif ! bash -n ./ops/observability/scan-oauth-mimic-chain.sh; then
    echo "  FAIL: scan-oauth-mimic-chain.sh syntax"
    errors=$((errors + 1))
elif ! bash -n ./ops/stage0/edge_anthropic_oauth_schedulable_probe.sh; then
    echo "  FAIL: edge_anthropic_oauth_schedulable_probe.sh syntax"
    errors=$((errors + 1))
elif ! python3 ./ops/observability/oauth_mimic_aggregate.py --selftest >/dev/null; then
    echo "  FAIL: oauth_mimic_aggregate.py selftest"
    errors=$((errors + 1))
elif ! python3 -m unittest ops.observability.test_scan_oauth_mimic_chain -q; then
    echo "  FAIL: scan oauth mimic chain tests"
    errors=$((errors + 1))
elif ! python3 -m unittest ops.observability.test_open_oauth_mimic_watch_issues -q; then
    echo "  FAIL: open oauth mimic watch issue tests"
    errors=$((errors + 1))
elif ! python3 -m unittest ops.observability.test_probe_oauth_mimicry_chain -q; then
    echo "  FAIL: probe oauth mimicry chain tests"
    errors=$((errors + 1))
else
    echo "  ok: oauth mimic edge watch tooling"
fi

# ---- sub2api: codex fingerprint pin consistency -----------------------------
# The Codex (OpenAI-platform) client version has one service source:
# DefaultOpenAICodexVersion. The UA default, gateway `version` header, and
# usage-probe `Version` must derive from it. This gate proves the service pins
# agree AMONG THEMSELVES — it never compares to a moving upstream codex release,
# so it cannot break CI when codex ships a new version. Real upstream drift is
# detected on-demand by skill tokenkey-codex-fingerprint-alignment.
echo ""
echo "=== sub2api: codex fingerprint pin consistency ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for codex pin consistency check)"
    errors=$((errors + 1))
elif ! python3 -m unittest discover -s ops/openai -p 'test_*.py' -t ops/openai >/dev/null 2>&1; then
    echo "  FAIL: ops/openai codex fingerprint engine self-test failed"
    errors=$((errors + 1))
elif ! python3 ./ops/openai/capture_codex_fingerprint.py check-consistency >/dev/null; then
    # On failure the engine prints the actionable per-pin report to stderr (stdout
    # is the success line, suppressed here); a single run surfaces it, no re-run.
    errors=$((errors + 1))
else
    echo "  ok: codex service version pins derive from one source"
fi

# ---- sub2api: client release watch -------------------------------------------
# Daily CI polls upstream client releases (Claude Code / cc-stainless / Codex /
# codex-vscode / Gemini CLI / Grok CLI / Antigravity / Kiro IDE / Kiro CLI) and
# opens tracking issues when upstream is ahead of TokenKey pins.
# The script's selftest + unit tests keep pin readers and semver logic honest.
echo ""
echo "=== sub2api: client release watch ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for client release watch check)"
    errors=$((errors + 1))
elif ! python3 ./scripts/fingerprint/client_release_watch.py --selftest --quiet; then
    echo "  FAIL: client_release_watch.py self-test failed"
    errors=$((errors + 1))
elif ! python3 -m unittest discover -s scripts/fingerprint -p 'test_*.py' -q; then
    echo "  FAIL: client release watch unit tests failed"
    errors=$((errors + 1))
else
    echo "  ok: client release watch engine self-test + unit tests"
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
# Check 12). Pure-insertion edits of EXISTING files / i18n changes are
# auto-accepted by the script; newly ADDED hotspot files (new TK companions /
# bridge files / TK frontend modules) are NOT — a new load-bearing surface
# must gain sentinel anchors or carry sentinel-registry-reviewed in the PR body.
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for sentinel registry update gate)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-registry-update-gate.py --selftest >/dev/null; then
    echo "  FAIL: check-registry-update-gate.py self-test failed"
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
elif ! MARKER_GATE_ADVISORY=1 PREFLIGHT_BASE="${PREFLIGHT_BASE:-origin/main}" \
    python3 ./scripts/checks/upstream-override-marker.py --base "${PREFLIGHT_BASE:-origin/main}" --quiet; then
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

# ---- sub2api: storefront SEO copy alignment ---------------------------------
echo ""
echo "=== sub2api: storefront SEO alignment ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to run storefront-seo-alignment check)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/storefront-seo-alignment.py --selftest >/dev/null; then
    echo "  FAIL: storefront-seo-alignment selftest failed"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/storefront-seo-alignment.py; then
    errors=$((errors + 1))
else
    echo "  ok: storefront SEO copy aligned across TS, Go prerender, and index.html"
fi

# ---- sub2api: admin persistent-shell layout invariant -----------------------
echo ""
echo "=== sub2api: admin persistent-shell layout ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to run admin-shell-layout check)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/admin-shell-layout.py --selftest >/dev/null; then
    echo "  FAIL: admin-shell-layout selftest failed"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/admin-shell-layout.py; then
    errors=$((errors + 1))
else
    echo "  ok: admin views rely on AdminShellView persistent shell (no per-view <AppLayout>)"
fi

# ---- sub2api: post-deploy smoke script (syntax only; no live HTTP) ----------
echo ""
echo "=== sub2api: post-deploy smoke script syntax ==="
_smoke_syntax_ok=true
for _smoke_script in \
  ./ops/stage0/smoke_env.sh \
  ./ops/stage0/load_smoke_github_env.sh \
  ./ops/stage0/smoke_lib.sh \
  ./ops/stage0/probe_account_model.sh \
  ./ops/stage0/probe_kiro_claude_models.sh \
  ./ops/stage0/edge_native_anthropic_smoke.sh \
  ./ops/stage0/post_deploy_smoke.sh \
  ./ops/stage0/edge_post_deploy_smoke.sh \
  ./ops/observability/probe-ssot-recent-success.sh \
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

# ---- sub2api: standalone host script syntax (tokenkey-*.sh) ------------------
# The tokenkey-*.sh host scripts (pgdump, qa-stale-cleanup, prune, disk-metrics)
# ship to prod/edge boxes either embedded in the CFN/Lightsail bootstrap or
# base64-pushed by the *_via_ssm.sh refresh primitives. In the base64 path the
# SSM host-parse guard below only sees the OPAQUE base64 blob, so a syntax error
# in the decoded script would slip every local gate and only surface at runtime
# on the box — and for the disk-full alert that means a SILENTLY broken safety
# net (the exact #778 failure mode). bash -n them as standalone files here.
# Glob (not an enumerated list) so a new tokenkey-*.sh is covered on day one.
echo ""
echo "=== sub2api: standalone host script syntax (tokenkey-*.sh) ==="
_host_syntax_ok=true
for _host_script in ./deploy/aws/stage0/tokenkey-*.sh ./deploy/aws/lightsail/tokenkey-*.sh; do
  [ -e "${_host_script}" ] || continue
  if ! bash -n "${_host_script}"; then
    echo "  FAIL: ${_host_script} has bash syntax errors"
    errors=$((errors + 1))
    _host_syntax_ok=false
  fi
done
if [[ "${_host_syntax_ok}" == "true" ]]; then
  echo "  ok: standalone tokenkey-*.sh host scripts parse"
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

# ---- sub2api: capacity-first safety contracts ------------------------------
echo ""
echo "=== sub2api: capacity-first safety contracts ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for capacity-first safety tests)"
    errors=$((errors + 1))
elif ! python3 ./ops/observability/test_data_layer_capacity_safety.py >/dev/null 2>&1; then
    echo "  FAIL: capacity probe/projection safety contracts"
    echo "        — run: python3 ops/observability/test_data_layer_capacity_safety.py"
    errors=$((errors + 1))
elif ! python3 ./ops/stage0/test_cfn_datavolume_no_replace.py >/dev/null 2>&1; then
    echo "  FAIL: DataVolume no-replace planning contracts"
    echo "        — run: python3 ops/stage0/test_cfn_datavolume_no_replace.py"
    errors=$((errors + 1))
else
    echo "  ok: dormant bounded probe + explicit offline projection + grow-only CFN plan"
fi

# ---- sub2api: runtime resource config verdict selftest ---------------------
echo ""
echo "=== sub2api: runtime resource config verdict selftest ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for runtime resource config selftest)"
    errors=$((errors + 1))
elif ! python3 ./ops/observability/runtime_resource_config_verdict.py --selftest >/dev/null 2>&1; then
    echo "  FAIL: runtime resource config verdict selftest"
    echo "        — run: python3 ops/observability/runtime_resource_config_verdict.py --selftest"
    errors=$((errors + 1))
else
    echo "  ok: bounded Docker logging and Redis AOF drift fixtures pass"
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
# The edge-health alert decision logic (actionable-set + state-diff dedup) lives in
# edge-health-alert.py for manual triage via scan-edge-health.sh. Scheduled
# edge-health-watch workflow was retired (2026-07): prod pool-exhaust Feishu + daily
# client-fidelity-watch cover user-visible failures; intermediate edge posture churn
# is intentionally manual. Fixtures pin the 2026-06-07 incident shapes + dedup behavior.
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

# live-host state drift verdict (ops/stage0/live_host_state_verdict.py): the logic
# half of assert-live-host-state.sh (the read-only SSM probe wired into
# deploy-stage0.yml post-deploy + ops-daily-diagnostics.yml). Fixtures pin the
# drift cases that have bitten — a silent image-tag rollback and a missing
# deploy_via_ssm-injected env key (the 2026-06 "3× repeat") — so a logic
# regression fails preflight instead of going silent. Read-only, no AWS.
echo ""
echo "=== sub2api: live-host state verdict selftest ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for live-host state verdict selftest)"
    errors=$((errors + 1))
elif ! python3 ./ops/stage0/live_host_state_verdict.py --selftest >/dev/null 2>&1; then
    echo "  FAIL: live-host state verdict selftest"
    echo "        — run: python3 ops/stage0/live_host_state_verdict.py --selftest"
    errors=$((errors + 1))
else
    echo "  ok: live-host state verdict drift fixtures pass"
fi

# live-host detector ↔ deploy injectors sync: the detector asserts the CONTAINER
# env that the Stage0 deploy primitives inject/map is present on the host. If a
# key is added/removed in a deploy primitive without updating the detector's
# DEFAULT_REQUIRED_ENV (or vice versa), the detector would silently check stale
# keys — the exact "靠自觉 drift" this whole PR exists to kill. The detector is
# the single source of truth (--print-required); assert each key it requires
# actually appears in every prod-capable injector script.
echo ""
echo "=== sub2api: live-host detector ↔ deploy injector env sync ==="
_lh_injectors="ops/stage0/deploy_via_ssm.sh ops/stage0/deploy_via_ssm_bluegreen.sh"
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for live-host detector sync check)"
    errors=$((errors + 1))
else
    for _lh_injector in ${_lh_injectors}; do
        if [ ! -f "${_lh_injector}" ]; then
            echo "  FAIL: ${_lh_injector} missing (live-host detector sync check)"
            errors=$((errors + 1))
            continue
        fi
        _lh_missing=""
        while IFS= read -r _lh_key; do
            [ -z "${_lh_key}" ] && continue
            grep -q "${_lh_key}" "${_lh_injector}" || _lh_missing="${_lh_missing} ${_lh_key}"
        done < <(python3 ./ops/stage0/live_host_state_verdict.py --print-required)
        if [ -n "${_lh_missing}" ]; then
            echo "  FAIL: detector requires env not injected/mapped by ${_lh_injector}:${_lh_missing}"
            echo "        — reconcile DEFAULT_REQUIRED_ENV (live_host_state_verdict.py) with the injection list in ${_lh_injector}"
            errors=$((errors + 1))
        else
            echo "  ok: every detector-required env key is injected/mapped by ${_lh_injector}"
        fi
    done
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

# ---- sub2api: GoReleaser Docker config is Graviton-safe + v2 ---------------
# Keeps release.yml on the faster dockers_v2 path. The legacy `dockers` +
# `docker_manifests` split pushes per-arch intermediate tags and then creates
# shared manifests afterward; it is extra registry work on the slowest release
# path. Default/full releases must also keep linux/arm64 because prod + Edge
# Stage0 hosts are Graviton.
echo ""
echo "=== sub2api: GoReleaser Docker config ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to parse GoReleaser configs)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/release-goreleaser-docker.py --quiet; then
    errors=$((errors + 1))
else
    echo "  ok: GoReleaser Docker config uses dockers_v2 with safe platforms"
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
elif [ "$_preflight_fast" = "1" ]; then
    echo "  skip: main-ancestry anchor (preflight-fast; main-ancestry-guard covers PRs with full history)"
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
# Lightsail-only. prod now uses the blue/green SSM primitive; Lightsail edge
# stays on the legacy single-app primitive because its bootstrap still uses the
# shared single-app compose. Neither workflow may inline compose deploy commands.
if ! grep -q 'ops/stage0/deploy_via_ssm_bluegreen.sh' .github/workflows/deploy-stage0.yml; then
    echo "  FAIL: deploy-stage0.yml must use ops/stage0/deploy_via_ssm_bluegreen.sh"
    errors=$((errors + 1))
elif ! grep -q 'ops/stage0/deploy_via_ssm.sh' .github/workflows/deploy-edge-lightsail-stage0.yml; then
    echo "  FAIL: deploy-edge-lightsail-stage0.yml must use ops/stage0/deploy_via_ssm.sh"
    errors=$((errors + 1))
elif grep -q 'docker compose --env-file .* up -d --no-deps tokenkey' .github/workflows/deploy-stage0.yml .github/workflows/deploy-edge-lightsail-stage0.yml; then
    echo "  FAIL: Stage0 workflows must not inline tokenkey SSM deploy commands; use the matching ops/stage0 deploy primitive"
    errors=$((errors + 1))
else
    echo "  ok: prod uses blue/green SSM primitive; Lightsail edge stays on single-app SSM primitive"
fi

echo ""
echo "=== sub2api: blue/green migration safety ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for blue/green migration safety)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/bluegreen-migration-safety.py --selftest >/dev/null; then
    echo "  FAIL: blue/green migration safety selftest failed"
    echo "        — run: python3 scripts/checks/bluegreen-migration-safety.py --selftest"
    errors=$((errors + 1))
else
    _bgm_base="${PREFLIGHT_BASE:-origin/main}"
    python3 ./scripts/checks/bluegreen-migration-safety.py --base "${_bgm_base}" --head HEAD --quiet
    _bgm_rc=$?
    if [ "$_bgm_rc" -eq 1 ]; then
        errors=$((errors + 1))
    elif [ "$_bgm_rc" -eq 2 ]; then
        echo "  skip: cannot resolve ${_bgm_base}..HEAD (fetch origin/main); the prod deploy workflow enforces this"
    else
        echo "  ok: changed SQL migrations are blue/green-safe"
    fi
    unset _bgm_base _bgm_rc
fi

echo ""
echo "=== sub2api: migration immutability ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for migration immutability)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/migration-immutability.py --selftest >/dev/null; then
    echo "  FAIL: migration immutability selftest failed"
    echo "        — run: python3 scripts/checks/migration-immutability.py --selftest"
    errors=$((errors + 1))
else
    _mig_base="${PREFLIGHT_BASE:-origin/main}"
    python3 ./scripts/checks/migration-immutability.py --base "${_mig_base}" --head HEAD --quiet
    _mig_rc=$?
    if [ "$_mig_rc" -eq 1 ]; then
        errors=$((errors + 1))
    elif [ "$_mig_rc" -eq 2 ]; then
        echo "  skip: cannot resolve ${_mig_base}..HEAD (fetch origin/main); release deploy will fail on checksum mismatch"
    else
        echo "  ok: no edits to already-shipped SQL migrations"
    fi
    unset _mig_base _mig_rc
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

# ---- sub2api: display-coverage gate (servable ⇒ displayable) ----------------
# Guards the #1030 / #1029 failure class: a model added to the Go servable
# allowlist with NO display price source. Prod's /pricing reads the upstream
# remote mirror ∪ tk_pricing_overlay.json; the bundled resources/model-pricing
# mirror is a SHADOWED, hand-maintained fallback prod never reads — so a model
# present only there renders a BLANK price (gpt-5.6 #1030; antigravity gemini-*
# #1029). Diff-scoped: only NEW allowlist entries must be overlay-priced (or carry
# `display-via-remote-verified`). Live backstop:
# ops/pricing/audit-display-coverage.py check --live. CLAUDE.md §「升级原则」.
echo ""
echo "=== sub2api: display-coverage gate ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by display-coverage-gate)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/display-coverage-gate.py selftest >/dev/null 2>&1; then
    echo "  FAIL: display-coverage-gate.py selftest (re-run: python3 scripts/checks/display-coverage-gate.py selftest)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/display-coverage-gate.py check --base "${PREFLIGHT_BASE:-origin/main}"; then
    # gate already printed the actionable failure.
    errors=$((errors + 1))
fi

# ---- sub2api: display-coverage audit selftest -------------------------------
# ops/pricing/audit-display-coverage.py is the LIVE close-out of any catalog
# change (run `check --live`, require 0 gaps) and a safe read-only periodic prod
# audit. CI only runs its offline selftest; the --live audit is an operator /
# scheduled action (no prod network in CI).
echo ""
echo "=== sub2api: display-coverage audit selftest ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by audit-display-coverage)"
    errors=$((errors + 1))
elif ! python3 ops/pricing/audit-display-coverage.py selftest >/dev/null 2>&1; then
    echo "  FAIL: audit-display-coverage.py selftest (re-run: python3 ops/pricing/audit-display-coverage.py selftest)"
    errors=$((errors + 1))
else
    echo "  ok: display-coverage audit selftest"
fi

# ---- sub2api: SSOT endpoint matrix selftest ---------------------------------
# ops/test/gateway_model_ssot_matrix.py derives endpoint probe rows from the
# public pricing projection instead of a hand-maintained model list. Its gate
# translates live probe verdicts into display actions without adding another
# catalog source. CI only runs its offline fixture selftest; live list/run/gate
# remain operator actions.
echo ""
echo "=== sub2api: SSOT endpoint matrix selftest ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by gateway_model_ssot_matrix.py)"
    errors=$((errors + 1))
elif ! python3 ops/test/gateway_model_ssot_matrix.py selftest >/dev/null 2>&1; then
    echo "  FAIL: gateway_model_ssot_matrix.py selftest (re-run: python3 ops/test/gateway_model_ssot_matrix.py selftest)"
    errors=$((errors + 1))
elif ! python3 -m unittest discover -s ops/test -p 'test_ssot_recent_success.py' -t ops/test -q >/dev/null 2>&1; then
    echo "  FAIL: test_ssot_recent_success.py (re-run: python3 -m unittest discover -s ops/test -p test_ssot_recent_success.py -t ops/test)"
    errors=$((errors + 1))
else
    echo "  ok: SSOT endpoint matrix selftest"
fi

# ---- sub2api: SSOT delta gate (structural; live in post-deploy closeout) ---
# Replaces the retired daily full SSOT matrix scan (account-ban risk). Structural
# SSOT stays in catalog-serving-drift + display-coverage-gate. PR/commit gates
# only derive the diff-scoped pending-live models; prod HTTP proof runs after
# deployment, when new mapping/catalog code is actually live.
echo ""
echo "=== sub2api: SSOT delta gate (structural) ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by ssot-delta-gate.py)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/ssot-delta-gate.py selftest >/dev/null 2>&1; then
    echo "  FAIL: ssot-delta-gate.py selftest (re-run: python3 scripts/checks/ssot-delta-gate.py selftest)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/ssot-delta-gate.py check --base "${PREFLIGHT_BASE:-origin/main}" --skip-live; then
    errors=$((errors + 1))
else
    echo "  ok: SSOT delta gate structural check (live proof runs post-deploy)"
fi

echo ""
echo "=== sub2api: endpoint-compat baseline freshness ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by check_endpoint_compat_baseline_freshness.py)"
    errors=$((errors + 1))
elif ! python3 scripts/check_endpoint_compat_baseline_freshness.py >/dev/null 2>&1; then
    echo "  FAIL: endpoint-compat baseline must mention backend/cmd/server/VERSION (re-run: python3 scripts/check_endpoint_compat_baseline_freshness.py)"
    errors=$((errors + 1))
else
    echo "  ok: endpoint-compat baseline freshness"
fi

# probe-servable-models.sh unconditionally sources its companion
# probe_reserved_resources.sh (reserved-only; no direct-key fallback). run-probe.sh
# only ships the companion to the remote host when the caller passes --with, so any
# invocation that delivers the probe script MUST also deliver the companion — else
# every family config_errors on the remote. Hard-wire the invariant (a new caller
# that forgets --with would otherwise fail silently only at live-probe time).
echo ""
echo "=== sub2api: probe-servable-models.sh --with companion guard ==="
_probe_flag="--script ops/pricing/probe-servable-models.sh"
_with_flag="--with ops/pricing/probe_reserved_resources.sh"
_probe_guard_err=0
_probe_guard_files=0
# NB: patterns start with "--", so they MUST be passed via `-e` (both GNU grep and
# ugrep otherwise parse them as options and silently match nothing — a vacuous pass).
# Restrict to source file types (--include): otherwise generated bytecode like
# ops/pricing/__pycache__/*.pyc embeds the flag strings and makes the count
# non-deterministic (and a stale .pyc could false-FAIL).
while IFS= read -r _f; do
    [ -n "$_f" ] || continue
    _probe_guard_files=$((_probe_guard_files + 1))
    _n_script="$( { grep -oF -e "$_probe_flag" "$_f" || true; } | wc -l | tr -d ' ')"
    _n_with="$( { grep -oF -e "$_with_flag" "$_f" || true; } | wc -l | tr -d ' ')"
    if [ "$_n_with" -lt "$_n_script" ]; then
        echo "  FAIL: $_f invokes probe-servable-models.sh ${_n_script}x but ships the companion only ${_n_with}x"
        echo "        every 'run-probe.sh $_probe_flag' must also pass '$_with_flag'"
        _probe_guard_err=1
    fi
done < <(grep -rlF --include='*.md' --include='*.py' --include='*.sh' -e "$_probe_flag" .cursor ops docs deploy 2>/dev/null || true)
if [ "$_probe_guard_err" -eq 0 ]; then
    echo "  ok: all $_probe_guard_files probe-servable-models.sh caller file(s) ship the companion via --with"
else
    errors=$((errors + 1))
fi

# Probe source pools must be anchored by group id, not mutable display names.
# Legacy PROBE_*_SOURCE_GROUP env vars remain available for one-off diagnostics,
# but their defaults must stay empty; otherwise an operator group rename becomes
# a false config_error again.
echo ""
echo "=== sub2api: probe source group-id defaults ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by probe source group-id defaults check)"
    errors=$((errors + 1))
elif ! python3 - <<'PY'
import re
from pathlib import Path

probe = Path("ops/pricing/probe-servable-models.sh").read_text(encoding="utf-8")
errors = []
expected_source_ids = {
    "openai": 2,
    "anthropic_edge": 1,
    "anthropic_mirror": 1,
    "gemini_vertex": 16,
    "dashscope": 18,
    "glm_dashscope": 18,
    "zhipu": 18,
    "volcengine": 5,
    "grok_edge": 4,
    "antigravity": 21,
}
fn = re.search(r'^probe_source_group_id\(\).*?^}', probe, flags=re.M | re.S)
if not fn:
    errors.append("probe-servable-models.sh must define probe_source_group_id() as the source group-id SSOT")
else:
    body = fn.group(0)
    for key, gid in expected_source_ids.items():
        if not re.search(rf'^\s*{re.escape(key)}\)\s*echo\s+{gid}\s*;;', body, flags=re.M):
            errors.append(f"probe_source_group_id {key} must map to group_id {gid}")

required_defaults = {
    "PROBE_OPENAI_SOURCE_GROUP_ID": "openai",
    "PROBE_ANTHROPIC_SOURCE_GROUP_ID": "anthropic_edge",
    "PROBE_ANTHROPIC_MIRROR_GROUP_ID": "anthropic_mirror",
    "PROBE_GEMINI_SOURCE_GROUP_ID": "gemini_vertex",
    "PROBE_DASHSCOPE_SOURCE_GROUP_ID": "dashscope",
    "PROBE_ZHIPU_SOURCE_GROUP_ID": "glm_dashscope",
    "PROBE_VOLCENGINE_SOURCE_GROUP_ID": "volcengine",
    "PROBE_GROK_SOURCE_GROUP_ID": "grok_edge",
    "PROBE_ANTIGRAVITY_SOURCE_GROUP_ID": "antigravity",
}
for name, key in required_defaults.items():
    pattern = rf'^{name}="\$\{{{name}:-\$\(\s*probe_source_group_id\s+{re.escape(key)}\s*\)\}}"'
    if not re.search(pattern, probe, flags=re.M):
        errors.append(f"{name} default must call probe_source_group_id {key}")

legacy_names = [
    "PROBE_OPENAI_SOURCE_GROUP",
    "PROBE_ANTHROPIC_SOURCE_GROUP",
    "PROBE_ANTHROPIC_MIRROR_GROUP",
    "PROBE_GEMINI_SOURCE_GROUP",
    "PROBE_DASHSCOPE_SOURCE_GROUP",
    "PROBE_ZHIPU_SOURCE_GROUP",
    "PROBE_VOLCENGINE_SOURCE_GROUP",
    "PROBE_GROK_SOURCE_GROUP",
    "PROBE_ANTIGRAVITY_SOURCE_GROUP",
]
for name in legacy_names:
    if not re.search(rf'^{name}="\$\{{{name}:-\}}"', probe, flags=re.M):
        errors.append(f"{name} default must stay empty; use {name}_ID for the default source pool")

probe_lib = Path("ops/pricing/probe_reserved_resources.sh").read_text(encoding="utf-8")
if "group_id_like)" not in probe_lib:
    errors.append("probe_reserved_resources.sh must support group_id_like for mirror sub-pool probes")

if errors:
    print("\n".join(f"  FAIL: {err}" for err in errors))
    raise SystemExit(1)
print("  ok: probe defaults are group-id anchored; legacy group-name defaults are empty")
PY
then
    errors=$((errors + 1))
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

# ---- sub2api: local aws/pyexpat helper selftest -----------------------------
# Guards the local macOS/Homebrew aws bootstrap helper that diagnoses the
# pyexpat/libexpat mismatch before ops probes ever hit prod. Only the helper's
# pure selftest belongs in preflight; the real workstation-health check remains
# operator-invoked so repo validation does not depend on each machine's current
# Homebrew state. Source: scripts/checks/check-local-aws-pyexpat.py (+ --selftest).
echo ""
echo "=== sub2api: local aws/pyexpat helper selftest ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for local aws/pyexpat helper selftest)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/check-local-aws-pyexpat.py --selftest >/dev/null; then
    echo "  FAIL: local aws/pyexpat helper selftest failed"
    echo "        — run: python3 scripts/checks/check-local-aws-pyexpat.py --selftest"
    errors=$((errors + 1))
else
    echo "  ok: local aws/pyexpat helper selftest"
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

# ---- sub2api: pnpm audit owner contract -------------------------------------
# Project installs stay on pnpm 9, while security audit uses a pinned pnpm 11
# owner because npm retired the quick-audit endpoint used by pnpm 9/10.
echo ""
echo "=== sub2api: pnpm audit owner contract ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by pnpm-audit-contract.py)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/pnpm-audit-contract.py --quiet; then
    errors=$((errors + 1))
else
    echo "  ok: frontend security workflows share the pinned bulk-advisory audit owner"
fi

# ---- sub2api: Node version alignment -----------------------------------------
# CI frontend jobs must build/test on the same Node major as the release
# Dockerfile (ARG NODE_IMAGE). Drift → CI validates on a different runtime
# than the shipped artifact. Gate: scripts/checks/node-version-align.py.
echo ""
echo "=== sub2api: Node version alignment ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by node-version-align.py)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/node-version-align.py --quiet; then
    errors=$((errors + 1))
else
    echo "  ok: CI setup-node majors match Dockerfile NODE_IMAGE"
fi

# ---- sub2api: platform registry drift ----------------------------------------
# Go ↔ TS platform registry lockstep: OpenAI-compat list, dispatch-config
# platforms, Platform constant universe, Ent enum coverage, and admin UI style
# maps. Drift → admin UI cannot render, cannot persist enum-constrained
# platforms, or silently drops dispatch config. Gate: scripts/checks/platform-registry-drift.py.
echo ""
echo "=== sub2api: platform registry drift ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by platform-registry-drift.py)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/test_platform_registry_drift.py >/dev/null; then
    echo "  FAIL: platform-registry-drift regression tests failed"
    echo "        — run: python3 scripts/checks/test_platform_registry_drift.py"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/platform-registry-drift.py --quiet; then
    errors=$((errors + 1))
else
    echo "  ok: Go ↔ TS platform registries in lockstep"
fi

# ---- sub2api: Wire DI staleness -----------------------------------------------
# wire_gen.go is generated by `go generate ./cmd/server` (Google Wire). If a
# developer changes wire providers but forgets to regenerate, the stale file
# still compiles but silently injects wrong dependencies at runtime.
# Strategy: check if the CURRENT BRANCH (diff vs base) modified any wire.go
# input without also touching wire_gen.go. This avoids false positives from
# cosmetic changes on main (e.g. blank line removal in #1216) and is reliable
# in CI where filesystem mtimes are set at checkout time.
echo ""
echo "=== sub2api: Wire DI staleness ==="
_wire_gen="backend/cmd/server/wire_gen.go"
if [ ! -f "$_wire_gen" ]; then
    echo "  FAIL: $_wire_gen does not exist — run 'go generate ./cmd/server' in backend/"
    errors=$((errors + 1))
elif ! command -v git >/dev/null 2>&1; then
    echo "  skip: git not on PATH"
else
    _wire_base="${PREFLIGHT_BASE:-origin/main}"
    _wire_inputs_changed=0
    _wire_gen_changed=0
    _wire_changed_file=""
    for _wf in $(git diff --name-only "$_wire_base"...HEAD -- 'backend/**/wire.go' 2>/dev/null); do
        _wire_inputs_changed=1
        _wire_changed_file="$_wf"
    done
    if git diff --name-only "$_wire_base"...HEAD -- "$_wire_gen" 2>/dev/null | grep -q .; then
        _wire_gen_changed=1
    fi
    if [ "$_wire_inputs_changed" -eq 1 ] && [ "$_wire_gen_changed" -eq 0 ]; then
        echo "  FAIL: wire.go input changed ($_wire_changed_file) but wire_gen.go was not regenerated. Run 'go generate ./cmd/server' in backend/"
        errors=$((errors + 1))
    else
        echo "  ok: wire_gen.go is up to date (no input files newer)"
    fi
fi

# ---- sub2api: Ent generation staleness ----------------------------------------
# Ent ORM code under backend/ent/ is generated from backend/ent/schema/. If a
# developer changes a schema but forgets `go generate ./ent`, stale generated
# code may ship with wrong fields, edges, or predicates. This check regenerates
# and diffs to catch drift. Non-destructive: restores the directory after.
echo ""
echo "=== sub2api: Ent generation staleness ==="
_ent_schema_changed=0
if has_merge_base_with_head "${PREFLIGHT_BASE:-origin/main}" && \
   git diff --name-only "${PREFLIGHT_BASE:-origin/main}...HEAD" 2>/dev/null | grep -q '^backend/ent/schema/'; then
    _ent_schema_changed=1
fi
if ! command -v go >/dev/null 2>&1; then
    echo "  skip: go not on PATH"
elif [ "$_preflight_fast" = "1" ] && [ "$_ent_schema_changed" = "0" ]; then
    echo "  skip: Ent generation staleness (preflight-fast; no backend/ent/schema changes)"
else
    _ent_rc=0
    ( cd backend && go generate ./ent ) >/dev/null 2>&1 || _ent_rc=$?
    if [ "$_ent_rc" -ne 0 ]; then
        echo "  FAIL: go generate ./ent failed (exit $_ent_rc)"
        errors=$((errors + 1))
    elif ! git diff --exit-code backend/ent/ >/dev/null 2>&1; then
        echo "  FAIL: ent generated code is stale — run 'go generate ./ent' in backend/"
        errors=$((errors + 1))
    else
        echo "  ok: ent generated code is up to date"
    fi
    git checkout -- backend/ent/ 2>/dev/null || true
fi

# ---- sub2api: go.mod replace path validation ---------------------------------
# The replace directive in backend/go.mod points to ../../new-api (relative to
# backend/). If the sibling clone is missing or the worktree symlink is broken,
# the build will fail with a confusing error. Catch it early.
echo ""
echo "=== sub2api: go.mod replace path validation ==="
_replace_path=$(grep 'QuantumNous/new-api =>' backend/go.mod 2>/dev/null | awk '{print $NF}')
if [ -z "$_replace_path" ]; then
    echo "  skip: no QuantumNous/new-api replace directive found in backend/go.mod"
else
    _resolved_path="backend/$_replace_path"
    if [ -d "$_resolved_path" ] && [ -f "$_resolved_path/go.mod" ]; then
        echo "  ok: replace path resolves to $_resolved_path (go.mod present)"
    else
        echo "  FAIL: replace path '$_replace_path' (resolved: $_resolved_path) does not point to a directory with go.mod"
        echo "        — run: bash scripts/upstream/sync-new-api.sh"
        errors=$((errors + 1))
    fi
fi

# ---- sub2api: upstream deletion ledger ---------------------------------------
# Every upstream-owned file deleted in TK must be documented in
# docs/DEPRECATIONS.md (CLAUDE.md §5.x). Skips gracefully when the upstream
# remote is absent. Gate: scripts/checks/upstream-deletion-ledger.py.
echo ""
echo "=== sub2api: upstream deletion ledger ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required by upstream-deletion-ledger.py)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/upstream-deletion-ledger.py --quiet; then
    errors=$((errors + 1))
else
    echo "  ok: upstream deletions documented in DEPRECATIONS.md"
fi

# ---- sub2api: upstream conflict surface (conditional) -----------------------
# Requires `upstream` remote — skips gracefully when absent (plain clones,
# worktrees without upstream fetch). When present, reports whether HEAD
# introduces NEW conflict files against upstream/main vs the PR base.
# CI runs this as a blocking gate (upstream-conflict-surface.yml); here it is
# advisory (warn only) to keep local preflight fast and non-blocking.
echo ""
echo "=== sub2api: upstream conflict surface (advisory) ==="
if ! git remote get-url upstream >/dev/null 2>&1; then
    echo "  skip: no upstream remote (clone without upstream fetch)"
elif ! git rev-parse --verify --quiet upstream/main >/dev/null 2>&1; then
    echo "  skip: upstream/main not fetched"
else
    _base="${PREFLIGHT_BASE:-origin/main}"
    _base_sha=$(git rev-parse --verify --quiet "${_base}^{commit}" 2>/dev/null) || _base_sha=""
    _head_sha=$(git rev-parse --verify --quiet "HEAD^{commit}" 2>/dev/null) || _head_sha=""
    if [ -z "$_base_sha" ] || [ -z "$_head_sha" ]; then
        echo "  skip: cannot resolve base/head commits"
    else
        _cs_rc=0
        bash ./scripts/checks/upstream-conflict-surface.sh \
            --upstream-ref upstream/main \
            --base "$_base_sha" \
            --head "$_head_sha" 2>&1 || _cs_rc=$?
        if [ "$_cs_rc" -eq 1 ]; then
            echo "  warn: new upstream conflict files detected (advisory; CI gate blocks)"
        elif [ "$_cs_rc" -gt 1 ]; then
            echo "  warn: upstream-conflict-surface.sh exited $_cs_rc (advisory)"
        fi
    fi
fi

# ---- sub2api: upstream insertion invasiveness (advisory) --------------------
# Advisory only: classifies how invasively this branch touches upstream-shaped
# Go files (EOF append vs func-body insert). Never blocks locally; CI runs
# the full report in upstream-conflict-surface.yml.
echo ""
echo "=== sub2api: upstream insertion invasiveness (advisory) ==="
if ! git remote get-url upstream >/dev/null 2>&1; then
    echo "  skip: no upstream remote"
elif ! command -v python3 >/dev/null 2>&1; then
    echo "  skip: python3 not on PATH"
else
    _inv_rc=0
    python3 ./scripts/checks/upstream-insertion-invasiveness.py \
        --base "${PREFLIGHT_BASE:-origin/main}" \
        --head HEAD \
        --upstream-ref upstream/main 2>&1 || _inv_rc=$?
    if [ "$_inv_rc" -ne 0 ]; then
        echo "  warn: upstream-insertion-invasiveness.py exited $_inv_rc (advisory)"
    fi
fi

echo ""
if [ "$errors" -eq 0 ]; then
    echo "=== preflight (with sub2api checks): PASS ==="
    exit 0
else
    echo "=== preflight (with sub2api checks): FAIL ($errors check(s) failed) ==="
    exit 1
fi
