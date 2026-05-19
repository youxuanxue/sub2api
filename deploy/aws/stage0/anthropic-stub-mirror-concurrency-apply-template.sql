-- Anthropic stub concurrency mirror apply template (R1 mirror path)
-- Purpose: write account.concurrency on a prod-side anthropic apikey
-- forward stub to mirror the upstream edge default group's OAuth account
-- concurrency absorb-zero SUM, per the R1 rule in
-- SKILL §"prod 控制面：anthropic stub 主路径镜像规则".
--
-- Why this template exists: the R3 mirror template
-- (anthropic-stub-mirror-rpm-apply-template.sql) covers group.rpm_limit
-- only.  R1 mirror writes to a different surface — accounts.concurrency
-- on individual stubs — so it needs its own template.  Splitting keeps
-- each template single-purpose and pre-flight scoped to the surface it
-- writes.
--
-- Pre-flight:
--   1. Run ops/anthropic/check-prod-stub-mirror.py --json against this
--      target and read `results[i].expected_concurrency` for the target
--      stub; pass that integer as :new_concurrency below.
--   2. The DO block aborts if the account is not an anthropic apikey
--      stub with a self-edge base_url (matching
--      `https?://api-<edge>.tokenkey.dev/?`).  OAuth accounts are
--      governed by the tier baseline template; external stubs (e.g.
--      agent.tokensea.ai) have no upstream OAuth pool to mirror and
--      must be set independently by the operator.
--
-- Usage (psql):
--   \set account_name 'cc-us1-oauth'
--   \set new_concurrency 5
--   \i deploy/aws/stage0/anthropic-stub-mirror-concurrency-apply-template.sql

BEGIN;

-- Pre-flight: refuse to run on accounts that aren't anthropic apikey
-- self-edge stubs.  Bridge :'account_name' into the DO block via a
-- session GUC so it is not expanded inside the dollar-quoted body.
SELECT set_config('app.target_account_name', :'account_name', true);
DO $$
DECLARE
  ta TEXT := current_setting('app.target_account_name');
  acc_platform TEXT;
  acc_type TEXT;
  acc_base_url TEXT;
BEGIN
  -- Account must exist; otherwise the WITH...UPDATE below silently
  -- no-ops (a typo in :'account_name' would leave the operator thinking
  -- the mirror was written when it wasn't).
  IF NOT EXISTS (
    SELECT 1 FROM accounts WHERE name = ta AND deleted_at IS NULL
  ) THEN
    RAISE EXCEPTION 'account % not found (or soft-deleted)', quote_literal(ta);
  END IF;

  SELECT a.platform, a.type, NULLIF(a.credentials->>'base_url', '')
    INTO acc_platform, acc_type, acc_base_url
  FROM accounts a
  WHERE a.name = ta AND a.deleted_at IS NULL;

  IF acc_platform IS DISTINCT FROM 'anthropic' THEN
    RAISE EXCEPTION USING
      MESSAGE = 'account "' || ta || '" platform=' || COALESCE(acc_platform, '<null>') || '; R1 mirror applies only to anthropic stubs',
      HINT = 'This template targets prod-side anthropic apikey forward stubs only.';
  END IF;

  IF acc_type IS DISTINCT FROM 'apikey' THEN
    RAISE EXCEPTION USING
      MESSAGE = 'account "' || ta || '" type=' || COALESCE(acc_type, '<null>') || '; R1 mirror applies only to apikey stubs',
      HINT = 'OAuth accounts are governed by tier baseline (anthropic-oauth-stability-tiered-apply-template.sql).';
  END IF;

  -- Self-edge regex MUST stay in lockstep with SELF_EDGE_BASE_URL_RE in
  -- ops/anthropic/check-prod-stub-mirror.py — both apply to the same
  -- prod stub base_url surface.  Character class includes `-` so edge
  -- ids like `us-west-1` are accepted by both layers.
  IF acc_base_url IS NULL OR acc_base_url !~ '^https?://api-[a-z0-9-]+\.tokenkey\.dev/?$' THEN
    RAISE EXCEPTION USING
      MESSAGE = 'account "' || ta || '" base_url=' || COALESCE(acc_base_url, '<null>') || '; not a self-edge stub',
      HINT = 'R1 mirror requires base_url matching api-<edge>.tokenkey.dev. External stubs (e.g. agent.tokensea.ai) have no upstream OAuth pool and must be set independently.';
  END IF;
END $$;

WITH target_account AS (
  SELECT
    a.id,
    a.name,
    a.platform,
    a.type,
    a.concurrency AS concurrency_before,
    NULLIF(a.credentials->>'base_url', '') AS base_url
  FROM accounts a
  WHERE a.name = :'account_name'
    AND a.deleted_at IS NULL
  ORDER BY a.id
  LIMIT 1
),
updated_account AS (
  UPDATE accounts a
  SET concurrency = :new_concurrency,
      updated_at = NOW()
  FROM target_account ta
  WHERE a.id = ta.id
  RETURNING a.id, a.name, ta.concurrency_before, a.concurrency AS concurrency_after, ta.base_url
)
SELECT jsonb_pretty(jsonb_build_object(
  'account', (SELECT jsonb_build_object(
    'id', id,
    'name', name,
    'platform', platform,
    'type', type,
    'base_url', base_url
  ) FROM target_account),
  'concurrency_update', (SELECT jsonb_build_object(
    'before', concurrency_before,
    'after', concurrency_after,
    'expected_per_r1_mirror', :new_concurrency
  ) FROM updated_account)
));

COMMIT;
