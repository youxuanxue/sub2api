#!/usr/bin/env bash
# probe-traffic-proven-models.sh — runs ON the prod host (delivered via
# ops/observability/run-probe.sh) and emits the (platform, model) pairs that
# served REAL traffic successfully in the last TRAFFIC_HOURS hours, read-only
# from usage_logs.
#
# Why this exists: refresh-servable-allowlist.py probes ~160 candidate models
# through prod SSM in ~16 batches (8–15 min). A candidate that real users already
# got a successful response for in the last day is provably servable — there is no
# value in re-probing it. This script supplies that positive evidence so the
# refresh tool can SHORT-CIRCUIT those candidates out of the probe batches.
#
# Correctness contract (the caller depends on these properties):
#   * usage_logs is append-only and holds ONE row per METERED (served) request.
#     A row existing for a model means that request completed and was billed —
#     errors that consume no tokens are never metered, so they leave no row. The
#     extra "real generation" filter (tokens / image / video > 0) drops any $0
#     placeholder row so a recorded-but-empty request can never read as proof.
#     => a row passing the filter is POSITIVE evidence of servability, never the
#        reverse: a model with no recent traffic is simply absent here (the caller
#        still probes it; absence is NOT an unsupported signal).
#   * Platform attribution is intentionally NOT decided here. accounts.platform is
#     emitted only as human context; the caller buckets each model by the
#     CANDIDATE platform set and intersects against it, so a model that is not a
#     known candidate is dropped (never injected into the allowlist) regardless of
#     which platform/account happened to serve it. This is robust to Vertex/gemini
#     being served under accounts.platform='newapi' as the fifth platform.
#
# Both the billed `model` and the client-facing `requested_model` are reported so
# a menu-facing requested id whose upstream was mapped is still counted.
#
# Env:
#   TRAFFIC_HOURS  default 24   (look-back window, integer hours)
#
# Output: one TSV line per (platform, model):  <platform>\t<model>\t<hits>
# Keys/credentials are never touched; this is a pure read of usage_logs+accounts.
set -uo pipefail

HOURS="${TRAFFIC_HOURS:-24}"
if ! [[ "$HOURS" =~ ^[0-9]+$ ]] || [ "$HOURS" -lt 1 ]; then
	echo "probe-traffic-proven [config]: TRAFFIC_HOURS must be a positive integer, got: $HOURS" >&2
	exit 1
fi

PSQL='sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1'

# One text column per row, tab-joined inside the SELECT (so -A -t needs no -F).
# UNION ALL folds the billed model id and the client-requested id into the same
# served-model universe; GROUP BY collapses to distinct (platform, model) + hits.
$PSQL <<SQL
SELECT a.platform || E'\t' || u.mdl || E'\t' || count(*)::text
FROM (
  SELECT account_id, model AS mdl
  FROM usage_logs
  WHERE created_at >= now() - interval '${HOURS} hours'
    AND (input_tokens > 0 OR output_tokens > 0 OR image_count > 0
         OR COALESCE(video_duration_seconds, 0) > 0)
  UNION ALL
  SELECT account_id, requested_model AS mdl
  FROM usage_logs
  WHERE created_at >= now() - interval '${HOURS} hours'
    AND requested_model IS NOT NULL AND requested_model <> ''
    AND (input_tokens > 0 OR output_tokens > 0 OR image_count > 0
         OR COALESCE(video_duration_seconds, 0) > 0)
) u
JOIN accounts a ON a.id = u.account_id AND a.deleted_at IS NULL
WHERE u.mdl IS NOT NULL AND u.mdl <> ''
GROUP BY a.platform, u.mdl
ORDER BY a.platform, u.mdl;
SQL
