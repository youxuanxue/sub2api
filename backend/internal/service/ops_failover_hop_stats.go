package service

import (
	"context"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const opsFailoverHopStatsDefaultTopN = 10

// GetFailoverHopStats returns the per-account wasted-failover-hop KPI for the
// OpenAI/GPT line (openai + newapi), used to observe the hop reduction from the
// window-sched scheduler change (PR #899). Read-time aggregate over
// ops_error_logs recovered-200 rows; see the repository for the SQL.
func (s *OpsService) GetFailoverHopStats(ctx context.Context, filter *OpsFailoverHopStatsFilter) (*OpsFailoverHopStatsResponse, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if filter == nil {
		return nil, infraerrors.BadRequest("OPS_FILTER_REQUIRED", "filter is required")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, infraerrors.BadRequest("OPS_TIME_RANGE_REQUIRED", "start_time/end_time are required")
	}
	if filter.StartTime.After(filter.EndTime) {
		return nil, infraerrors.BadRequest("OPS_TIME_RANGE_INVALID", "start_time must be <= end_time")
	}
	if filter.GroupID != nil && *filter.GroupID <= 0 {
		return nil, infraerrors.BadRequest("OPS_GROUP_ID_INVALID", "group_id must be > 0")
	}
	if filter.TopN <= 0 {
		filter.TopN = opsFailoverHopStatsDefaultTopN
	}
	if filter.TopN < 1 || filter.TopN > 100 {
		return nil, infraerrors.BadRequest("OPS_TOPN_INVALID", "top_n must be between 1 and 100")
	}

	return s.opsRepo.GetFailoverHopStats(ctx, filter)
}
