# CLAUDE.md

Always-on guidance for Claude Code in this repo. Keep this file thin — long form
is overflow, Read on demand. Process rules live in `.cursor/rules/*.mdc` (not
auto-loaded here). Skills index: [`AGENTS.md`](AGENTS.md).

Overflow:

- Hard-rule detail (§4 / §5.x / §9 / account usage): [`docs/global/claude-hard-rules.md`](docs/global/claude-hard-rules.md)
- Gateway / Studio / PR checklist / topology: [`docs/global/agent-reference.md`](docs/global/agent-reference.md)
- Upstream merge history & gates (§5.y): [`docs/global/upstream-merge-discipline.md`](docs/global/upstream-merge-discipline.md)

## Project

TokenKey (TK): AI API gateway for subscription quota distribution. Fork of
[Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api); New API relay adaptors
via Go module import.

| Area | Stack |
| ---- | ----- |
| Backend | Go 1.26+, Gin, Ent, Wire — `backend/` |
| Frontend | Vue 3, Vite, TS, Pinia, Tailwind, **pnpm** — `frontend/` |
| Data | PostgreSQL 16+ only; Redis 7+ |
| Deploy / CI | `deploy/`; GitHub Actions; golangci-lint v2 |

```bash
make build && make test
# backend/: go test -tags=unit|integration ./... ; go generate ./ent ; go generate ./cmd/server
# frontend/: pnpm install | pnpm dev | pnpm build | pnpm lint:check && pnpm typecheck
```

Layering: `handler → service → repository → ent`. Key paths:
`backend/internal/{handler,service,integration/newapi,relay/bridge}`,
`frontend/src/{views,composables,api}`. Sibling `new-api` at `../../new-api` (§4).

## Architecture pointers

- **upstream-output-limits-ssot:**
  `backend/internal/service/upstream_output_limits_tk.go` — omit at dispatch, never
  during candidate admission. Contract:
  [`docs/approved/cursor-oauth-service.md`](docs/approved/cursor-oauth-service.md#output-limit-compatibility).
- **candidate-eligibility-ssot:** owners only in
  [`docs/approved/candidate-eligibility-ssot.md`](docs/approved/candidate-eligibility-ssot.md)
  §Implementation/Owners (+ `scripts/sentinels/gateway-tk.json`). Do not copy the
  owner list here or into `AGENTS.md`. Execution uses the actual account and Plan,
  never the billing group's platform.

## Hard Rules

### 1. PostgreSQL Only

Business persistence = PostgreSQL 16+ / Ent only. **NEVER** add SQLite/MySQL for
business data. QA Bundle's temporary SQLite spill
([`docs/approved/qa-bundle-session-export.md`](docs/approved/qa-bundle-session-export.md))
is not a second business DB.

### 2. Ent Schema Is Source of Truth

Change `ent/schema/` → `go generate ./ent` → commit generated `ent/` → update every
interface stub/mock. **NEVER** hand-edit generated files outside `ent/schema/`.
Large generated diffs on upstream merge are normal; prefer hooks/interceptors over
hand-written fragments of generated code.

### 3. pnpm Only

Frontend: **pnpm** only. Commit `pnpm-lock.yaml` with `package.json` changes. CI
uses `pnpm install --frozen-lockfile`.

### 4. Cross-Repo Dependency: New API

`replace … => ../../new-api`; pin in `.new-api-ref`; sync via
`bash scripts/upstream/sync-new-api.sh`. Import only stateless new-api packages;
TK bridge code stays in `internal/integration/newapi/`. Deep worktrees need
`worktree-bootstrap.sh`. **Detail:**
[`docs/global/claude-hard-rules.md`](docs/global/claude-hard-rules.md#4-cross-repo-dependency-new-api).

### 5. Upstream Isolation

Minimize diff vs `upstream/main`. TK logic in scoped packages / `*_tk_*.go` /
`*.tk.ts`; prefer append over rewrite. Convergence boundary:
[`docs/global/tokenkey-opc-transformation-plan.md`](docs/global/tokenkey-opc-transformation-plan.md).

#### 5.x Deletion discipline

Default = **keep** upstream features; override defaults or toggle-disable — **never
silent-delete**. **Detail:**
[`docs/global/claude-hard-rules.md`](docs/global/claude-hard-rules.md#5x-deletion-discipline--default--keep-override-never-silent-delete).

#### 5.y / 5.y.1 History & merge gates

`main` immutable once pushed. TK PRs → Squash; `merge/upstream-*` → merge commit
(`--no-ff`). Gates and patterns:
[`docs/global/upstream-merge-discipline.md`](docs/global/upstream-merge-discipline.md).

### 6. Interface Method Completeness

New interface method → update **every** implementation including test stubs/mocks.

### 7. No Credentials in Git

Never commit `backend/config.yaml`, `deploy/config.yaml`, `.env`.

### 8. Layer Dependencies

`handler → service → repository → ent`. **NEVER** import upward.

### 9. Release Discipline (ARM + Tag Triggers)

Prod/edges are **arm64**. **`simple_release` default stays `false`** (§9.1).
VERSION bump commits must not carry bracketed skip-ci markers; use
`bash scripts/release-tag.sh vX.Y.Z` (§9.2). Discuss markers as `skip-ci` /
`ci-skip` (no brackets) in PR text that lands on `main`. **Detail:**
[`docs/global/claude-hard-rules.md`](docs/global/claude-hard-rules.md#9-release-discipline-arm--tag-triggers).

### 10. Dev-rules Submodule

Process SSOT: `dev-rules/` → synced `.cursor/rules/`.
`scripts/preflight.sh` wraps the template + TK checks (newapi compat-pool,
sentinel registry). Edit rules in submodule first, `sync.sh --local`, commit
submodule then parent. CI must `submodules: recursive`.

## SSOT index (pointers only)

| Topic | Owner |
| ----- | ----- |
| Catalog / pricing / model delivery | [`docs/approved/pricing-serving-single-source-of-truth.md`](docs/approved/pricing-serving-single-source-of-truth.md); nav in [`agent-reference.md`](docs/global/agent-reference.md#model-serving-ssot--模型交付-ssot) |
| Account usage UI / NewAPI windows | [`claude-hard-rules.md`](docs/global/claude-hard-rules.md#account-usage-ssot-detail) |
| Studio Image / Video / BakeOff | [`agent-reference.md`](docs/global/agent-reference.md#studio-ssot-studio-image--video--bakeoff) |
| Client-closed 499 | [`docs/approved/client-closed-499-ssot.md`](docs/approved/client-closed-499-ssot.md) — owner `client_closed_request_tk.go` |
| Trajectory / QA Bundle export | [`docs/approved/qa-bundle-session-export.md`](docs/approved/qa-bundle-session-export.md) |
| Skills | `.cursor/skills/*/SKILL.md`; `.claude/skills` → symlink; index [`AGENTS.md`](AGENTS.md) |

Model/catalog tests derive sets from SSOT owners — do not hand-maintain duplicate
model lists. Implementation truth for New API bridge:
`internal/integration/newapi/` + `internal/relay/bridge/`.

**Before push:** `./scripts/preflight.sh` + `make test`.
