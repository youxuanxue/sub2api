-- Migration: tk_022_qwen_deepseek_to_extension_engine
--
-- Migrate the Qwen (account id=60 / group id=18) and DeepSeek (account id=39 /
-- group id=11) channels from the openai-native passthrough path
-- (platform=openai, channel_type=0, raw forward to credentials.base_url) to the
-- New API "extension engine" bridge — i.e. the fifth-platform adaptor path,
-- same shape as the VolcEngine account 7 (platform=newapi, channel_type>0):
--   - DeepSeek  -> channel_type=43 (ChannelTypeDeepSeek, relay/channel/deepseek)
--   - Qwen/Ali  -> channel_type=17 (ChannelTypeAli,      relay/channel/ali)
--
-- Why bridge over passthrough:
--   The new-api Ali/DeepSeek adaptors do provider-specific shaping the raw
--   passthrough does not (Ali clamps top_p to (0,1) + sets DashScope SSE header;
--   DeepSeek handles reasoning / FIM /beta / thinking-suffix). Routing both
--   through the bridge also unifies them with the existing fifth-platform
--   account (VolcEngine) under one identity (platform=newapi) so admin UI,
--   affinity, and the newapi sentinel/registry all treat them consistently.
--
-- Audit (2026-06-10, read-only prod probe). Both groups are DEDICATED
-- single-account pools (group 11 = only account 39; group 18 = only account 60),
-- so flipping group.platform to newapi orphans no sibling account:
--   - account 39 "ds-官": platform=openai, channel_type=0, schedulable=true,
--     credentials.base_url=https://api.deepseek.com (bare host — the DeepSeek
--     adaptor appends /v1/chat/completions itself, so this base_url is already
--     adaptor-compatible and is NOT changed here),
--     model_mapping={deepseek-v4-pro, deepseek-v4-flash} (identity whitelist).
--   - account 60 "Qwen": platform=openai, channel_type=0, schedulable=false
--     (tk_021 enables it on the openai path first; this migration then moves it
--     to the bridge), credentials.base_url=
--     https://dashscope.aliyuncs.com/compatible-mode/v1 (HAS the /compatible-mode/v1
--     suffix). The Ali adaptor builds {base_url}/compatible-mode/v1/chat/completions
--     itself, so the stored suffix would DOUBLE-append -> 404. This migration
--     rewrites base_url to the bare host https://dashscope.aliyuncs.com.
--     model_mapping={qwen3.7-max + 4 dated/preview variants} (identity whitelist).
--
-- Not changed (verified, no code/pricing change needed):
--   - Model names pass through unrewritten: ParseDeepSeekV4ThinkingSuffix only
--     trims {"-none","-max"}; deepseek-v4-pro/-flash match neither.
--   - Pricing: bridge accounts bill via the same BillingModel resolver; overlay
--     already prices deepseek-v4-pro/-flash + qwen3.7-max* (#696/#701/#706).
--   - claude-code: messages_dispatch_model_config is preserved for newapi groups
--     (tkGroupKeepsDispatchConfig includes newapi). group 11 keeps its
--     opus->deepseek-v4-pro / sonnet,haiku->deepseek-v4-flash dispatch; group 18's
--     dispatch is (re)set to qwen3.7-max here so it is correct even if tk_021 has
--     not run on this stack yet.
--
-- Atomicity: the migration runner wraps each .sql file in one transaction
-- (migrations_runner.go BeginTx/Commit), so the account-platform flip and the
-- group-platform flip commit together. A live scheduler replica (separate MVCC
-- connection) sees the old consistent state until COMMIT, then refreshes both via
-- the trailing scheduler_outbox events (account_changed + group_changed) — never
-- a torn state where account.platform != group.platform (which IsOpenAICompatPoolMember
-- would read as "not a pool member" and silently de-schedule the account).
--
-- Raw-SQL account/group mutations bypass the Ent hooks that enqueue a scheduler
-- snapshot refresh, so each guarded UPDATE enqueues its own outbox event
-- (account_changed / group_changed) only when it actually matched a row.
--
-- Idempotent + cross-deployment safe: every UPDATE is guarded by
-- (id, name, platform='openai') so re-running is a no-op once migrated, and a
-- bare id colliding with an unrelated account/group in another DB (these
-- migrations run on every TK stack) cannot match.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- 1) DeepSeek account 39 -> newapi / channel_type=43 (DeepSeek adaptor).
--    base_url unchanged (bare host already adaptor-compatible).
WITH upd_acct AS (
    UPDATE accounts
    SET platform = 'newapi',
        channel_type = 43,
        schedulable = true,
        updated_at = NOW()
    WHERE id = 39
      AND name = 'ds-官'
      AND platform = 'openai'
      AND channel_type = 0
      AND deleted_at IS NULL
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd_acct;

-- 2) Qwen account 60 -> newapi / channel_type=17 (Ali adaptor).
--    Strip the /compatible-mode/v1 suffix from base_url (the adaptor appends it).
WITH upd_acct AS (
    UPDATE accounts
    SET platform = 'newapi',
        channel_type = 17,
        schedulable = true,
        credentials = jsonb_set(credentials, '{base_url}', '"https://dashscope.aliyuncs.com"'),
        updated_at = NOW()
    WHERE id = 60
      AND name = 'Qwen'
      AND platform = 'openai'
      AND deleted_at IS NULL
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd_acct;

-- 3) DeepSeek group 11 -> newapi (dispatch config preserved for newapi groups).
WITH upd_grp AS (
    UPDATE groups
    SET platform = 'newapi',
        updated_at = NOW()
    WHERE id = 11
      AND name = 'deepseek'
      AND platform = 'openai'
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'group_changed', NULL, id, NULL FROM upd_grp;

-- 4) Qwen group 18 -> newapi, and (re)set Claude-Code dispatch to the only model
--    the Qwen account advertises (qwen3.7-max), self-contained vs tk_021 ordering.
WITH upd_grp AS (
    UPDATE groups
    SET platform = 'newapi',
        messages_dispatch_model_config = '{
            "opus_mapped_model": "qwen3.7-max",
            "sonnet_mapped_model": "qwen3.7-max",
            "haiku_mapped_model": "qwen3.7-max"
        }'::jsonb,
        updated_at = NOW()
    WHERE id = 18
      AND name = 'Qwen'
      AND platform = 'openai'
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'group_changed', NULL, id, NULL FROM upd_grp;
