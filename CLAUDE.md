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
| CI | GitHub Actions (`backend-ci`, `release`) | `.github/workflows/` |
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

Backend `backend/` (Go: `handler` → `service` → `repository` → `ent`), frontend `frontend/` (Vue 3 + pnpm), deploy `deploy/`. Sibling `new-api/` clone required at `../../new-api` (see §4). Key paths: `backend/internal/{handler,service,integration/newapi,relay/bridge}`, `frontend/src/{views,composables,api}`, `deploy/docker-compose*.yml`.

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

The repo-root file `.new-api-ref` records the exact `QuantumNous/new-api` commit SHA used by both local dev and CI. `scripts/upstream/sync-new-api.sh` and the two workflows (`release.yml`, `backend-ci.yml`) both read it, so the release Docker image is bit-identical to what is tested locally.

```bash
bash scripts/upstream/sync-new-api.sh           # pull sibling clone to the pinned SHA
bash scripts/upstream/sync-new-api.sh --check   # CI-style drift check; exit 1 if mismatch
bash scripts/upstream/sync-new-api.sh --bump <sha>   # update .new-api-ref + sync
```

**Bumping the pin:** `--bump <sha>` → `make test` → `git add .new-api-ref` → commit. **NEVER** hand-edit hardcoded SHAs in workflows.

**Docker build:** From the parent of `sub2api/`, run `docker build -f sub2api/Dockerfile -t sub2api:local .`, or from `deploy/`: `docker compose -f docker-compose.dev.yml build`. See `Dockerfile` header.

**Constraints:**

- Import only stateless packages: `relay/channel/`*, `relay/common/*`, `dto/*`, `constant/*`, `types/*`, `service/` (affinity). **NEVER** call GORM DB operations from New API code.
- New API integration logic lives in `internal/integration/newapi/`. Keep it there.
- When upstream changes break compilation, fix the bridge — do NOT modify New API from this repo.
- New-api packages may register top-level `flag.Bool` (e.g. `-version`) in their `init()`; check `flag.Lookup` before defining your own to avoid `flag redefined` panics at startup. See `backend/cmd/server/main.go`.

**Worktrees + the `../../new-api` sibling (turnkey bootstrap):**

Default to an **isolated git worktree** for any commit-bearing task — sharing the primary checkout's single mutable HEAD/index with a parallel agent (e.g. a twin worker) lets one `git checkout` land your commits on the wrong branch. A worktree created at a deep path (`EnterWorktree` → `.claude/worktrees/<name>/`) breaks the `replace … => ../../new-api` resolution, which is exactly the friction that makes people skip worktrees. Make it free instead of skipping it:

