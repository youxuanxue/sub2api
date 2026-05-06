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
#        silently deleted. Driven by `scripts/newapi-sentinels.json`
#        (single source of truth) via `scripts/check-newapi-sentinels.py`.
#        The same script is invoked by
#        `.github/workflows/upstream-merge-pr-shape.yml`.
#   brand sentinel registry      — guards outward TokenKey brand surfaces
#        (browser title, deploy/operator surfaces, image metadata,
#        fifth-platform display label) from drifting back apart. Driven by
#        `scripts/brand-sentinels.json` via `scripts/check-brand-sentinels.py`;
#        intentionally separate from `newapi` semantics / routing truth.
#   redaction version contract   — guards Evidence Spine contract drift:
#        changing the default sensitive-key set in logredact must bump the
#        outward QA `redaction_version` contract in the same commit. Driven by
#        `scripts/redaction-sentinels.json` via `scripts/check-redaction-version.py`.
#   trajectory hook registry     — guards the request-evidence hook contract:
#        main gateway scopes must keep `trajectory_id` + `qaCapture` wiring, and
#        the QA middleware must still terminate in `CaptureFromContext`. Driven by
#        `scripts/trajectory-sentinels.json` via `scripts/check-trajectory-hooks.py`.
#   terminal event registry      — guards stream terminal semantics: OpenAI /
#        Anthropic terminal helpers, `[DONE]` emission, and focused terminal-path
#        assertions must remain intact so evidence capture keeps stable completion
#        signals. Driven by `scripts/terminal-sentinels.json` via
#        `scripts/check-terminal-events.py`.
#   engine facade registry      — guards Engine Spine dispatch semantics: key
#        gateway dispatch paths must keep routing bridge eligibility through
#        shared engine facade helpers instead of drifting back into hotspot
#        service files. Driven by `scripts/engine-facade-sentinels.json` via
#        `scripts/check-engine-facade-hooks.py`.
#   OpenAI upstream capability truth — guards Responses probe status semantics:
#        probe call sites must use `internal/pkg/openai_compat` as the owner
#        instead of reintroducing local status-code truth in service files.
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

# ---- Sections 1-8: delegate to dev-rules template ----------------------------
if [ ! -x ./dev-rules/templates/preflight.sh ]; then
    echo "FAIL: dev-rules submodule not initialized."
    echo "      Run: git submodule update --init --recursive"
    exit 1
fi

PREFLIGHT_REPO_ROOT="$REPO_ROOT" ./dev-rules/templates/preflight.sh "$@"
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

# ---- sub2api: newapi sentinel registry --------------------------------------
# Source of truth: scripts/newapi-sentinels.json. Verifies that every
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
elif ! python3 ./scripts/check-newapi-sentinels.py --quiet; then
    # check-newapi-sentinels.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: all newapi sentinels intact"
fi

# ---- sub2api: brand sentinel registry ---------------------------------------
# Source of truth: scripts/brand-sentinels.json. Verifies that outward TokenKey
# brand surfaces (default title, deploy/operator docs, image metadata,
# fifth-platform display label) stay converged without turning compat identities
# like `sub2api` / `newapi` into banned strings across the repo.
echo ""
echo "=== sub2api: brand sentinel registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read brand-sentinels.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/check-brand-sentinels.py --quiet; then
    # check-brand-sentinels.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: all brand sentinels intact"
fi

# ---- sub2api: redaction version contract ------------------------------------
# Source of truth: scripts/redaction-sentinels.json. Verifies that the default
# sensitive-key set in logredact and the outward QA redaction_version literals
# move together, so a changed evidence redaction policy cannot silently keep the
# old version string.
echo ""
echo "=== sub2api: redaction version contract ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read redaction-sentinels.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/check-redaction-version.py --quiet; then
    # check-redaction-version.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: redaction key snapshot and version sources are aligned"
fi

# ---- sub2api: trajectory hook registry --------------------------------------
# Source of truth: scripts/trajectory-sentinels.json. Verifies that the main
# gateway route scopes still carry trajectory_id + qaCapture wiring, and that
# the QA middleware still terminates in CaptureFromContext after teeing request /
# response bodies.
echo ""
echo "=== sub2api: trajectory hook registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read trajectory-sentinels.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/check-trajectory-hooks.py --quiet; then
    # check-trajectory-hooks.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: gateway trajectory hooks and QA terminal capture are aligned"
fi

# ---- sub2api: terminal event registry ---------------------------------------
# Source of truth: scripts/terminal-sentinels.json. Verifies that the stable
# terminal-event helpers, `[DONE]` emission, and focused terminal assertions stay
# intact so evidence capture keeps reliable completion markers.
echo ""
echo "=== sub2api: terminal event registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read terminal-sentinels.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/check-terminal-events.py --quiet; then
    # check-terminal-events.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: terminal-event helpers and focused assertions are aligned"
fi

# ---- sub2api: engine facade registry -----------------------------------------
# Source of truth: scripts/engine-facade-sentinels.json. Verifies that the key
# gateway dispatch paths still route bridge eligibility through the shared
# Engine facade helpers instead of reintroducing local provider branching.
echo ""
echo "=== sub2api: engine facade registry ==="
if ! command -v python3 >/dev/null 2>&1; then
    echo "  FAIL: python3 not on PATH (required to read engine-facade-sentinels.json)"
    errors=$((errors + 1))
elif ! python3 ./scripts/check-engine-facade-hooks.py --quiet; then
    # check-engine-facade-hooks.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: key dispatch paths still route through Engine facade truth"
fi

# ---- sub2api: QA evidence dataset validator ----------------------------------------
# Source of truth: scripts/check-qa-evidence-dataset.py. Verifies that the standalone
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
elif ! python3 -m py_compile ./scripts/check-frontend-release-assets.py ./scripts/check-frontend-dist-freshness.py; then
    errors=$((errors + 1))
elif ! python3 ./scripts/check-frontend-dist-freshness.py --check ./backend/internal/web/dist; then
    errors=$((errors + 1))
elif [ -f ./backend/internal/web/dist/index.html ] && ls ./backend/internal/web/dist/assets/AccountsView-*.js >/dev/null 2>&1; then
    if ! python3 ./scripts/check-frontend-release-assets.py --dist ./backend/internal/web/dist; then
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
if ! bash -n ./scripts/tk_post_deploy_smoke.sh; then
    echo "  FAIL: scripts/tk_post_deploy_smoke.sh has bash syntax errors"
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
elif ! python3 ./scripts/check-workflow-job-if-env.py --quiet; then
    # check-workflow-job-if-env.py already printed the actionable failure.
    errors=$((errors + 1))
else
    echo "  ok: no env references in job-level if expressions"
fi

# Headless agent stream redactor: scripts/redact-agent-stream.py sits between
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
elif ! bash ./scripts/redact-agent-stream_test.sh >/dev/null; then
    echo "  FAIL: scripts/redact-agent-stream_test.sh failed (re-run for details)"
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
