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

type slaCounts struct{ requests, faults int64 }

func (c slaCounts) plus(d slaCounts) slaCounts {
	return slaCounts{c.requests + d.requests, c.faults + d.faults}
}

type slaSegment struct {
	start, end time.Time
	counts     slaCounts
}

// Sum only the official part of each source segment. Whole excluded hours need
// no raw logs; both partial-hour boundaries count the *included* side. This
// avoids subtracting expired raw failure rows from retained hourly totals.
func officialSLACounts(segments []slaSegment, raw func(time.Time, time.Time) (slaCounts, error)) (slaCounts, error) {
	var total slaCounts
	for _, s := range segments {
		if !s.start.Before(s.end) {
			continue
		}
		if !beijingTrialOverlaps(s.start, s.end) {
			total = total.plus(s.counts)
			continue
		}
		for _, edge := range [][2]time.Time{{s.start, minTime(s.end, beijingTrialStart)}, {maxTime(s.start, beijingTrialEnd), s.end}} {
			if !edge[0].Before(edge[1]) {
				continue
			}
			c, err := raw(edge[0], edge[1])
			if err != nil {
				return slaCounts{}, err
			}
			total = total.plus(c)
		}
	}
	return total, nil
}

func (r *opsRepository) officialSLA(ctx context.Context, f *service.OpsDashboardFilter, segs []slaSegment) (*service.OpsOfficialSLAScope, error) {
	counts, err := officialSLACounts(segs, func(start, end time.Time) (slaCounts, error) {
		success, _, err := r.queryUsageCounts(ctx, f, start, end)
		if err != nil {
			return slaCounts{}, err
		}
		errors, faults, _, _, _, err := r.queryErrorCounts(ctx, f, start, end)
		return slaCounts{success + errors, faults}, err
	})
	if err != nil {
		return nil, err
	}
	// Hourly aggregates retain fault totals but not their final HTTP codes. Verify
	// coverage before using raw evidence; never guess a rate from expired rows.
	var final400, observed int64
	if counts.faults > 0 {
		where, args, next := buildErrorWhere(f, f.StartTime, f.EndTime, 1)
		if beijingTrialOverlaps(f.StartTime, f.EndTime) {
			where += fmt.Sprintf(" AND NOT (created_at >= $%d AND created_at < $%d)", next, next+1)
			args = append(args, beijingTrialStart, beijingTrialEnd)
		}
		q := "SELECT count(*) FILTER (WHERE status_code=400), count(*) FROM ops_error_logs " + where + " AND status_code >= 400 AND " + service.OpsSLAFaultOwnerPredicate("")
		if err := r.db.QueryRowContext(ctx, q, args...).Scan(&final400, &observed); err != nil {
			return nil, err
		}
	}
	if observed != counts.faults {
		return &service.OpsOfficialSLAScope{Requests: counts.requests, Incomplete: true}, nil
	}
	counts.faults -= final400
	scope := &service.OpsOfficialSLAScope{Requests: counts.requests, Faults: counts.faults}
	if counts.requests > 0 {
		rate, _ := service.ComputeSLAMetrics(0, counts.requests, counts.faults)
		scope.Rate = &rate
	}
	return scope, nil
}
