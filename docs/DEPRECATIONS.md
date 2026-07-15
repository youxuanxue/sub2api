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

## backend/internal/handler/openai_embeddings.go

- **Upstream path:** `backend/internal/handler/openai_embeddings.go`
  (still present in `upstream/main`; introduced upstream in `ccace69d4`
  "Add OpenAI embeddings gateway").
- **Deletion commits + PRs:**
  - `3b4e780d75ca87b9d81724cf31e126aaa8fdc21b` — "fix(invariant): remove
    upstream duplicate Embeddings handler" (tk-upstream-agent[bot],
    2026-05-28), landed via
    [PR #450](https://github.com/youxuanxue/sub2api/pull/450)
    (`merge/upstream-2026-05-28`, merge commit `562d6f18b`).
  - Re-deleted after the 2026-05-29 merge resurrected it:
    `31f116c98a20c913bf1b1660792aa5a1893ff2e4` — "fix(merge): reconcile
    half-merged upstream features post-2026-05-29 merge" (247 lines), landed
    via [PR #456](https://github.com/youxuanxue/sub2api/pull/456)
    (`merge/upstream-2026-05-29`, merge commit `aefba7b73`).
- **Reason:** Go duplicate-symbol conflict, not a feature removal. Upstream's
  file declares `func (h *OpenAIGatewayHandler) Embeddings(c *gin.Context)`,
  which TK already implements in
  `backend/internal/handler/openai_gateway_embeddings_images.go:21` with TK
  extensions (recoverResponsesPanic, body guard, session hash, pool-mode
  retry, `ForwardAsEmbeddingsDispatched` dispatch, `TkSetBridgeGinAuth`,
  billing handler — see line 215). Keeping both files does not compile
  (method redeclared on the same receiver). The embeddings capability itself
  remains fully wired: `backend/internal/server/routes/gateway.go:61` and
  `:95` route `POST /embeddings` / `/v1/embeddings` through
  `tkOpenAICompatEmbeddingsHandler`
  (`backend/internal/server/routes/gateway_tk_openai_compat_handlers.go:88-100`),
  which calls `h.OpenAIGateway.Embeddings`. Upstream's **service**-layer files
  were kept: `backend/internal/service/openai_embeddings.go`
  (`ForwardEmbeddings` — no name collision with TK's
  `ForwardAsEmbeddingsDispatched`) and
  `backend/internal/service/openai_embeddings_test.go` are both in TK's tree.
- **Regression cost:** every upstream change to
  `backend/internal/handler/openai_embeddings.go` resurrects the file or
  conflicts at each upstream merge (this already happened once, at the
  2026-05-29 merge — hence the second deletion commit above). The merge
  resolver must re-drop the file and manually port any upstream behavior
  delta into `openai_gateway_embeddings_images.go`.
- **Upstream tests lost:** none. Upstream has no handler-level test for this
  file; its service-level `backend/internal/service/openai_embeddings_test.go`
  is retained in TK.
- **Re-adopt when:** upstream renames its handler method (removing the symbol
  collision), or TK refactors the combined
  `openai_gateway_embeddings_images.go` so the upstream-shaped handler file
  can carry the upstream shape again with TK deltas in a `*_tk_*.go`
  companion. Until then, at every upstream merge: re-drop this file, port
  deltas, keep this entry.

## backend/internal/service/redeem_service_redeem_test.go

- **Upstream path:** `backend/internal/service/redeem_service_redeem_test.go`.
- **Deletion commit + PR:** **none — this is NOT a TK deletion.** Forensics
  (2026-07-03): `git log --all --full-history --diff-filter=D -- <path>`
  finds no TK-side deletion commit. The file was **added upstream on
  2026-07-02** in `372436323` "修复邀请码普通兑换错误" (upstream
  [Wei-Shaw/sub2api PR #3657](https://github.com/Wei-Shaw/sub2api/pull/3657),
  merge `5d7f213cb`), which is **after** TK's current merge-base with
  upstream (`9caa3c9c5`, the last merged upstream commit). TK has simply not
  merged it yet — it is one of the 6 pending upstream commits as of
  2026-07-03.
- **Reason listed here anyway:** the CLAUDE.md §5.x drift detector uses the
  two-dot form (`git diff --diff-filter=D upstream/main..HEAD`), which
  compares trees directly and therefore counts *not-yet-merged upstream
  additions* as "deletions". This entry documents that reading as a false
  positive. The mechanical gate (`scripts/checks/upstream-deletion-ledger.py`)
  uses merge-base (three-dot) semantics, under which this file correctly does
  **not** appear as deleted.
- **Regression cost:** none today. The real risk is at the **next upstream
  merge**: this test file and the `backend/internal/service/redeem_service.go`
  fix arrive together in `372436323`, and the 2026-05-29 merge already
  demonstrated the failure mode of dropping upstream test files during
  conflict resolution (see `31f116c98`). The merge MUST adopt both.
- **Upstream tests lost:** none yet; the file (102 lines of redeem/invitation
  regression tests) is pending adoption.
- **Re-adopt when:** the next `merge/upstream-YYYYMMDD` merge lands — the file
  should be taken as-is from upstream. Delete this entry once the merge is on
  `main` and `git diff --diff-filter=D upstream/main..HEAD -- backend/` no
  longer lists it.

## frontend/src/components/account/__tests__/CreateAccountModal.grok.spec.ts

- **Upstream path:** `frontend/src/components/account/__tests__/CreateAccountModal.grok.spec.ts`.
- **Deletion commit + PR:** `3fa27f851` — "fix(upstream): address R-001..R-002 — restore Grok OAuth create wiring", landed through
  [PR #1353](https://github.com/youxuanxue/sub2api/pull/1353). The upstream
  merge itself removed this standalone fixture after folding its coverage into
  `CreateAccountModal.spec.ts`.
- **Reason:** this is an upstream-owned test consolidation, not a TokenKey
  product deletion. The replacement test file remains in the tree and covers
  the shared account modal behavior.
- **Regression cost:** no production behavior is removed; future changes to
  Grok-specific account creation must update the consolidated modal tests.
- **Upstream tests lost:** the standalone Grok fixture only; its assertions were
  folded into `CreateAccountModal.spec.ts` by the upstream change.
- **Re-adopt when:** upstream restores the standalone fixture or splits the
  coverage back out; remove this ledger entry in the same merge that re-adds it.
