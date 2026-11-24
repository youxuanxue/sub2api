package repository

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TOKENKEY_NODE_NAME is rendered from the fleet manifest (deploy/domestic/node_compose.py),
// never from a request host, billing group, or user-controlled filter.
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
		if !s.start.Before(beijingTrialEnd) || !s.end.After(beijingTrialStart) {
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
	scope := &service.OpsOfficialSLAScope{Requests: counts.requests, Faults: counts.faults}
	if counts.requests > 0 {
		rate, _ := service.ComputeSLAMetrics(0, counts.requests, counts.faults)
		scope.Rate = &rate
	}
	return scope, nil
}