- Run `bash dev-rules/templates/worktree-bootstrap.sh <worktree_dir>` after creating a worktree. It inits the `dev-rules` submodule and runs this repo's `scripts/worktree-bootstrap-hook.sh`, which symlinks the deep-path-resolved `new-api` location to the real sibling clone so `go build` / preflight work.
- Sibling-placed worktrees (e.g. twin's `<parent>/<repo>-twin-*`) resolve `../../new-api` natively — the hook is a no-op there.
- The real sibling clone is still located/synced by `scripts/upstream/sync-new-api.sh` (`.new-api-ref`); the hook only fixes path resolution, never the pin.

### 5. Upstream Isolation

This repo is a fork of `Wei-Shaw/sub2api`, tracked via the `upstream` remote (`upstream/main`). Minimize diff against upstream:

- TK-specific code goes in scoped packages (`internal/integration/newapi/`, dedicated files).
- For large upstream-owned Go sources (handlers, services, routes), prefer companion files in the same package named `*_tk_*.go` (examples: `gateway_handler_tk_affinity.go`, `setting_service_tk_bridge_passkey_payments.go`, `routes/admin_tk_channel_routes.go`, `routes/gateway_tk_openai_compat_handlers.go`) so the primary file stays close to upstream shape.
- For Vue/admin UI, prefer `*.tk.ts` modules under `frontend/src/constants/` (or composables) for TokenKey-only styling and options; keep upstream-shaped `.vue` files to thin template + import + call.
- When modifying upstream files, prefer **appending** code (new imports + calls) over rewriting existing functions.
- Merge upstream: `git fetch upstream && git merge upstream/main` → resolve → `make test`.
- See `docs/global/tokenkey-opc-transformation-plan.md` for the upstream convergence boundary and what NOT to modify.

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

`main` history is immutable once pushed. TK PRs → **Squash and merge**; upstream merges (`merge/upstream-YYYYMMDD`) → **`git merge --no-ff upstream/main`** then **Create a merge commit** on GitHub (never squash/ff). Pre-merge dry-run: `git merge-tree upstream/main HEAD`. Merge PR body must include `upstream/main..HEAD` audit cadence. Full rules: [`docs/global/upstream-merge-discipline.md`](docs/global/upstream-merge-discipline.md).

#### 5.y.1 Mechanical enforcement (no soft rule without a check)

Gates: `scripts/upstream/check-drift.sh`, `scripts/checks/upstream-override-marker.py`, `scripts/checks/main-ancestry-anchor.py`; workflows `upstream-merge-pr-shape.yml`, `main-ancestry-guard.yml`. `.main-ancestry-anchor` is a one-way ratchet — advance only via PR with `main-ancestry-anchor-advance` marker. Full mechanism table + minimal-invasion patterns: [`docs/global/upstream-merge-discipline.md`](docs/global/upstream-merge-discipline.md).

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

Production deployment (`api.tokenkey.dev`) runs on **AWS Graviton (`arm64`)**, and Release workflow is triggered by `tags: v*`. Two pitfalls have already broken prod once each — both are now **hard rules**:

#### 9.1 `simple_release` MUST stay `false`

`.github/workflows/release.yml` exposes a `workflow_dispatch` input `simple_release`. **DEFAULT MUST REMAIN `false`.**

- `simple_release=true` → GoReleaser builds **`linux/amd64` only**, then **overwrites the shared tags** `:latest`, `:X`, `:X.Y`, `:X.Y.Z` with that single-arch image.
- Any ARM host pulling `:latest` (or any overwritten tag) will crash immediately with `exec format error` on `docker compose up`. **Prod and Edge Stage0 hosts today are ARM** — this is a guaranteed outage for those stacks.
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

→ No image is built, prod deploy goes stale, and the only recovery is a manual `gh workflow run release.yml -f tag=vX.Y.Z`.

**Rule:** when bumping `backend/cmd/server/VERSION` by hand for a release, the commit message MUST NOT contain `[skip ci]` / `[ci skip]`. **Discussing the marker counts as carrying it** — GitHub matches the literal substring even inside an explanation. The **only** commits that may include `[skip ci]` are the auto-generated **`sync-version-file` writeback commits** produced by `release.yml` itself (needed to break the release → sync → release loop).

**Mechanical enforcement:** use `bash scripts/release-tag.sh vX.Y.Z` instead of `git tag` directly. It validates: HEAD commit message carries no literal `[skip ci]` / `[ci skip]`, `backend/cmd/server/VERSION` matches the tag, the tag doesn't already exist, and local `main` is in sync with `origin/main` — then creates the annotated tag and pushes. The `merge/upstream-*` PR shape workflow (§5.y.1) enforces the same rule on first-parent commits of upstream-merge PRs (imported upstream messages exempt).

**Discussion-of-marker discipline.** Two squash-merge incidents (v1.3.0; PR #312, 2026-05-19) showed that referencing the bracketed marker even in passing — to *explain* the rule — lands it in the squash-merge commit body, where GitHub still matches the substring and skips `release.yml`. **Rule:** any PR title, PR body, or PR commit message that lands on `main` MUST use unbracketed forms when discussing these markers — `skip-ci`, `ci-skip`, or `skip ci` (no brackets). CLAUDE.md text itself is exempt (file contents never reach the commit-message context). `main-ancestry-guard.yml` Check 3 enforces this on every PR targeting `main`, with an inline hint to switch to the hyphen form.

See `deploy/aws/README.md` § "发版纪律（两条铁律）" for the operator-facing version of these two rules.

### 10. Dev-rules Submodule (Single Source of Truth for Process Rules)

This repo consumes process/quality rules from `github.com/youxuanxue/dev-rules` as
a git submodule at `dev-rules/`. The full convention is in
`dev-rules/rules/dev-rules-convention.mdc` (synced to `.cursor/rules/`); this
section only records sub2api-specific choices.

- **`scripts/preflight.sh` is a thin wrapper** delegating generic checks to `dev-rules/templates/preflight.sh`. Sub2api-specific checks only: **newapi compat-pool drift** (`IsOpenAICompatPoolMember` / `OpenAICompatPlatforms`) and **sentinel registry** (`scripts/sentinels/newapi.json` + `check-newapi.py`; new hotspot files need anchors or `sentinel-registry-reviewed` — see `docs/approved/newapi-as-fifth-platform.md` §12). Append new checks to `scripts/preflight.sh`, never the dev-rules template.
- **CI must check out submodules** (`actions/checkout@v6` with `submodules: recursive`).
- **Editing rules:** edit `dev-rules/rules/*.mdc`, `dev-rules/sync.sh --local`, commit submodule first + push, then parent (`dev-rules` pointer + `.cursor/rules/`).

## Studio SSOT（`/studio` Image / Video / BakeOff）

Owner table + extension rules: [`docs/global/agent-reference.md`](docs/global/agent-reference.md#studio-ssot-studio-image--video--bakeoff). 宪法原则见 `dev-rules/global/CLAUDE.md` §5.1。

## Agent skills（Cursor / Claude Code）

技能正文在 `.cursor/skills/<name>/SKILL.md`；`.claude/skills` 仅为 symlink。完整索引见 [`AGENTS.md`](AGENTS.md)。常用入口：modelops → `tokenkey-modelops-planner`；Stage0 发版 → `tokenkey-stage0-release-rollout`；cc/codex 指纹 → `tokenkey-cc-fingerprint-alignment` / `tokenkey-codex-fingerprint-alignment`。

## Key Reference

Gateway flow, prod↔edge topology, disaster recovery, full PR checklist: [`docs/global/agent-reference.md`](docs/global/agent-reference.md).

Model serving SSOT (prod `model_mapping` + catalog/pricing alignment; edge keeps empty mapping; official upstream aliases display when priced+servable): [`docs/global/agent-reference.md`](docs/global/agent-reference.md#model-serving-ssot-model_mapping-catalog-prod-vs-edge).

Treat `internal/integration/newapi/` and `internal/relay/bridge/` as implementation source of truth; external planning docs may lag the code.

**Before push:** run `./scripts/preflight.sh` + `make test`. PR checklist detail in agent-reference doc above.
