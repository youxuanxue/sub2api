-- Bound blue/green startup DDL waits; a busy old instance must not stall live traffic.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- Drop user_platform_quotas.platform CHECK constraint (TokenKey follow-up to upstream 242).
--
-- Upstream 242_drop_platform_check_constraints.sql removed the platform CHECK
-- whitelist for upstream-known platforms: platform membership is validated by the
-- application-layer catalog (domain/platforms.go), not the database. TK's shipped
-- tk_083/tk_087/tk_096 migrations are immutable and still re-ADD a widened CHECK
-- later in filename order, so without this migration the whitelist would be
-- resurrected after 242 and reject future platforms (e.g. command_code/cline)
-- on fresh installs and migrated fleets alike.
--
-- Idempotent DROP keeps every install path converging on the post-242 shape:
--   fresh: 242 drop (no-op) -> tk_083/087/096 re-add -> tk_103 drop
--   prod : constraint already widened by applied tk_083/087/096 -> tk_103 drop
ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;
