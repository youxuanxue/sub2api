WITH target_tables(table_name) AS (
  VALUES ('ops_error_logs'), ('ops_system_logs'), ('usage_logs')
), required_ranges AS (
  SELECT
    date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS today_lower,
    (date_trunc('day', now() AT TIME ZONE 'UTC') + interval '1 day') AT TIME ZONE 'UTC' AS today_upper,
    (date_trunc('day', now() AT TIME ZONE 'UTC') + interval '1 day') AT TIME ZONE 'UTC' AS future_lower,
    (date_trunc('day', now() AT TIME ZONE 'UTC') + interval '8 days') AT TIME ZONE 'UTC' AS future_upper
), parents AS (
  SELECT targets.table_name, to_regclass(targets.table_name) AS parent_oid
  FROM target_tables targets
), child_bounds AS (
  SELECT
    parents.table_name,
    pg_get_expr(child.relpartbound, child.oid, true) AS bound_expr
  FROM parents
  JOIN pg_inherits inheritance ON inheritance.inhparent = parents.parent_oid
  JOIN pg_class child ON child.oid = inheritance.inhrelid
  JOIN pg_namespace child_namespace ON child_namespace.oid = child.relnamespace
), parsed_bounds AS (
  SELECT
    table_name,
    bound_expr,
    bound_expr LIKE 'FOR VALUES FROM (MINVALUE)%' AS lower_unbounded,
    substring(bound_expr FROM $$FROM \('([^']+)'$$)::timestamptz AS lower_bound,
    substring(bound_expr FROM $$TO \('([^']+)'$$)::timestamptz AS upper_bound,
    bound_expr = 'DEFAULT' OR bound_expr LIKE '%TO (MAXVALUE)' AS unsupported_bound
  FROM child_bounds
), table_coverage AS (
  SELECT
    parents.table_name,
    parents.parent_oid IS NOT NULL
      AND count(parsed_bounds.bound_expr) > 0
      AND bool_and(
        NOT parsed_bounds.unsupported_bound
        AND (parsed_bounds.lower_unbounded OR parsed_bounds.lower_bound IS NOT NULL)
        AND parsed_bounds.upper_bound IS NOT NULL
      ) AS topology_valid,
    range_agg(tstzrange(
      CASE WHEN parsed_bounds.lower_unbounded THEN NULL ELSE parsed_bounds.lower_bound END,
      parsed_bounds.upper_bound,
      '[)'
    )) FILTER (
      WHERE NOT parsed_bounds.unsupported_bound
        AND (parsed_bounds.lower_unbounded OR parsed_bounds.lower_bound IS NOT NULL)
        AND parsed_bounds.upper_bound IS NOT NULL
    ) AS covered_ranges
  FROM parents
  LEFT JOIN parsed_bounds ON parsed_bounds.table_name = parents.table_name
  GROUP BY parents.table_name, parents.parent_oid
), heartbeat AS (
  SELECT last_success_at, last_error_at
  FROM ops_job_heartbeats
  WHERE job_name = 'ops_partition_maintenance'
)
SELECT 'PARTITIONSTATS ' || row_to_json(result)::text
FROM (
  SELECT
    now() AS server_clock,
    COALESCE(error_coverage.topology_valid AND error_coverage.covered_ranges @>
      tstzrange(required.today_lower, required.today_upper, '[)'), false)
      AS ops_error_logs_current_covered,
    COALESCE(error_coverage.topology_valid AND error_coverage.covered_ranges @>
      tstzrange(required.future_lower, required.future_upper, '[)'), false)
      AS ops_error_logs_future_covered,
    COALESCE(system_coverage.topology_valid AND system_coverage.covered_ranges @>
      tstzrange(required.today_lower, required.today_upper, '[)'), false)
      AS ops_system_logs_current_covered,
    COALESCE(system_coverage.topology_valid AND system_coverage.covered_ranges @>
      tstzrange(required.future_lower, required.future_upper, '[)'), false)
      AS ops_system_logs_future_covered,
    COALESCE(usage_coverage.topology_valid AND usage_coverage.covered_ranges @>
      tstzrange(required.today_lower, required.today_upper, '[)'), false)
      AS usage_logs_current_covered,
    COALESCE(usage_coverage.topology_valid AND usage_coverage.covered_ranges @>
      tstzrange(required.future_lower, required.future_upper, '[)'), false)
      AS usage_logs_future_covered,
    (SELECT last_success_at FROM heartbeat) AS partition_maintenance_last_success_at,
    (SELECT last_error_at FROM heartbeat) AS partition_maintenance_last_error_at
  FROM required_ranges required
  JOIN table_coverage error_coverage ON error_coverage.table_name = 'ops_error_logs'
  JOIN table_coverage system_coverage ON system_coverage.table_name = 'ops_system_logs'
  JOIN table_coverage usage_coverage ON usage_coverage.table_name = 'usage_logs'
) result;
