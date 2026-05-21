-- Anthropic prod-side group RPM apply template (R3-unified path).
--
-- This template replaces the legacy R3 absorb-zero path
-- (anthropic-stub-mirror-rpm-apply-template.sql), which let any
-- mixed group (self-edge + external) collapse to rpm_limit=0
-- (unlimited).  Under R3-unified that state is forbidden:
--
--     group.rpm_limit  =  Σ stub.declared_rpm           (plain SUM, no absorb-zero)
--     stub.declared_rpm > 0                             (unlimited forbidden)
--     self-edge stub.declared_rpm  =  upstream edge default_group.rpm_limit
--     external stub.declared_rpm   =  operator declared (visible quota)
--
-- declared_rpm lives in `accounts.extra->>'declared_rpm'` (jsonb int).
-- The runtime does not currently consume it; the value is a guard /
-- audit-trail metadata that pins what the operator committed to.
-- Future Go-layer enforcement reads from the same key.
--
-- Pre-flight (NON-NEGOTIABLE — see SKILL §"Pre-apply re-read 协议"):
--   1. Run check-prod-stub-mirror.py against the target group, capture
--      `group_results[i].expected_rpm_limit` (= Σ declared_rpm) and the
--      per-stub `declared_rpm` map.  These are the values you write below.
--   2. Read upstream edge default_group.rpm_limit for every self-edge
--      stub *at apply time* (not from earlier output) — concurrent
--      sessions may have shifted it.  If your :stub_inputs row for a
--      self-edge stub disagrees with the live upstream value, abort
--      and re-plan.
--   3. The DO block aborts if any stub_inputs row is missing or names
--      a non-anthropic-apikey account.
--
-- Usage (psql, self-contained SQL pattern):
--   1) Copy this template body into $CLAUDE_JOB_DIR/<env>-<group>-apply.sql.
--   2) Replace the :group_id / :target_group_rpm / stub_inputs VALUES
--      block with this apply's numbers.
--   3) Pipe via SSM to docker exec tokenkey-postgres psql.
--
-- Example header (operator fills before apply):
--
--   \set group_id 1
--   \set target_group_rpm 364
--   -- stub_inputs(account_id, declared_rpm):
--   --   cc-uk1-oauth → mirror uk1.default = 16
--   --   cc-us1-oauth → mirror us1.default = 48
--   --   tokensea-0.4 → operator declared = 100
--   --   tokensea-0.6 → operator declared = 100
--   --   ds-fallback  → operator declared = 100

BEGIN;

-- Pre-flight guard: refuse to run if any stub_inputs row is missing,
-- soft-deleted, or not an anthropic apikey account.  Refuse to run if
-- target group is missing.  Refuse to run if any declared_rpm <= 0
-- (unlimited is not a legal R3-unified state).
SELECT set_config('app.target_group_id', :'group_id', true);
DO $$
DECLARE
  tg_id BIGINT := current_setting('app.target_group_id')::bigint;
  missing_count INT;
  bad_decl_count INT;
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM groups WHERE id = tg_id AND deleted_at IS NULL
  ) THEN
    RAISE EXCEPTION 'group id=% not found (or soft-deleted)', tg_id;
  END IF;

  -- The temp_stub_inputs view is created by the CTE below at the point
  -- of UPDATE.  We materialize a session-scoped temp table here so the
  -- DO block can validate before any write hits the table.
  CREATE TEMP TABLE IF NOT EXISTS _stub_inputs_validate (
    account_id BIGINT PRIMARY KEY,
    declared_rpm INT NOT NULL
  ) ON COMMIT DROP;
END $$;

-- Operator-filled inputs.  ONE ROW PER STUB IN THE TARGET GROUP.
-- Every row must:
--   - reference an existing anthropic apikey account (DO block enforces)
--   - declare a positive RPM (DO block enforces declared_rpm > 0)
-- self-edge stubs: declared_rpm = upstream edge default_group.rpm_limit
-- external stubs:  declared_rpm = operator quota (visible non-zero)
INSERT INTO _stub_inputs_validate (account_id, declared_rpm) VALUES
  -- BEGIN stub_inputs (REPLACE THIS BLOCK)
  (40::bigint,  16::int),    -- cc-uk1-oauth: mirror uk1.default.rpm_limit
  (42::bigint,  48::int),    -- cc-us1-oauth: mirror us1.default.rpm_limit
  (43::bigint, 100::int),    -- tokensea-0.4: operator declared
  (44::bigint, 100::int),    -- tokensea-0.6: operator declared
  (45::bigint, 100::int)     -- ds-fallback:  operator declared
  -- END stub_inputs
;  -- duplicate account_id intentionally triggers a PK violation (operator error)

