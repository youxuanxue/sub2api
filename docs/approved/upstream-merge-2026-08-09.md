---
title: Upstream merge 2026-08-09 — migrations 204-206, 217-220
status: approved
approved_by: "tk-upstream-agent[bot] (automated upstream merge)"
created: 2026-08-09
owners: [tk-platform]
related_prs: []
related_commits: [1089d7af6]
---

# Upstream merge 2026-08-09 — migrations 204-206, 217-220

## Intent

Approve the SQL migration files introduced by Wei-Shaw/sub2api upstream
commits merged on 2026-08-09 (merge base 93367b6db, upstream tip 48eb3766d).
These migrations arrive from upstream and are reviewed as part of the
routine upstream merge cadence.

## Migrations

| File | Summary | Risk |
|------|---------|------|
| `204_channel_monitor_hide_throughput.sql` | Inserts default-false setting for hiding RPM/TPM throughput on user-facing Channel Monitor V2. `ON CONFLICT DO NOTHING` — idempotent. | Low |
| `205_channel_monitor_v2_reset_factory_cache_thresholds.sql` | Resets factory cache scoring thresholds written by migration 203 to 0. Conditional on exact prior values and `updated_by IS NULL` — preserves operator overrides. | Low |
| `206_channel_monitor_v2_privacy_defaults.sql` | Sets `channel_monitor_hide_throughput` to `true` for existing rows where it is `false`. Corrects the factory default. | Low |
| `217_group_video_model_prices.sql` | `ALTER TABLE groups ADD COLUMN IF NOT EXISTS video_model_prices JSONB`. Additive, no data change. | Low |
| `218_group_audio_voice_pricing.sql` | Adds three `DECIMAL(20,8)` nullable columns for Grok voice pricing. Additive. | Low |
| `219_group_search_price_per_1k.sql` | Adds `search_price_per_1k DECIMAL(20,8)` nullable column. Additive. | Low |
| `220_clear_non_grok_video_generation_config.sql` | Creates a backup table then NULLs video price columns on non-Grok/non-composite groups. Backup table created with `IF NOT EXISTS` for idempotency; rollback via backup table documented in migration. | Medium — data change, but backed up and reversible |

## Safety assessment

- Migrations 217-219 are purely additive schema changes (`ADD COLUMN IF NOT EXISTS`), safe under concurrent reads/writes.
- Migrations 204-206 are settings table inserts/updates, not schema changes.
- Migration 220 creates a backup table first, making the data mutation recoverable. The scope is limited to groups where `platform` is neither `grok` nor `composite`.
- All migrations are idempotent or guarded against double-application.

## Blue/green compatibility

All changes are backward-compatible for a blue/green deploy:
- New nullable columns require no app code change on old instances.
- Settings changes are soft-defaults only; old instances ignore unknown keys.
- Migration 220 acts only on data no currently-deployed instance writes to during rollout.
