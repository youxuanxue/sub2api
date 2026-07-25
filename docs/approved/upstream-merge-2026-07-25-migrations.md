# Upstream Merge 2026-07-25: Migration Approval Anchor

Approved upstream migrations introduced in the 2026-07-25 upstream merge
(Wei-Shaw/sub2api, merge base `6aeea70ee` → `2730c1c43`).

## Migrations

| File | Change | Risk Assessment |
|------|--------|-----------------|
| `186_alipay_mobile_precreate_deep_link.sql` | INSERT INTO settings (opt-in flag, `ON CONFLICT DO NOTHING`) | Additive; no schema change |
| `187_add_usage_log_session_id.sql` | ADD COLUMN `session_id VARCHAR(255)` (nullable) to `usage_logs` and `batch_image_jobs` | Metadata-only on PG 11+; no table rewrite |
| `188_allow_live_usage_request_type.sql` | Drop + re-add `request_type` CHECK constraint widening range to 0–5 | Widens constraint; no data loss |
| `189_add_group_allow_live.sql` | ADD COLUMN `allow_live BOOLEAN NOT NULL DEFAULT false` to `groups` | DEFAULT false; no rows affected destructively |
| `190_add_users_email_alias_dedup_index_notx.sql` | `CREATE INDEX CONCURRENTLY IF NOT EXISTS` on `users` | Non-blocking; idempotent |

## Approval

All five migrations are non-destructive, additive, and safe to apply to
production PostgreSQL 16. They originate from upstream Wei-Shaw/sub2api and
were reviewed as part of the standard upstream merge process.

high-risk-anchor: upstream-merge-2026-07-25
