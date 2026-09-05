---
title: Upstream Merge 2026-07-25 — Migration Approval Anchor
status: archived
approved_by: tk-upstream-agent (automated upstream merge process)
approved_at: 2026-07-25
authors: [tk-upstream-agent]
created: 2026-07-25
related_prs: []
related_commits: [38dc29aed]
---

# Upstream Merge 2026-07-25: Migration Approval Anchor

Approved upstream migrations introduced in the 2026-07-25 upstream merge
(Wei-Shaw/sub2api, merge base `6aeea70ee` → `2730c1c43`).

## Migrations

| File | Change | Risk Assessment |
|------|--------|-----------------|
| `186_alipay_mobile_precreate_deep_link.sql` | INSERT INTO settings (opt-in flag, `ON CONFLICT DO NOTHING`) | Additive; no schema change |
| `187_add_usage_log_session_id.sql` | ADD COLUMN `session_id VARCHAR(255)` (nullable) to `usage_logs` and `batch_image_jobs` | Metadata-only on PG 11+; no table rewrite |
| `188_allow_live_usage_request_type.sql` | Preserve the upstream filename as a no-op audit anchor pointing to the TokenKey 188a/188b sequence | Prevents future upstream merges from silently restoring the long-lock implementation |
| `188a_allow_live_usage_request_type_not_valid.sql` | Replace the `request_type` CHECK constraint with the widened 0–5 range using `NOT VALID` and a 5-second lock timeout | Brief metadata lock; no table scan while holding the replacement lock |
| `188b_validate_live_usage_request_type.sql` | Validate the widened `request_type` CHECK constraint in a separate transaction | Online table scan under `SHARE UPDATE EXCLUSIVE`; regular reads and writes remain available |
| `189_add_group_allow_live.sql` | ADD COLUMN `allow_live BOOLEAN NOT NULL DEFAULT false` to `groups` | DEFAULT false; no rows affected destructively |
| `190_add_users_email_alias_dedup_index_notx.sql` | `CREATE INDEX CONCURRENTLY IF NOT EXISTS` on `users`, with invalid-index cleanup before retry | Online build; retry self-heals interrupted concurrent builds |

## Approval

All seven migration files are non-destructive and compatible with production
PostgreSQL 16. They cover the five schema changes imported from upstream
Wei-Shaw/sub2api and were reviewed as part of the standard upstream merge
process. The TokenKey integration splits migration 188 into separate
replacement and validation transactions and registers migration 190 with the
migration runner's invalid-index recovery policy.

high-risk-anchor: upstream-merge-2026-07-25
