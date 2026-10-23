---
title: Upstream Merge 2026-10-22 Migration Approval Anchor
status: shipped
approved_by: "用户（本会话：同意合并。）"
approved_at: 2026-10-22
authors: [tk-upstream-agent]
created: 2026-10-22
related_prs: [2796]
related_commits: [24d136d9da8533c6c094e31e2d90d09990232022]
---

# Upstream Merge 2026-10-22: Migration Approval Anchor

Migrations absorbed from `upstream/main@dba770d56` landed on `main` as
merge commit `24d136d9d` via PR #2796. This session approved that merge.

## Migrations

| File | Change | Risk |
|------|--------|------|
| `242_drop_platform_check_constraints.sql` | Drop Ent/Postgres platform CHECK constraints so catalog-driven platforms (command_code/cline + TK newapi/kiro) can persist | DDL on account/group platform columns; empty/defaultable on Stage0; annotated `bluegreen-safe-destructive-ok` |
| `242_group_i18n.sql` | ADD COLUMN NOT NULL for group i18n JSON | Expand-only; annotated `bluegreen-safe-destructive-ok` |
| `243_gemini_cached_contents.sql` | Create gemini cached-content tables | Additive schema for MH gemini cache surface |
| `244_gemini_cached_contents_channel_usage.sql` | ADD COLUMN NOT NULL channel usage on cached contents | Expand-only; annotated `bluegreen-safe-destructive-ok` |
| `245_gemini_cached_contents_mutation_lease.sql` | Mutation lease columns for cache workers | Additive |
| `246_enable_gemini_image_generation_groups.sql` | No-op. Does not flip existing `allow_image_generation` | TK groups default the switch off while native Gemini image traffic already works; rewriting it on would also open OpenAI image calls. Gate stays off via `tkGeminiImageGroupSwitchRetroactive` |

## TokenKey invariants preserved alongside these migrations

- `domain.PlatformNewAPI` / `PlatformKiro` remain in `platforms.go` and ConcretePlatformIDs.
- Admin group Platform `oneof` retains `newapi kiro grok` (+ MH `command_code`/`cline`).
- Frontend `CONCRETE_PLATFORM_OPTIONS` / `...CONCRETE_PLATFORM_OPTIONS` sentinel spread retained.
- `go.mod` keeps `replace … => ../../new-api`.

## Mechanical Evidence (pre-merge)

```bash
./scripts/preflight.sh
go -C backend test -tags=unit ./internal/service/ -count=1 \
  -run 'GeminiCachedContentNotFound|GeminiCompatThinking|GeminiErrorPolicy|GeminiVertex|OpenAICursor|OpenAIModelRetirement|OpenAIOauthWebSearch|OpenAIResponsesCreatedAt|PaymentNotificationConfirm'
```

high-risk-anchor: upstream-merge-2026-10-22
