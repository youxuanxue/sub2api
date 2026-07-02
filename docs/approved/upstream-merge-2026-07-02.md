---
title: Upstream Merge 2026-07-02 High-Risk Anchor
status: approved
approved_by: xuejiao (PR #1154 merge)
approved_at: 2026-07-02
created: 2026-07-02
authors: [agent]
risk: high
related_prs: [1154]
related_commits: [39c769ee2dade9b41f9c3f64051909a131cdd8db]
---

# Upstream Merge 2026-07-02 High-Risk Anchor

## Scope

This anchor covers the `merge/upstream-2026-07-02` branch while it imports
`Wei-Shaw/sub2api` upstream changes and current TokenKey `origin/main` fixes.

The diff touches high-risk migration paths because the upstream and TokenKey
mainline changes include account shadow, Grok quota, content moderation, and
group peak-rate migrations. The merge PR remains the human review gate for
accepting those migration changes into `main`.

## Review Points

- Migrations remain append-only and pass the repository migration safety gates.
- TokenKey schema extensions are regenerated through Ent before merge.
- Frontend embedded assets are regenerated after conflict resolution.
- TokenKey sentinel registries remain in sync with the touched load-bearing
  surfaces.

## Validation

- `bash scripts/preflight.sh`
- `go -C backend test -tags=unit ./internal/service ./internal/handler/... ./internal/repository ./internal/server/... -count=1`
- `pnpm --dir frontend typecheck`
- `pnpm --dir frontend run build`