-- Validate stub_inputs: every account_id must resolve to an active
-- anthropic apikey stub, declared_rpm > 0, and the SUM must match
-- :target_group_rpm exactly (no silent drift between plan and apply).
SELECT set_config('app.target_group_rpm', :'target_group_rpm', true);
DO $$
DECLARE
  tg_id BIGINT := current_setting('app.target_group_id')::bigint;
  tg_rpm INT := current_setting('app.target_group_rpm')::int;
  missing_count INT;
  bad_decl_count INT;
  sum_decl INT;
  member_count INT;
  input_count INT;
BEGIN
  SELECT COUNT(*) INTO input_count FROM _stub_inputs_validate;
  IF input_count = 0 THEN
    RAISE EXCEPTION 'stub_inputs is empty — operator must fill the VALUES block before apply.';
  END IF;

  -- Every input must resolve to anthropic apikey + deleted_at IS NULL.
  SELECT COUNT(*) INTO missing_count
  FROM _stub_inputs_validate s
  WHERE NOT EXISTS (
    SELECT 1 FROM accounts a
    WHERE a.id = s.account_id
      AND a.platform = 'anthropic'
      AND a.type = 'apikey'
      AND a.deleted_at IS NULL
  );
  IF missing_count > 0 THEN
    RAISE EXCEPTION 'stub_inputs has % row(s) pointing to missing / non-anthropic-apikey / soft-deleted accounts. Re-plan against live state.', missing_count;
  END IF;

  -- declared_rpm > 0 (unlimited is not legal).
  SELECT COUNT(*) INTO bad_decl_count
  FROM _stub_inputs_validate WHERE declared_rpm <= 0;
  IF bad_decl_count > 0 THEN
    RAISE EXCEPTION 'stub_inputs has % row(s) with declared_rpm <= 0; unlimited is forbidden under R3-unified.', bad_decl_count;
  END IF;

  -- Σ declared_rpm must equal :target_group_rpm exactly.
  SELECT SUM(declared_rpm) INTO sum_decl FROM _stub_inputs_validate;
  IF sum_decl <> tg_rpm THEN
    RAISE EXCEPTION 'stub_inputs SUM(declared_rpm)=% does not match :target_group_rpm=%. Re-plan or fix one of the two.', sum_decl, tg_rpm;
  END IF;

  -- The set of stub_inputs account_ids must equal the set of anthropic
  -- apikey members in the target group.  This catches the "operator
  -- added a stub to the group after planning" case and the "operator
  -- removed a stub between plan and apply" case.
  SELECT COUNT(*) INTO member_count
  FROM account_groups ag
  JOIN accounts a ON a.id = ag.account_id
  WHERE ag.group_id = tg_id
    AND a.platform = 'anthropic'
    AND a.type = 'apikey'
    AND a.deleted_at IS NULL;
  IF member_count <> input_count THEN
    RAISE EXCEPTION
      'group id=% has % anthropic apikey member(s) but stub_inputs has % row(s). Re-plan against live group membership.',
      tg_id, member_count, input_count;
  END IF;

  -- And every input id must be an actual group member.
  IF EXISTS (
    SELECT 1 FROM _stub_inputs_validate s
    WHERE NOT EXISTS (
      SELECT 1 FROM account_groups ag
      WHERE ag.group_id = tg_id AND ag.account_id = s.account_id
    )
  ) THEN
    RAISE EXCEPTION 'stub_inputs references account_id(s) not bound to group id=%; re-plan against live binding.', tg_id;
  END IF;
END $$;

-- Apply: write declared_rpm onto every stub's extra, write the group rpm_limit.
WITH applied_stubs AS (
  UPDATE accounts a
  SET extra = COALESCE(a.extra, '{}'::jsonb)
              || jsonb_build_object('declared_rpm', s.declared_rpm),
      updated_at = NOW()
  FROM _stub_inputs_validate s
  WHERE a.id = s.account_id
  RETURNING a.id, a.name, s.declared_rpm
),
applied_group AS (
  UPDATE groups g
  SET rpm_limit = :target_group_rpm,
      updated_at = NOW()
  WHERE g.id = :group_id
  RETURNING g.id, g.name, g.rpm_limit
)
SELECT jsonb_pretty(jsonb_build_object(
  'group', (SELECT jsonb_build_object('id', id, 'name', name, 'rpm_limit', rpm_limit) FROM applied_group),
  'stubs', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', id, 'name', name, 'declared_rpm', declared_rpm
    ) ORDER BY id) FROM applied_stubs
  ), '[]'::jsonb),
  'sum_check', (SELECT jsonb_build_object(
    'sum_declared_rpm', SUM(declared_rpm),
    'group_rpm_limit', (SELECT rpm_limit FROM applied_group),
    'matches', SUM(declared_rpm) = (SELECT rpm_limit FROM applied_group)
  ) FROM applied_stubs)
));

COMMIT;
