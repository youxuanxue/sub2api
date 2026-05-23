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
trap 'git config --local --unset core.bare >/dev/null 2>&1 || true' EXIT

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
drift2_hits="$(grep -nE '!\s*account\.IsOpenAI\(\)' \
    backend/internal/service/openai_account_scheduler.go \
    backend/internal/service/openai_gateway_service.go 2>/dev/null || true)"
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

# ---- sub2api: sentinel registry update gate ---------------------------------
# Existing sentinel checks prove current guarded literals still exist. This gate
# proves PRs that modify guarded/hotspot files also update the matching registry,
# turning the recurring review ask "补充必要的 upstream merge 覆写防护门禁" into
# a hard preflight failure instead of human memory.
echo ""
echo "=== sub2api: sentinel registry update gate ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for sentinel registry update gate)"
    errors=$((errors + 1))
elif ! python3 ./scripts/sentinels/check-registry-update-gate.py --quiet; then
    # check-sentinel-registry-update-gate.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: guarded hotspot changes update their sentinel registries"
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
    if ! python3 ./scripts/checks/script-ref-existence.py; then
        # script-ref-existence.py already printed the actionable failure list.
        errors=$((errors + 1))
    fi
    if ! bash ./scripts/checks/script-ref-existence_test.sh; then
        # script-ref-existence_test.sh already printed which case(s) failed.
        errors=$((errors + 1))
    fi
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
echo "=== sub2api: upstream override marker ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required for upstream override marker check)"
    errors=$((errors + 1))
elif ! python3 ./scripts/checks/upstream-override-marker.py --quiet; then
    # check-upstream-override-marker.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: upstream-shaped paths protected (sentinel update or marker present)"
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
elif ! (cd backend && go test -tags=unit ./internal/observability/qa -run 'TestUS077_QAEvidenceDatasetCheck_' -count=1); then
    errors=$((errors + 1))
else
    echo "  ok: QA evidence dataset validator accepts/rejects covered fixtures as expected"
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
if ! bash -n ./ops/stage0/post_deploy_smoke.sh; then
    echo "  FAIL: ops/stage0/post_deploy_smoke.sh has bash syntax errors"
    errors=$((errors + 1))
else
    echo "  ok: tk_post_deploy_smoke.sh parses"
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
if [ ! -x ./scripts/checks/caddyfile-syntax.sh ]; then
    echo "  FAIL: scripts/checks/caddyfile-syntax.sh missing or not executable"
    errors=$((errors + 1))
elif ! ./scripts/checks/caddyfile-syntax.sh; then
    errors=$((errors + 1))
else
    echo "  ok: Caddyfile syntax gate"
fi

# ---- sub2api: edge-ip-status doc / live AWS drift ---------------------------
# Source of truth: deploy/aws/stage0/edge-targets.json + edge-polluted-ips.json
# + live AWS state. The script's --check mode reconciles
# docs/deploy/tokenkey-edge-ip-history.md § 1 / § 2 tables against generated
# output. Skips gracefully (exit 0) when AWS credentials are unavailable so
# preflight stays usable on dev laptops without AWS configured; in CI / on
# operator machines with creds, it catches forgotten regenerations after an
# EIP rotation.
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
if [ -f .github/workflows/deploy-edge-stage0.yml ]; then
    if ! grep -q 'ops/stage0/deploy_via_ssm.sh' .github/workflows/deploy-stage0.yml; then
        echo "  FAIL: deploy-stage0.yml must use ops/stage0/deploy_via_ssm.sh"
        errors=$((errors + 1))
    elif ! grep -q 'ops/stage0/deploy_via_ssm.sh' .github/workflows/deploy-edge-stage0.yml; then
        echo "  FAIL: deploy-edge-stage0.yml must use ops/stage0/deploy_via_ssm.sh"
        errors=$((errors + 1))
    elif grep -q 'docker compose --env-file .* up -d --no-deps tokenkey' .github/workflows/deploy-stage0.yml .github/workflows/deploy-edge-stage0.yml; then
        echo "  FAIL: Stage0 workflows must not inline tokenkey SSM deploy commands; use ops/stage0/deploy_via_ssm.sh"
        errors=$((errors + 1))
    else
        echo "  ok: prod deploy-stage0 and Edge workflows share the Stage0 SSM deploy primitive"
    fi
else
    echo "  ok: no Edge Stage0 workflow present"
fi

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
elif ! python3 -m unittest discover -s ops/anthropic -p 'test_*.py' -t ops/anthropic >/dev/null 2>&1; then
    echo "  FAIL: ops/anthropic unittest failed (re-run: python3 -m unittest discover -s ops/anthropic -p 'test_*.py' -t ops/anthropic -v)"
    errors=$((errors + 1))
else
    echo "  ok: ops/anthropic unittest suite (tier plan + oauth priority rebalance)"
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
    _det_baseline_failed=0
    for _det_dir in ops/observability ops/stage0 scripts deploy/aws/stage0 deploy/aws/lightsail; do
        if ! python3 -m unittest discover -s "$_det_dir" -p 'test_*.py' -t "$_det_dir" >/dev/null 2>&1; then
            echo "  FAIL: $_det_dir unittest failed (re-run: python3 -m unittest discover -s $_det_dir -p 'test_*.py' -t $_det_dir -v)"
            errors=$((errors + 1))
            _det_baseline_failed=1
        fi
    done
    if [ "$_det_baseline_failed" -eq 0 ]; then
        echo "  ok: determinism-baseline suites (observability / stage0 / scripts / deploy.stage0)"
    fi
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
elif ! bash ./scripts/agent/redact-stream_test.sh >/dev/null; then
    echo "  FAIL: scripts/agent/redact-stream_test.sh failed (re-run for details)"
    errors=$((errors + 1))
else
    echo "  ok: agent stream redactor self-test"
fi

echo ""
if [ "$errors" -eq 0 ]; then
    echo "=== preflight (with sub2api checks): PASS ==="
    exit 0
else
    echo "=== preflight (with sub2api checks): FAIL ($errors check(s) failed) ==="
    exit 1
fi
