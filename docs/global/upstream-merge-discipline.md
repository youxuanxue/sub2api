# Upstream merge discipline (CLAUDE.md §5 overflow)

Canonical hard rules stay in root `CLAUDE.md` §5; this file holds the long-form reference for merge history, mechanical gates, and minimal-invasion patterns.

## 5.y Forward-looking history & merge discipline

The `main` branch is **immutable history** once pushed. The TK-ahead commits include both linear and merge commits and several `vX.Y.Z` tags pointing into them — rewriting history would orphan tags and break PR audit trails. Going forward:

- **No history rewrites on `main`.** No `git rebase -i` of pushed commits, no `git push --force` to `main`/`master`, no squash-merge of already-merged feature branches.
- **Every TK feature lands via PR** with a clear scope (new file or one upstream-file injection point), reviewed against rule §5 above. Small + frequent beats one giant rebase.
- **PR merge mode is content-typed, not personal preference:**
  - **TK feature / fix / chore PRs** (anything originating from this fork) → GitHub **"Squash and merge"**. The PR becomes **one** commit on `main` whose title = PR title and whose body aggregates the development commits. Rationale: the feature branch's work-in-progress commits (`fix lint`, `typo`, `rebase main`, sync-VERSION housekeeping) carry no long-term audit value once the PR is approved as a unit; collapsing them keeps `git log --oneline --first-parent main` readable and keeps the per-PR diff trivially `git revert`-able.
  - **Upstream merge PRs** (`merge/upstream-YYYYMMDD`) → GitHub **"Create a merge commit"** invoked locally as `git merge --no-ff upstream/main`. Never `--squash`, never `--ff-only`. Rationale: each upstream commit is an external contract reference; squashing them severs the `git log upstream/main..HEAD` audit chain that §5.y depends on.
  - This is **not** a contradiction with "no squash-merge of already-merged feature branches" above: that rule forbids rewriting commits **already on `main`**; the squash here happens **at PR-merge time**, before anything reaches `main`.
- **`git merge-tree upstream/main HEAD` is the pre-merge dry-run.** Run it before any upstream merge to surface conflicts; resolve in a dedicated `merge/upstream-YYYYMMDD` branch, not on `main`.
- **Tag = consolidation point, not a rewrite cue.** When you tag `vX.Y.Z`, all earlier commits become permanent history. If a tag points at a commit with `[skip ci]` (see CLAUDE.md §9.2), do NOT delete and re-tag — dispatch the workflow manually.
- **Audit cadence:** every merge PR description includes `git log --oneline upstream/main..HEAD | wc -l` (TK ahead count) + `git diff --stat upstream/main..HEAD -- backend/` (top changed files). Use these numbers to decide whether the next batch of TK work should be split into smaller PRs.

## 5.y.1 Mechanical enforcement stack

Per dev-rules §"Hard Constraint Wiring" — every soft rule above MUST have an automated gate.

