package service

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type failoverHopStatsRepoStub struct {
	OpsRepository
	resp     *OpsFailoverHopStatsResponse
	captured *OpsFailoverHopStatsFilter
}

func (s *failoverHopStatsRepoStub) GetFailoverHopStats(ctx context.Context, filter *OpsFailoverHopStatsFilter) (*OpsFailoverHopStatsResponse, error) {
	s.captured = filter
	if s.resp != nil {
		return s.resp, nil
	}
	return &OpsFailoverHopStatsResponse{}, nil
}

func TestOpsServiceGetFailoverHopStats_Validation(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name       string
		filter     *OpsFailoverHopStatsFilter
		wantCode   int
		wantReason string
	}{
		{"filter 不能为空", nil, 400, "OPS_FILTER_REQUIRED"},
		{"时间范围必填", &OpsFailoverHopStatsFilter{EndTime: now}, 400, "OPS_TIME_RANGE_REQUIRED"},
		{"start 不能晚于 end", &OpsFailoverHopStatsFilter{StartTime: now, EndTime: now.Add(-time.Minute)}, 400, "OPS_TIME_RANGE_INVALID"},
		{"group_id 必须 > 0", &OpsFailoverHopStatsFilter{StartTime: now.Add(-time.Hour), EndTime: now, GroupID: int64Ptr(0)}, 400, "OPS_GROUP_ID_INVALID"},
		{"top_n 越界", &OpsFailoverHopStatsFilter{StartTime: now.Add(-time.Hour), EndTime: now, TopN: 101}, 400, "OPS_TOPN_INVALID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &OpsService{opsRepo: &failoverHopStatsRepoStub{}}
			_, err := svc.GetFailoverHopStats(context.Background(), tt.filter)
			require.Error(t, err)
			require.Equal(t, tt.wantCode, infraerrors.Code(err))
			require.Equal(t, tt.wantReason, infraerrors.Reason(err))
		})
	}
}

func TestOpsServiceGetFailoverHopStats_DefaultTopN(t *testing.T) {
	now := time.Now().UTC()
	repo := &failoverHopStatsRepoStub{resp: &OpsFailoverHopStatsResponse{}}
	svc := &OpsService{opsRepo: repo}

	_, err := svc.GetFailoverHopStats(context.Background(), &OpsFailoverHopStatsFilter{
		TimeRange: "1d",
		StartTime: now.Add(-24 * time.Hour),
		EndTime:   now,
	})
	require.NoError(t, err)
	require.NotNil(t, repo.captured)
	require.Equal(t, opsFailoverHopStatsDefaultTopN, repo.captured.TopN, "topN defaults when unset")
}

func TestOpsServiceGetFailoverHopStats_RepoUnavailable(t *testing.T) {
	now := time.Now().UTC()
	svc := &OpsService{}

	_, err := svc.GetFailoverHopStats(context.Background(), &OpsFailoverHopStatsFilter{
		TimeRange: "1h",
		StartTime: now.Add(-time.Hour),
		EndTime:   now,
		TopN:      10,
	})
	require.Error(t, err)
	require.Equal(t, 503, infraerrors.Code(err))
	require.Equal(t, "OPS_REPO_UNAVAILABLE", infraerrors.Reason(err))
}
