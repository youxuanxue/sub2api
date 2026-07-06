#!/bin/bash
# probe-ssot-recent-success.sh — models/modalities with successful usage_logs in a window.
# Runs on TokenKey prod host via run-probe.sh. Output TSV:
#   model<TAB>modality<TAB>count
#
# Env:
#   WINDOW_HOURS   default 24
#   MIN_COUNT      default 1 (minimum successful rows to treat as "normally serving")
set -u

WINDOW_HOURS="${WINDOW_HOURS:-24}"
MIN_COUNT="${MIN_COUNT:-1}"
PSQL=(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -F $'\t')

echo "# ssot_recent_success window_hours=${WINDOW_HOURS} min_count=${MIN_COUNT} db_now=$(date -u +%Y-%m-%dT%H:%M:%SZ)"

"${PSQL[@]}" -c "
SELECT model,
       CASE
         WHEN COALESCE(billing_mode, 'token') = 'image' THEN 'image'
         WHEN COALESCE(billing_mode, 'token') = 'video' THEN 'video'
         WHEN model ILIKE '%embedding%' THEN 'embeddings'
         ELSE 'text'
       END AS modality,
       count(*)::bigint AS n
FROM usage_logs
WHERE created_at >= now() - interval '${WINDOW_HOURS} hours'
GROUP BY 1, 2
HAVING count(*) >= ${MIN_COUNT}
ORDER BY n DESC, model ASC;
"