| Mechanism | Trigger | What it does |
|---|---|---|
| `scripts/upstream/check-drift.sh` | local, on demand | Prints TK ahead/behind vs `upstream/main` + the §5.y procedure when behind. Exit: `0` synced, `1` behind, `2` failure; `--json` / `--quiet`. |
| `.github/workflows/upstream-merge-agent-daily.yml` | daily 04:00 Asia/Shanghai + manual dispatch | Single periodic upstream-sync path: drift → headless merge automation when needed → final preflight + PR-body cadence audit; actionable blockers tracked via the `upstream-merge-agent` issue channel. |
| `.github/workflows/upstream-merge-pr-shape.yml` | PRs from `merge/upstream-*` | Hard gates: (a) PR must contain a merge commit whose second parent is reachable from `upstream/main` (squash/ff fail), (b) PR body must include the literal substring `upstream/main..HEAD` (§5.y audit cadence), (c) no first-parent commit introduced by the PR may carry the bracketed skip-ci marker (see CLAUDE.md §9.2; imported upstream history exempt), (d) the newapi sentinel registry (`scripts/sentinels/newapi.json`) is intact — `scripts/preflight.sh` runs the same script locally. |
| `scripts/checks/upstream-override-marker.py` (via `scripts/preflight.sh`) | every PR (CI preflight + local pre-commit) | When the PR diff touches any upstream-shaped path (handlers/services/views/… excluding `*_tk_*.go` / `*.tk.ts` / `*_test.go` / TK-only subpackages), the gate is **coverage-first**: a pure-insertion diff or **verified sentinel coverage** of every deletion-bearing upstream file (its `path` is pinned in some `scripts/sentinels/*.json`, pre-existing or added this PR) auto-passes with **no marker**. Only an *uncovered* revert-risk edit needs a marker. `upstream-touch-guarded` is **mechanically verified, not trusted**: it asserts the touched files are already pinned, so if they are NOT the claim is false and the gate **fails** (it can no longer be a free bypass — covered edits already passed without it). The other three markers assert protection is *not needed* and remain honest, reviewer-visible opt-outs: `upstream-touch-trivial` (no revert risk) / `upstream-merge` (the merge PR) / `no-upstream-touch` (misclassified path). Forced confrontation before merge, like `no-web-impact`. |
| `.github/workflows/main-ancestry-guard.yml` | any PR with `base.ref == 'main'` | **Check 1:** PR base must be an ancestor of PR head (catches the PR #307 orphan-reset mode). **Check 2:** `.main-ancestry-anchor` may only change with the `main-ancestry-anchor-advance` marker AND a new SHA descending from the old (see below). **Check 3:** PR title/body/commit messages must not contain the bracketed forms `[skip ci]` / `[ci skip]` (see CLAUDE.md §9.2). |
| `scripts/checks/main-ancestry-anchor.py` (via `scripts/preflight.sh`) | every PR (CI preflight + local pre-commit) | Verifies the SHA in repo-root `.main-ancestry-anchor` is an ancestor of HEAD — catches orphan-resets on paths the PR-level guard can't see (direct push, force push, propagated reset). |

**Anchor advancement.** `.main-ancestry-anchor` is a one-way ratchet: a known-good SHA every future HEAD must descend from (baseline `62482fa9bc30ac292ecca92341ef055a024d8a26`, locked after the PR #307 orphan-reset incident). To advance it: open a PR that updates the file AND carries the literal marker `main-ancestry-anchor-advance` + a one-line justification in a commit message. Guard Check 2 also requires the new SHA to be a descendant of the old one — the descendant check is the real ratchet, the marker is the human-attention gate.

**Branch protection on `main`** requires CI jobs `preflight`, `test-unit`, `test-integration`, `frontend`, `golangci-lint`, `backend-security`, `frontend-security`, plus PR gates `main-ancestry-guard` and `marker-acknowledgement`. Do **not** require `upstream-merge-pr-shape` globally — it only runs on `merge/upstream-*` PRs and skips elsewhere. Known limit: GitHub doesn't expose merge-method choice, so these gates can't stop a manual "Squash and merge" click on a merge PR — they only make the wrong shape fail CI.

## Convergence & minimal invasion (especially large upstream files)

**Goal:** TK behavior should **converge** into dedicated modules so the fork stays **merge-friendly**; upstream files should read almost unchanged except for **thin injection points** (imports + a few lines, not new pages of logic).

**When the file is upstream-shaped and large** (e.g. `gateway_handler*.go`, `openai_*_handler.go`, `setting_handler.go`, `endpoint.go`, `ChannelsView.vue`, account modals):

1. **Do not** paste multi-screen TK branches, repeated error handling, or catalog/API glue into the upstream file.
2. **Do** implement behavior in a companion and call it from upstream:
   - **Go (same package):** `*_tk_*.go` — selection/affinity, relay error JSON, endpoint aliases, settings merge fields, passkey routes, admin route registration helpers (`registerTK*`), etc.
   - **Go (shared across handlers):** small neutral helpers in the `handler` package (e.g. `TkTryWriteNewAPIRelayErrorJSON`, `TkAPIKeyGroupName`) to dedupe without inflating each upstream handler.
   - **Routes:** move new paths and predicates into `*_tk_*.go` in `internal/server/routes/`; keep `admin.go` / `gateway.go` to a single `registerTK…()` call where possible.
   - **Vue / TS:** `frontend/src/composables/useTk*.ts` (and `constants/` or `*.tk.ts` for pure maps). Views and modals stay **template + wiring**; composables own API calls, watchers, and state for TK-only flows.
3. **Upstream file edits** should trend toward: **one import block delta + one-line hooks** (or replacing a repeated 10–20 line pattern with **one** helper call), not reformatting or reordering unrelated upstream code.
4. **DTO / struct fields** that belong to the upstream request/response shape may still live in the primary handler file; **validation, merge rules, and audit diffs** belong in `*_tk_*.go` helpers.
5. **Out of scope for this pattern:** Ent schema + generated `ent/`, `wire_gen.go`, and migration SQL — follow schema-first rules; generated churn is expected.

**Anti-patterns:** Duplicating the same `errors.As` + JSON response blocks across handlers; duplicating aggregated-admin API logic across large `.vue` files; registering many new routes inline in `admin.go` instead of a `registerTK…` helper.
