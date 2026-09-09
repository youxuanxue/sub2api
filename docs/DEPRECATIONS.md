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

## Deletion ledger: `backend/internal/service/openai_apikey_responses_probe_verdict_test.go#removed`

- **Upstream path:** `backend/internal/service/openai_apikey_responses_probe_verdict_test.go`.
- **Deletion commit + PR:** `a760c22ce` — "feat(protocol): share capability truth by endpoint identity", proposed in [PR #1848](https://github.com/youxuanxue/sub2api/pull/1848).
- **Reason:** PR #1848 replaces the account-owned Responses probe writer with the endpoint-scoped protocol capability probe. Keeping this file would preserve a second test harness around the removed per-account `extra` mutation path. Its classifier cases now live in `backend/internal/service/openai_apikey_responses_probe_test.go`; inconclusive-history preservation, conclusive mutation, conflict, and one-persist-per-generation behavior live in `backend/internal/service/protocol_capability_probe_test.go` against the new SSOT owner.
- **Regression cost:** future upstream additions to this standalone per-account verdict suite will not merge automatically. Upstream-merge review must map any new response classification into `openai_apikey_responses_probe_test.go` and any persistence/history invariant into `protocol_capability_probe_test.go`, without restoring account-owned protocol facts.
- **Upstream tests lost:** the standalone `runResponsesProbe` account-`extra` fixture and its two persistence tests were removed because that storage path no longer exists. `TestResponsesProbeVerdictIsConclusive` remains under `openai_apikey_responses_probe_test.go`; equivalent endpoint-scoped preservation and commit behavior is covered by `TestApplyProtocolProbeVerdictsUpdatesOnlyConclusiveEndpointFacts`, `TestResolveProtocolProbeGenerationKeepsConclusiveHistoryAcrossInconclusiveGeneration`, and `TestProbeAccountProtocolCapabilitiesEvaluatesCandidateSetAndPersistsOnce`.
- **Re-adopt when:** upstream adopts the endpoint-capability SSOT and rewrites this file to exercise a distinct behavior not covered by the classifier and endpoint-scoped probe suites. Restore the file and remove this entry in the same change; never re-adopt the account-owned `extra` writer fixture.
