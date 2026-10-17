# DEPRECATIONS — upstream deletion ledger

This file is the durable registry required by CLAUDE.md **§5.x「Deletion
discipline — default = keep, override; never silent-delete」**. Any file that
exists in `upstream/main` (Wei-Shaw/sub2api) but has been deliberately deleted
from TokenKey's tree MUST have an entry here containing, verbatim, the
repo-relative path, plus: the deletion commit + PR link, the reason, the
regression cost, the upstream tests lost, and the conditions under which TK
should re-adopt the file.

**Mechanical enforcement:** `scripts/checks/upstream-deletion-ledger.py`
computes `git diff --diff-filter=D --name-only upstream/main...HEAD --
backend/ frontend/` (merge-base semantics, so not-yet-merged upstream
*additions* do not false-positive) and fails if any deleted path is missing
from this file. Environments without an `upstream` remote (e.g. plain CI
clones) are skipped harmlessly. The deleted path must appear **verbatim**
(exact string, e.g. `backend/internal/handler/openai_embeddings.go`) somewhere
in this document.

**Housekeeping:** if a future upstream merge re-adopts a file, delete its
entry. If an entry's "re-adopt when" condition becomes true, open a PR that
restores the file and removes the entry in the same change.

---

## Cursor capability retirement

- **Deletion commit + PR:** `a1843854ac`, [Cursor retirement #2690](https://github.com/youxuanxue/sub2api/pull/2690).
- **Reason:** the user explicitly retired Cursor accounts, native inference and
  client connection support under `docs/approved/cursor-retirement.md`. Keeping
  the implementation behind a flag would preserve an unwanted credential and
  execution path.
- **Regression cost / tests lost:** Cursor OAuth, refresh transactions, native
  Agent framing, tool continuation, usage estimation, content caching and live
  provider probes are intentionally no longer supported or tested. Shared
  protocol conversions, billing and other suppliers retain their own suites.
  US-066 replaces the retired feature suites with account admission/scheduling,
  route 404 and real browser retirement regressions, including supported-client
  configuration. Historical usage and billing rows remain intact.
- **Post-merge documentation cleanup:** obsolete design, recovery instructions
  and US-060 were removed from the working tree to prevent agent reactivation.
  Git history retains them; the still-active output-limit contract moved to
  `docs/approved/protocol-routing-ssot.md#output-limit-compatibility`.
- **Re-adopt when:** only after a new explicit product approval to restore Cursor,
  followed by a new credential/protocol review and restored behavioral coverage;
  an upstream merge alone must not reactivate this supply.
- **Deleted paths:**

  - `docs/approved/cursor-oauth-service.md`
  - `docs/approved/design-cursor-tool-gateway-compat.md`
  - `docs/task-breakdown-cursor-tool-gateway-compat.md`
  - `docs/spec-delta-cursor-fable-display.md`
  - `docs/ops/cursor-errors-ssot-20260915.md`
  - `docs/ops/cursor-messages-validation-20260914.md`
  - `docs/ops/cursor-upstream-revalidation-20260916.md`
  - `.testing/user-stories/stories/US-060-cursor-client-only-tools.md`
  - `backend/internal/handler/admin/account_handler_tk_cursor.go`
  - `backend/internal/handler/gateway_handler_tk_cursor_messages_identity_test.go`
  - `backend/internal/integration/cursor/agent.go`
  - `backend/internal/integration/cursor/agent_compare_live_tk_test.go`
  - `backend/internal/integration/cursor/agent_error_tk.go`
  - `backend/internal/integration/cursor/agent_error_tk_test.go`
  - `backend/internal/integration/cursor/agent_test.go`
  - `backend/internal/integration/cursor/agent_timeout_repro_tk_test.go`
  - `backend/internal/integration/cursor/agentpb/VENDORED_FROM.md`
  - `backend/internal/integration/cursor/agentpb/agent.pb.go`
  - `backend/internal/integration/cursor/agentpb/agent.proto`
  - `backend/internal/integration/cursor/client.go`
  - `backend/internal/integration/cursor/client_shell_routing_tk.go`
  - `backend/internal/integration/cursor/client_shell_routing_tk_test.go`
  - `backend/internal/integration/cursor/client_test.go`
  - `backend/internal/integration/cursor/client_tool_metadata_tk.go`
  - `backend/internal/integration/cursor/client_tool_metadata_tk_test.go`
  - `backend/internal/integration/cursor/estimate.go`
  - `backend/internal/integration/cursor/messages.go`
  - `backend/internal/integration/cursor/messages_agentrun_tk.go`
  - `backend/internal/integration/cursor/messages_agentrun_tk_test.go`
  - `backend/internal/integration/cursor/messages_retry_tk_test.go`
  - `backend/internal/integration/cursor/messages_test.go`
  - `backend/internal/integration/cursor/native_boundary_tk_test.go`
  - `backend/internal/integration/cursor/native_client_results_tk.go`
  - `backend/internal/integration/cursor/native_client_tools_tk.go`
  - `backend/internal/integration/cursor/native_client_tools_tk_test.go`
  - `backend/internal/integration/cursor/native_continuation_close_tk_test.go`
  - `backend/internal/integration/cursor/native_runs_tk.go`
  - `backend/internal/integration/cursor/native_runs_tk_test.go`
  - `backend/internal/integration/cursor/oauth.go`
  - `backend/internal/integration/cursor/oauth_refresh.go`
  - `backend/internal/integration/cursor/oauth_refresh_live_test.go`
  - `backend/internal/integration/cursor/oauth_refresh_test.go`
  - `backend/internal/integration/cursor/oauth_test.go`
  - `backend/internal/integration/cursor/tool_compat_tk_test.go`
  - `backend/internal/repository/account_repo_cursor_refresh_integration_test.go`
  - `backend/internal/repository/account_repo_cursor_refresh_tk.go`
  - `backend/internal/repository/account_repo_cursor_transaction_integration_test.go`
  - `backend/internal/server/routes/admin_tk_cursor_routes.go`
  - `backend/internal/service/account_model_mapping_cursor_tk_test.go`
  - `backend/internal/service/account_tk_cursor.go`
  - `backend/internal/service/account_tk_cursor_test.go`
  - `backend/internal/service/candidate_content_reuse_tk_test.go`
  - `backend/internal/service/candidate_cursor_ssot_test.go`
  - `backend/internal/service/cursor_buffered_failure_regression_test.go`
  - `backend/internal/service/cursor_content_consistency_tk_test.go`
  - `backend/internal/service/cursor_content_fanout_cost_bench_tk_test.go`
  - `backend/internal/service/cursor_content_split_equivalence_tk_test.go`
  - `backend/internal/service/cursor_messages_live_tk_test.go`
  - `backend/internal/service/cursor_native_transport_regression_test.go`
  - `backend/internal/service/cursor_relay_billing_regression_test.go`
  - `backend/internal/service/cursor_rollout_regression_test.go`
  - `backend/internal/service/cursor_token_refresher_tk.go`
  - `backend/internal/service/cursor_token_refresher_tk_test.go`
  - `backend/internal/service/gateway_cursor_tool_json_test.go`
  - `backend/internal/service/gateway_tk_cursor.go`
  - `backend/internal/service/gateway_tk_cursor_test.go`
  - `backend/internal/service/openai_cursor_warmup_pipeline_test.go`
  - `frontend/e2e/cursor-authorization-recovery.e2e.ts`
  - `frontend/e2e/cursor-live.tk.mjs`
  - `frontend/src/api/admin/cursor.tk.ts`
  - `frontend/src/components/account/CursorConnectModal.tk.vue`
  - `frontend/src/composables/__tests__/useCursorAuthorization.tk.spec.ts`
  - `frontend/src/composables/useCursorAuthorization.tk.ts`

## Upstream import: PR #2062

Deletion commit: `9e0bf73e092e99e0b7251d5b938a16c36357c137`, reviewed in
[PR #2062](https://github.com/youxuanxue/sub2api/pull/2062).

- `backend/internal/service/channel_plaza.go`: upstream moved aggregation to
  `model_plaza_service.go`. TokenKey composite-platform isolation, deterministic
  platform/model deduplication, image-price precedence and official pricing
  remain in `ModelPlazaService`; `model_plaza_service_test.go` covers the new
  owner. No plaza behavior is intentionally retired. Future upstream edits to
  the old path must be ported to the new owner; re-adopt only if upstream moves
  the canonical service back.
- `frontend/src/views/admin/__tests__/groupsModelsList.spec.ts`: the old
  `groupsModelsList` implementation was already replaced by `groupModelAllowlist`.
  All eight selection, ordering, hydration and disabled-state cases survive in
  `groupModelAllowlist.spec.ts`, alongside custom-entry cases. No test scenario
  is intentionally lost. New upstream cases must follow that owner; restore
  the old filename only if the implementation moves back.
- `assets/partners/logos/novada.png` and `assets/partners/logos/sui-xiang.jpg`:
  unused upstream partner artwork was removed upstream. No runtime or document
  references remain and no tests were removed with these assets. Regression
  cost is limited to losing the unused artwork; re-adopt when a reviewed partner
  entry actually references it.

## Deletion ledger: redundant frontend tests

- **Deletion commit + PR:** `63e351dec`, [fork cleanup PR](https://github.com/youxuanxue/sub2api/pulls?q=is%3Apr+head%3Achore%2Fclean0910).
- **Path:** `frontend/src/components/layout/__tests__/siteLogoSanitization.spec.ts`.
  **Reason and replacement:** redundant logo expectations are covered by the active branding tests, `frontend/src/utils/__tests__/defaultSiteLogo.spec.ts`, and the brand surface checks; keep the runtime branding owner as the source of truth.
  **Regression cost / tests lost:** this standalone suite no longer receives automatic upstream additions. During upstream review, move any new URL-safety or logo-fallback behavior into the active branding coverage instead of restoring duplicate fixtures.
  **Re-adopt when:** upstream adds distinct runtime branding behavior not covered by the active suites.
- **Path:** `frontend/src/features/channel-monitor-v2/__tests__/designSystem.structure.spec.ts`.
  **Reason and replacement:** source-string checks for CSS classes and component names do not prove the monitor settings entry works. `frontend/e2e/admin-maintenance.e2e.ts` drives the actual route, changes settings, saves, reloads, and switches management tabs on desktop and mobile; existing monitor component behavior tests remain.
  **Regression cost / tests lost:** exact historical CSS tokens, SVG/layout composition, and source ordering are no longer pinned. Future upstream changes to those assertions require behavior-level review instead of automatic fixture restoration.
  **Re-adopt when:** the suite tests distinct user-visible behavior or accessibility guarantees, rather than source text already superseded by the current UI.

## Deletion ledger: `backend/internal/service/openai_apikey_responses_probe_verdict_test.go#removed`

- **Upstream path:** `backend/internal/service/openai_apikey_responses_probe_verdict_test.go`.
- **Deletion commit + PR:** `a760c22ce` — "feat(protocol): share capability truth by endpoint identity", proposed in [PR #1848](https://github.com/youxuanxue/sub2api/pull/1848).
- **Reason:** PR #1848 replaces the account-owned Responses probe writer with the endpoint-scoped protocol capability probe. Keeping this file would preserve a second test harness around the removed per-account `extra` mutation path. Its classifier cases now live in `backend/internal/service/openai_apikey_responses_probe_test.go`; inconclusive-history preservation, conclusive mutation, conflict, and one-persist-per-generation behavior live in `backend/internal/service/protocol_capability_probe_test.go` against the new SSOT owner.
- **Regression cost:** future upstream additions to this standalone per-account verdict suite will not merge automatically. Upstream-merge review must map any new response classification into `openai_apikey_responses_probe_test.go` and any persistence/history invariant into `protocol_capability_probe_test.go`, without restoring account-owned protocol facts.
- **Upstream tests lost:** the standalone `runResponsesProbe` account-`extra` fixture and its two persistence tests were removed because that storage path no longer exists. `TestResponsesProbeVerdictIsConclusive` remains under `openai_apikey_responses_probe_test.go`; equivalent endpoint-scoped preservation and commit behavior is covered by `TestApplyProtocolProbeVerdictsUpdatesOnlyConclusiveEndpointFacts`, `TestResolveProtocolProbeGenerationKeepsConclusiveHistoryAcrossInconclusiveGeneration`, and `TestProbeAccountProtocolCapabilitiesEvaluatesCandidateSetAndPersistsOnce`.
- **Re-adopt when:** upstream adopts the endpoint-capability SSOT and rewrites this file to exercise a distinct behavior not covered by the classifier and endpoint-scoped probe suites. Restore the file and remove this entry in the same change; never re-adopt the account-owned `extra` writer fixture.
