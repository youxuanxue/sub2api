# CLAUDE.md

Guidance for Claude Code / Cursor agents working in this repository.

## Project Overview

TokenKey (TK): AI API gateway for subscription quota distribution. Fork of [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api), integrating [QuantumNous/new-api](https://github.com/QuantumNous/new-api) relay adaptors via Go module import.

## Tech Stack

| Component | Stack | Location |
| --------- | --------------------------------------------------------- | ------------------------------ |
| Backend | Go 1.26+, Gin, Ent ORM, Wire DI | `backend/` |
| Frontend | Vue 3, Vite 5, TypeScript, Pinia, TailwindCSS 3, **pnpm** | `frontend/` |
| DB | PostgreSQL 16+ only | |
| Cache | Redis 7+ | |
| CI | GitHub Actions (`backend-ci`, `security-scan`, `release`) | `.github/workflows/` |
| Lint | golangci-lint v2 | `backend/.golangci.yml` |
| Deploy | Docker Compose (4 variants) | `deploy/` |

## Commands

```bash
# From repo root (sub2api/)
make build                        # Backend + frontend
make test                         # Backend tests + frontend lint/typecheck

# Backend (from backend/)
go test -tags=unit ./...          # Unit tests
go test -tags=integration ./...   # Integration tests (testcontainers)
golangci-lint run ./...           # Lint
go generate ./ent                 # Regen Ent code after schema change
go generate ./cmd/server          # Regen Wire DI

# Frontend (from frontend/)
pnpm install                      # Install deps — MUST use pnpm
pnpm dev                          # Dev server
pnpm build                        # Production build
pnpm lint:check && pnpm typecheck # Lint + type check
```

## Architecture

```
sub2api/                                  # This repo (.git)
├── CLAUDE.md
├── docs/                                 # Planning & operational docs
├── backend/
│   ├── cmd/server/                       # Entry point, Wire DI, VERSION
│   ├── ent/schema/                       # DB schema definitions (source of truth)
│   ├── ent/                              # Generated Ent ORM code
│   ├── internal/
│   │   ├── handler/                      # HTTP handlers (Gin)
│   │   ├── service/                      # Business logic, gateway forwarding
│   │   ├── repository/                   # Data access (Ent queries)
│   │   ├── middleware/                   # Auth, rate-limit, concurrency
│   │   ├── integration/newapi/           # New API bridge (affinity, payment SDKs)
│   │   ├── pkg/                          # Platform adapters (claude, openai, gemini, etc.)
│   │   ├── domain/                       # Constants & types
│   │   ├── model/                        # Business models
│   │   ├── config/                       # App configuration + Wire
│   │   ├── server/                       # Server bootstrap + routes
│   │   ├── setup/                        # First-run initialization
│   │   ├── web/                          # Embedded frontend dist
│   │   ├── testutil/                     # Test fixtures & stubs
│   │   └── util/                         # Shared utilities
│   ├── migrations/                       # SQL migrations (001–092+)
│   └── resources/model-pricing/          # Model pricing data
├── frontend/src/
│   ├── api/                              # API client
│   ├── views/ components/                # Pages & components
│   ├── stores/ router/                   # Pinia stores, Vue Router
│   ├── composables/ utils/ styles/       # Hooks, helpers, CSS
│   ├── i18n/ types/                      # i18n (en/zh), TS types
│   └── __tests__/                        # Frontend tests
├── deploy/                               # Docker Compose variants
│   ├── docker-compose.yml                #   Production
│   ├── docker-compose.dev.yml            #   Development (with build)
│   ├── docker-compose.local.yml          #   Local (pre-built image)
│   └── docker-compose.standalone.yml     #   Standalone (all-in-one)
├── assets/                               # Logos, partner assets
└── tools/                                # check_pnpm_audit_exceptions.py
```

Sibling dependency (same parent directory):

```
tk/                         # Parent directory (NOT a git repo)
├── sub2api/                # This repo
└── new-api/                # QuantumNous/new-api clone (own .git)
```

## Hard Rules

### 1. PostgreSQL Only

All DB code targets PostgreSQL 16+ exclusively. Ent ORM for all data access; raw SQL only in migrations or perf-critical paths.

**NEVER** introduce SQLite or MySQL compatibility.

### 2. Ent Schema Is Source of Truth

Data model changes start in `ent/schema/`. After modification:

1. `go generate ./ent` — regenerate.
2. `git add ent/` — generated code MUST be committed.
3. Update ALL test stubs/mocks implementing changed interfaces.

**NEVER** hand-edit generated files under `ent/` outside of `ent/schema/`.

**Upstream merge:** Large diffs in generated files (for example `ent/mutation.go`) are a normal consequence of schema changes and `go generate ./ent`. Do not try to maintain parallel hand-written fragments of that code—it will be overwritten on regen. Prefer `ent/schema` plus Ent hooks (`ent/hook`) or interceptors for cross-cutting behavior.

### 3. pnpm Only

Frontend uses **pnpm** exclusively. **NEVER** `npm install` or `yarn install`.

- `pnpm-lock.yaml` MUST be committed when `package.json` changes.
- CI runs `pnpm install --frozen-lockfile` — stale lock file breaks the build.

### 4. Cross-Repo Dependency: New API

`backend/go.mod` contains:

```
replace github.com/QuantumNous/new-api => ../../new-api
```

**Required layout:** `new-api` must sit next to `sub2api` under the same parent directory. The `../../new-api` path resolves from `sub2api/backend/` up two levels to the parent, then into `new-api/`.

**Pinned commit (`.new-api-ref` is the single source of truth):**

The repo-root file `.new-api-ref` records the exact `QuantumNous/new-api` commit SHA used by both local dev and CI. `scripts/sync-new-api.sh` and the three workflows (`release.yml`, `backend-ci.yml`, `security-scan.yml`) all read it, so the release Docker image is bit-identical to what is tested locally.

```bash
bash scripts/sync-new-api.sh           # pull sibling clone to the pinned SHA
bash scripts/sync-new-api.sh --check   # CI-style drift check; exit 1 if mismatch
bash scripts/sync-new-api.sh --bump <sha>   # update .new-api-ref + sync
```

**Bumping the pin:** `--bump <sha>` → `make test` → `git add .new-api-ref` → commit. **NEVER** hand-edit hardcoded SHAs in workflows.

**Docker build:** From the parent of `sub2api/`, run `docker build -f sub2api/Dockerfile -t sub2api:local .`, or from `deploy/`: `docker compose -f docker-compose.dev.yml build`. See `Dockerfile` header.

**Constraints:**

- Import only stateless packages: `relay/channel/`*, `relay/common/*`, `dto/*`, `constant/*`, `types/*`, `service/` (affinity). **NEVER** call GORM DB operations from New API code.
- New API integration logic lives in `internal/integration/newapi/`. Keep it there.
- When upstream changes break compilation, fix the bridge — do NOT modify New API from this repo.
- New-api packages may register top-level `flag.Bool` (e.g. `-version`) in their `init()`; check `flag.Lookup` before defining your own to avoid `flag redefined` panics at startup. See `backend/cmd/server/main.go`.

### 5. Upstream Isolation

This repo is a fork of `Wei-Shaw/sub2api`, tracked via the `upstream` remote (`upstream/main`). Minimize diff against upstream:

- TK-specific code goes in scoped packages (`internal/integration/newapi/`, dedicated files).
- For large upstream-owned Go sources (handlers, services, routes), prefer companion files in the same package named `*_tk_*.go` (examples: `gateway_handler_tk_affinity.go`, `setting_service_tk_bridge_passkey_payments.go`, `routes/admin_tk_channel_routes.go`, `routes/gateway_tk_openai_compat_handlers.go`) so the primary file stays close to upstream shape.
- For Vue/admin UI, prefer `*.tk.ts` modules under `frontend/src/constants/` (or composables) for TokenKey-only styling and options; keep upstream-shaped `.vue` files to thin template + import + call.
- When modifying upstream files, prefer **appending** code (new imports + calls) over rewriting existing functions.
- Merge upstream: `git fetch upstream && git merge upstream/main` → resolve → `make test`.
- See `docs/tokenkey_upstream_convergence_plan.md` for the full upstream merge guide and what NOT to modify.

#### 5.x Deletion discipline — default = keep, override; never silent-delete

**Default assumption: an upstream feature stays compiled in.** TokenKey almost always wants to **change defaults / wire new behavior**, not strip community capabilities. Quietly deleting upstream files (handlers, middleware, services, migrations) is the highest-risk form of divergence because:

1. It silently regresses functionality every operator may rely on (e.g. `backend_mode_guard` was deleted in TK once → blocked our own admin-only deployment story until re-adopted).
2. It guarantees recurring **merge conflicts** at every upstream change to the deleted file's call sites (`auth.go`, `payment.go`, `user.go` …).
3. It loses upstream's **tests + docs** for that feature, then we have to rebuild a worse version later.

**Rules:**

- **NEVER** delete an upstream-owned file/method/route to "clean up" or "simplify" — open an issue / PR comment and discuss instead.
- If TK truly does not want a feature, prefer one of these in order:
  1. **Override the default** via migration or `InitializeDefaultSettings` (e.g. `tk_003_default_backend_mode_enabled.sql` flips the user-facing default without touching code).
  2. **Add an admin-toggleable setting** (`SettingKey* + IsXxxEnabled()`) and ship a `*_tk_*.go` companion that short-circuits at the call site.
  3. Last resort, **comment out the registration** with an inline `// TK: disabled because <link to ticket>` — easier to re-enable on merge than a deletion.
- Any PR that net-deletes upstream symbols (functions / route registrations / DB columns) MUST in its description: (a) link the upstream commit being reverted, (b) state the regression cost, (c) list which upstream tests are now skipped or removed.
- A drift detector for "TK-only deletions of upstream files" lives in `git diff --diff-filter=D upstream/main..HEAD -- backend/`. If that command returns anything, the next merge will fight us — re-evaluate.

#### 5.y Forward-looking history & merge discipline

The `main` branch is **immutable history** once pushed. The TK-ahead commits include both linear and merge commits and several `vX.Y.Z` tags pointing into them — rewriting history would orphan tags and break PR audit trails. Going forward:

- **No history rewrites on `main`.** No `git rebase -i` of pushed commits, no `git push --force` to `main`/`master`, no squash-merge of already-merged feature branches.
- **Every TK feature lands via PR** with a clear scope (new file or one upstream-file injection point), reviewed against rule §5 above. Small + frequent beats one giant rebase.
- **PR merge mode is content-typed, not personal preference:**
  - **TK feature / fix / chore PRs** (anything originating from this fork) → GitHub **"Squash and merge"**. The PR becomes **one** commit on `main` whose title = PR title and whose body aggregates the development commits. Rationale: the feature branch's work-in-progress commits (`fix lint`, `typo`, `rebase main`, sync-VERSION housekeeping) carry no long-term audit value once the PR is approved as a unit; collapsing them keeps `git log --oneline --first-parent main` readable and keeps the per-PR diff trivially `git revert`-able.
  - **Upstream merge PRs** (`merge/upstream-YYYYMMDD`) → GitHub **"Create a merge commit"** invoked locally as `git merge --no-ff upstream/main`. Never `--squash`, never `--ff-only`. Rationale: each upstream commit is an external contract reference; squashing them severs the `git log upstream/main..HEAD` audit chain that §5.y depends on.
  - This is **not** a contradiction with "no squash-merge of already-merged feature branches" above: that rule forbids rewriting commits **already on `main`**; the squash here happens **at PR-merge time**, before anything reaches `main`.
- **`git merge-tree upstream/main HEAD` is the pre-merge dry-run.** Run it before any upstream merge to surface conflicts; resolve in a dedicated `merge/upstream-YYYYMMDD` branch, not on `main`.
- **Tag = consolidation point, not a rewrite cue.** When you tag `vX.Y.Z`, all earlier commits become permanent history. If a tag points at a commit with `[skip ci]` (see §9.2), do NOT delete and re-tag — dispatch the workflow manually.
- **Audit cadence:** every merge PR description includes `git log --oneline upstream/main..HEAD | wc -l` (TK ahead count) + `git diff --stat upstream/main..HEAD -- backend/` (top changed files). Use these numbers to decide whether the next batch of TK work should be split into smaller PRs.

#### 5.y.1 Mechanical enforcement (no soft rule without a check)

Per dev-rules §"Hard Constraint Wiring" — every soft rule above MUST have an automated gate. The §5.y enforcement stack:

| Mechanism | Trigger | What it does |
|---|---|---|
| `scripts/check-upstream-drift.sh` | local, on demand (`bash scripts/check-upstream-drift.sh`) | Auto-adds `upstream` remote if missing, fetches both, prints TK ahead/behind counts + the §5.y procedure when behind. Exit codes: `0` = synced, `1` = behind, `2` = git/network failure. Supports `--json` for CI consumption and `--quiet` for hooks. |
| `.github/workflows/upstream-drift-monitor.yml` | every Monday 09:00 UTC + manual dispatch | Calls the same script in `--json` mode. When TK is behind, opens (or updates) a single issue labelled `upstream-drift` with the full commit list, files touched, and the exact 7-line procedure. Auto-closes the issue with a comment when the next merge PR brings the fork back in sync. |
| `.github/workflows/upstream-merge-pr-shape.yml` | any PR whose head branch matches `merge/upstream-*` | Hard gates that fail the PR if any is violated: (a) PR must contain a merge commit whose second parent is reachable from `upstream/main` — squash and ff merges fail this, (b) PR body must include the literal substring `upstream/main..HEAD` so the §5.y audit cadence is present, (c) no commit in the PR may carry literal `[skip ci]` / `[ci skip]` in its message (those silently disable downstream pipelines — see §9.2 / v1.3.0 incident), (d) the newapi sentinel registry (`scripts/newapi-sentinels.json`) is intact after the merge — same script `scripts/preflight.sh` runs locally, so a green local preflight implies a green PR-shape check. |

**Branch protection on `main`** SHOULD list `Upstream Merge PR Shape / validate` as a required status check for `merge/upstream-*` PRs. Configure in repo Settings → Branches → main.

**Trade-off acknowledged:** the drift monitor + PR shape check cannot stop a determined operator from clicking GitHub's "Squash and merge" on a `merge/upstream-*` PR — GitHub does not expose merge-method choice as a webhook. They can only stop the merge PR from passing CI in the wrong shape. Combined with the PR Checklist reviewer reminder above, this brings the path of least resistance to the correct behavior; full lockdown requires GitHub Enterprise org-level branch rules.

#### Convergence & minimal invasion (especially large upstream files)

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

### 6. Interface Method Completeness

Adding a method to any Go interface → search ALL implementations (including test stubs/mocks) → add the method to EVERY one. The project will not compile otherwise.

### 7. No Credentials in Git

`backend/config.yaml`, `deploy/config.yaml`, `.env` are gitignored. **NEVER** commit them.

### 8. Layer Dependencies

```
handler → service → repository → ent
```

**NEVER** import upward (repository must not import handler, service must not import handler).

### 9. Release Discipline (ARM + Tag Triggers)

Both production (`api.tokenkey.dev`) and the test stack (`test-api.tokenkey.dev`) run on **AWS Graviton (`t4g.small`, `arm64`)**, and Release workflow is triggered by `tags: v*`. Two pitfalls have already broken prod once each — both are now **hard rules**:

#### 9.1 `simple_release` MUST stay `false`

`.github/workflows/release.yml` exposes a `workflow_dispatch` input `simple_release`. **DEFAULT MUST REMAIN `false`.**

- `simple_release=true` → GoReleaser builds **`linux/amd64` only**, then **overwrites the shared tags** `:latest`, `:X`, `:X.Y`, `:X.Y.Z` with that single-arch image.
- Any ARM host pulling `:latest` (or any overwritten tag) will crash immediately with `exec format error` on `docker compose up`. **Both our hosts are ARM** — this is a guaranteed prod outage.
- **NEVER** flip the default to `true`, **NEVER** dispatch with `simple_release=true` unless every consumer has been verified amd64.
- If accidentally dispatched: re-dispatch the **same** tag with `simple_release=false` immediately to rewrite the multi-arch manifest.

The release workflow already prints a `::warning::` and a Step Summary banner when `simple_release=true` — do not silence it; treat it as a stop-the-line signal.

#### 9.2 `VERSION` bump commits MUST NOT carry `[skip ci]`

Release is triggered by `tag push`, but GitHub evaluates `[skip ci]` against the **commit message of the commit the tag points at**. So:

```
git commit -m "chore: bump VERSION to X.Y.Z [skip ci]"   # ← BAD
git tag vX.Y.Z
git push origin main vX.Y.Z                              # release.yml is silently SKIPPED
```

→ No image is built, prod/test deploys go stale, and the only recovery is a manual `gh workflow run release.yml -f tag=vX.Y.Z`.

**Rule:** when bumping `backend/cmd/server/VERSION` by hand for a release, the commit message MUST NOT contain `[skip ci]` / `[ci skip]`. **The trap goes further than the literal commit body**: the v1.3.0 release was silently broken because the squash-merge commit body contained the explanatory phrase _"this commit deliberately omits `[skip ci]`"_ — GitHub matched the literal substring inside the explanation and skipped `release.yml` anyway. Discussing the marker in commit text counts as carrying it. The **only** commits in this repo that may include `[skip ci]` are the auto-generated **`sync-version-file` writeback commits** produced by `release.yml` itself (those need `[skip ci]` to break the release → sync → release loop).

**Mechanical enforcement:** use `bash scripts/release-tag.sh vX.Y.Z` instead of `git tag` directly. The helper validates that the HEAD commit message does NOT contain literal `[skip ci]` / `[ci skip]` anywhere, that `backend/cmd/server/VERSION` matches the tag, that the tag does not already exist, and that local `main` is in sync with `origin/main`; only then does it create the annotated tag and push. If the [skip ci] check fires it prints both fix paths (reword commit / use `gh workflow run` recovery dispatch). The `merge/upstream-*` PR shape workflow (§5.y.1) enforces the same rule on every commit in upstream-merge PRs as a defense in depth.

See `deploy/aws/README.md` § "发版纪律（两条铁律）" for the operator-facing version of these two rules.

### 10. Dev-rules Submodule (Single Source of Truth for Process Rules)

This repo consumes process/quality rules from `github.com/youxuanxue/dev-rules` as
a git submodule at `dev-rules/`. The full convention is in
`dev-rules/rules/dev-rules-convention.mdc` (synced to `.cursor/rules/`); this
section only records sub2api-specific choices.

- **`scripts/preflight.sh` is a thin wrapper, not a re-implementation.**
  All generic checks (branch naming, submodule pointer, `.cursor/rules`
  drift, agent contract drift, story/test alignment, `docs/approved`
  discipline, approved-doc invariants R1-R5, doc-stat drift, cloud-agent
  env consistency) are **delegated** to `dev-rules/templates/preflight.sh`
  — the wrapper just invokes it. The wrapper exists ONLY to host
  **sub2api-specific checks**:
  - **newapi compat-pool drift** — guards the **forward-drift** failure
    mode that triggered `docs/approved/newapi-as-fifth-platform.md`. Any
    new scheduler/gateway caller must use `IsOpenAICompatPoolMember` /
    `OpenAICompatPlatforms` instead of bare `PlatformOpenAI` / `IsOpenAI`.
  - **newapi sentinel registry** — guards the **backward-drift** failure
    mode (a load-bearing fifth-platform file/symbol gets silently deleted
    by an upstream merge or refactor). Source of truth is
    `scripts/newapi-sentinels.json` (declarative `path` + `must_contain`
    list with rationale per entry). `scripts/check-newapi-sentinels.py`
    reads the registry; the same script is invoked by
    `.github/workflows/upstream-merge-pr-shape.yml`, so a green local
    preflight implies a green merge-PR check. Adding a new load-bearing
    surface for newapi MUST add a sentinel entry in the same commit. See
    `docs/approved/newapi-as-fifth-platform.md` § 12 for the registry
    doctrine (categories, double-trigger, evolution discipline).
  When adding a new sub2api-only check, append it to `scripts/preflight.sh`
  (NEVER edit the dev-rules template — it is shared across all consumer
  projects). If the check turns out to be useful for more than just
  sub2api, lift it into dev-rules and remove the local copy. The git
  pre-commit hook installed by `dev-rules/templates/install-hooks.sh`
  prefers `scripts/preflight.sh` when present and falls back to the
  dev-rules template otherwise.
- **CI must check out submodules.** All workflow jobs that run
  `dev-rules/...` (preflight, contract drift, etc.) must use
  `actions/checkout@v6` with `submodules: recursive`.
- **Editing rules:** edit `dev-rules/rules/*.mdc` (or `commands/`,
  `global/`), then `dev-rules/sync.sh --local`, commit submodule first, push
  submodule, then commit parent (`dev-rules` pointer + `.cursor/rules/`).
  The "submodule first" order is enforced by preflight § 2 (warns on offline,
  fails if SHA missing locally) and by `dev-rules/rules/dev-rules-convention.mdc`.

## Agent skill: prod release + AWS Stage0 deploy

Full checklist (**同步 main → VERSION bump / 打 tag → `release.yml` → `deploy-stage0` prod → 真实烟测**) lives in [.cursor/skills/tokenkey-prod-release-deploy/SKILL.md](.cursor/skills/tokenkey-prod-release-deploy/SKILL.md). The same file is symlinked at `.claude/skills/tokenkey-prod-release-deploy/SKILL.md` so **Claude Code** picks it up as a project skill alongside **Cursor** (`.cursor/skills/…`).

## Key Reference

### Current Gateway Flow

```
HTTP Request → Auth (JWT/APIKey) → Account Scheduling (sticky/load-aware)
 → Platform-specific forwarding (Claude / OpenAI / Gemini / Antigravity / New API fifth platform `newapi`)
 → Usage recording + quota deduction
```

The fifth platform **`newapi`** is a first-class account/group platform (not an add-on card on the other four): it uses OpenAI-compatible gateway routes and the New API **adaptor** layer in `internal/relay/bridge` when `channel_type > 0`. The `internal/integration/newapi/` package provides the channel-type catalog, affinity helpers, upstream model metadata helpers, and other `newapi`-specific bridge support required by TokenKey's fifth-platform flow.

**Image and video generation surfaces** ride on the same `newapi` (and `openai`) compat-pool routing:

- **Sync image** — `POST /v1/images/generations` (and `POST /images/generations` alias) via `bridge.RunImageRelay` and `bridge.DispatchImageGenerations`. Volcengine `channel_type=45` (Doubao Seedream) is supported through the upstream `volcengine` adapter.
- **Async video** — `POST /v1/video/generations` + `GET /v1/video/generations/:task_id` (and the OpenAI-compat aliases `POST /v1/videos` + `GET /v1/videos/:task_id`, plus their no-prefix variants). Submit returns a TokenKey-issued `task_id` (prefix `vt_`); subsequent polls hit the upstream task adapter pinned at submit time. Supported channel types are auto-derived from `relay.GetTaskAdaptor` — currently `45` (VolcEngine, Doubao Seedance) and `54` (DoubaoVideo). Routing metadata lives in `service.VideoTaskCache` (Redis primary, in-memory fallback for single-replica dev). Default record TTL is 24h; terminal status (`succeeded`/`failed`) deletes the record. Adding a new task adapter upstream requires no TK code changes — the `IsVideoSupportedChannelType` predicate sees the new channel type as soon as the upstream adapter map registers it.

**Scheduling-pool semantics (per `docs/approved/newapi-as-fifth-platform.md`, shipped):** the OpenAI-compatible pool now partitions strictly by `group.platform`. `openai` groups schedule only `openai` accounts; `newapi` groups schedule only `newapi` accounts (with `channel_type > 0`). The canonical predicate is `account.IsOpenAICompatPoolMember(groupPlatform)` in `backend/internal/service/account_tk_compat_pool.go`, used by load-balance, sticky-session, and recheck paths in `openai_account_scheduler.go` / `openai_gateway_service.go`. Cross-platform fallback is forbidden — an empty pool surfaces an error. Sticky-session bindings whose bound account drifted to the wrong platform (or whose `channel_type` was reset to 0) are invalidated and the request fails over to load-balance. `messages_dispatch_model_config` is preserved for `openai` and `newapi` groups, cleared for `anthropic` / `gemini` / `antigravity` (predicate: `isOpenAICompatPlatformGroup`). Sticky routing (per `docs/approved/sticky-routing.md`, shipped) layers above this pool to optimize prompt-cache hit rates within each platform's bucket.

### Fusion / Bridge Plans

Treat `internal/integration/newapi/` and `internal/relay/bridge/` as the implementation source of truth; any external planning docs may lag the code.

### PR Checklist

- `go test -tags=unit ./...` passes
- `go test -tags=integration ./...` passes
- `golangci-lint run ./...` — no new issues
- `pnpm-lock.yaml` in sync (if `package.json` changed)
- Test stubs complete (if interfaces changed)
- Ent generated code committed (if schema changed)
- `go build ./...` succeeds (cross-repo dependency compiles)
- If bumping `backend/cmd/server/VERSION` for a release: commit message contains **no** literal `[skip ci]` / `[ci skip]` anywhere (rule 9.2 — discussion of the marker counts as carrying it). Use `bash scripts/release-tag.sh vX.Y.Z` to push the tag — it enforces this mechanically.
- If touching `.github/workflows/release.yml`: `simple_release` default stays `false`; warning banner step is intact (rule 9.1)
- If the PR deletes any upstream-owned file/method/route: PR description contains the (a)/(b)/(c) justification block from rule §5.x; otherwise change to "override default" or "disable via setting" instead
- After upstream merge: PR body includes `git log --oneline upstream/main..HEAD | wc -l` and the top-5 lines of `git diff --stat upstream/main..HEAD -- backend/` (rule §5.y audit cadence). The `Upstream Merge PR Shape` workflow (§5.y.1) enforces this automatically — fix any failures it reports rather than ignoring them.
- Drift check: before opening any non-trivial PR, run `bash scripts/check-upstream-drift.sh`. If TK is behind upstream/main, pause and either land the upstream merge first or document why this PR ships out of order.
- Reviewer picks the GitHub merge button per rule §5.y: **Squash and merge** for TK-originated PRs (feature / fix / chore / docs), **Create a merge commit** for `merge/upstream-*` PRs. Never use **Rebase and merge** on `main`.
- If the PR touches `dev-rules/` (submodule pointer bump or `.cursor/rules/` resync): per rule §10, the dev-rules submodule MUST be pushed first; this PR's CI `preflight` job will fail otherwise (the dev-rules SHA in `.gitmodules` won't be reachable on `origin/main`).
