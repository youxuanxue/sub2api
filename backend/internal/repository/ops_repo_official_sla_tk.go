package repository

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TOKENKEY_NODE_NAME is rendered from the fleet manifest (deploy/domestic/node_compose.py),
// never from a request host, billing group, or user-controlled filter.
func usesOfficialSLAPolicy() bool {
	node := os.Getenv("TOKENKEY_NODE_NAME")
	return strings.HasPrefix(node, "prod-") || strings.HasPrefix(node, "cn-prod-")
}

func beijingTrialOverlaps(start, end time.Time) bool {
	return strings.HasPrefix(os.Getenv("TOKENKEY_NODE_NAME"), "cn-prod-") && start.Before(beijingTrialEnd) && end.After(beijingTrialStart) && start.Before(end)
}

var (
	beijingTrialStart = time.Date(2026, 11, 18, 14, 34, 0, 0, time.UTC)
	beijingTrialEnd   = time.Date(2026, 11, 18, 23, 12, 0, 0, time.UTC)
)

// All dashboard modes use one statement (one PostgreSQL snapshot). Read whole
// containing hours to verify historical coverage, but count only eligible rows
// for the requested interval. Sparse hours have no aggregate row when empty.
func (r *opsRepository) officialSLA(ctx context.Context, f *service.OpsDashboardFilter) (*service.OpsOfficialSLAScope, error) {
	start, end := f.StartTime, f.EndTime
	first, last := utcFloorToHour(start), utcCeilToHour(end)
	sealed := utcFloorToHour(preaggSafeEnd(time.Now().UTC()))
	hw, args := hourlyMetricsScope(f, first, last)
	join, uw, ua, next := buildUsageWhere(f, first, last, len(args)+1)
	args = append(args, ua...)
	ew, ea, next := buildErrorWhere(f, first, last, next)
	args = append(args, ea...)
	eligible := fmt.Sprintf("created_at >= $%d AND created_at < $%d", next, next+1)
	args = append(args, start, end)
	next += 2
	verifyHour := "TRUE"
	if beijingTrialOverlaps(start, end) {
		eligible += fmt.Sprintf(" AND NOT (created_at >= $%d AND created_at < $%d)", next, next+1)
		// Excluded-only hours need no proof; mixed hours must reconcile in full.
		verifyHour = fmt.Sprintf("(GREATEST(bucket,$%d) < $%d OR LEAST(bucket+interval '1 hour',$%d) > $%d)", next-2, next, next-1, next+1)
		args = append(args, beijingTrialStart, beijingTrialEnd)
		next += 2
	}
	args = append(args, sealed)
	owner := service.OpsSLAFaultOwnerPredicate("")
	q := `WITH usage_rows AS (SELECT ul.created_at FROM usage_logs ul ` + join + ` ` + uw + `),
    detail AS (
      SELECT date_trunc('hour',created_at,'UTC') AS bucket,count(*) AS successes,0 AS errors,0 AS faults,
        count(*) FILTER (WHERE ` + eligible + `) AS requests,0 AS official_faults FROM usage_rows GROUP BY 1
      UNION ALL
      SELECT date_trunc('hour',created_at,'UTC'),0,count(*),count(*) FILTER (WHERE ` + owner + `),
        count(*) FILTER (WHERE ` + eligible + `),count(*) FILTER (WHERE ` + eligible + ` AND status_code NOT IN (400, 429) AND ` + owner + `)
        FROM ops_error_logs ` + ew + ` AND status_code>=400 GROUP BY 1
    ), hourly AS (
      SELECT bucket_start AS bucket,sum(success_count) AS successes,sum(error_count_total) AS errors,sum(error_count_sla) AS faults
      FROM ops_metrics_hourly WHERE ` + hw + ` GROUP BY 1
    ), observed AS (
      SELECT bucket,sum(successes) AS successes,sum(errors) AS errors,sum(faults) AS faults,sum(requests) AS requests,sum(official_faults) AS official_faults FROM detail GROUP BY 1
    ) SELECT COALESCE(sum(requests),0),COALESCE(sum(official_faults),0),
      COALESCE(bool_or(bucket < $` + fmt.Sprint(next) + ` AND ` + verifyHour + ` AND (
        COALESCE(h.successes,0)<>COALESCE(o.successes,0) OR COALESCE(h.errors,0)<>COALESCE(o.errors,0) OR COALESCE(h.faults,0)<>COALESCE(o.faults,0))),false)
      FROM hourly h FULL JOIN observed o USING(bucket)`
	var scope service.OpsOfficialSLAScope
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&scope.Requests, &scope.Faults, &scope.Incomplete); err != nil {
		return nil, err
	}
	if !scope.Incomplete && scope.Requests > 0 {
		rate, _ := service.ComputeSLAMetrics(0, scope.Requests, scope.Faults)
		scope.Rate = &rate
	}
	return &scope, nil
}
