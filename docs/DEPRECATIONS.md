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
