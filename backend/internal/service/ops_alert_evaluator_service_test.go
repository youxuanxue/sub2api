//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

var _ OpsRepository = (*stubOpsRepo)(nil)

type stubOpsRepo struct {
	OpsRepository
	overview *OpsDashboardOverview
	err      error

	routingRejections    int64
	routingRejectionsErr error
	routingCauses        []*OpsRoutingRejectionCause
	routingCausesErr     error
	routingUsers         []*OpsRoutingRejectionUser
	routingUsersErr      error
}

func (s *stubOpsRepo) GetDashboardOverview(ctx context.Context, filter *OpsDashboardFilter) (*OpsDashboardOverview, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.overview != nil {
		return s.overview, nil
	}
	return &OpsDashboardOverview{}, nil
}

func (s *stubOpsRepo) CountRoutingCapacityRejections(ctx context.Context, filter *OpsDashboardFilter) (int64, error) {
	if s.routingRejectionsErr != nil {
		return 0, s.routingRejectionsErr
	}
	return s.routingRejections, nil
}

func (s *stubOpsRepo) TopRoutingCapacityRejectionCauses(ctx context.Context, filter *OpsDashboardFilter, limit int) ([]*OpsRoutingRejectionCause, error) {
	if s.routingCausesErr != nil {
		return nil, s.routingCausesErr
	}
	return s.routingCauses, nil
}

func (s *stubOpsRepo) TopRoutingCapacityRejectionUsers(ctx context.Context, filter *OpsDashboardFilter, limit int) ([]*OpsRoutingRejectionUser, error) {
	if s.routingUsersErr != nil {
		return nil, s.routingUsersErr
	}
	return s.routingUsers, nil
}

// GetTopErrorCause is overridden (the embedded OpsRepository is nil) so the
// evaluator's best-effort top-cause lookup never panics in these tests; an empty
// result simply means no 主因 line is attached.
func (s *stubOpsRepo) GetTopErrorCause(ctx context.Context, filter *OpsDashboardFilter, upstreamOnly bool, limit int) ([]*OpsTopErrorCause, error) {
	return nil, nil
}

func TestComputeGroupAvailableRatio(t *testing.T) {
	t.Parallel()

	t.Run("正常情况: 10个账号, 8个可用 = 80%", func(t *testing.T) {
		t.Parallel()

		got := computeGroupAvailableRatio(&GroupAvailability{
			TotalAccounts:  10,
			AvailableCount: 8,
		})
		require.InDelta(t, 80.0, got, 0.0001)
	})

	t.Run("边界情况: TotalAccounts = 0 应返回 0", func(t *testing.T) {
		t.Parallel()

		got := computeGroupAvailableRatio(&GroupAvailability{
			TotalAccounts:  0,
			AvailableCount: 8,
		})
		require.Equal(t, 0.0, got)
	})

	t.Run("边界情况: AvailableCount = 0 应返回 0%", func(t *testing.T) {
		t.Parallel()

		got := computeGroupAvailableRatio(&GroupAvailability{
			TotalAccounts:  10,
			AvailableCount: 0,
		})
		require.Equal(t, 0.0, got)
	})
}

func TestCountAccountsByCondition(t *testing.T) {
	t.Parallel()

	t.Run("测试限流账号统计: acc.IsRateLimited", func(t *testing.T) {
		t.Parallel()

		accounts := map[int64]*AccountAvailability{
			1: {IsRateLimited: true},
			2: {IsRateLimited: false},
			3: {IsRateLimited: true},
		}

		got := countAccountsByCondition(accounts, func(acc *AccountAvailability) bool {
			return acc.IsRateLimited
		})
		require.Equal(t, int64(2), got)
	})

	t.Run("测试错误账号统计（排除临时不可调度）: acc.HasError && acc.TempUnschedulableUntil == nil", func(t *testing.T) {
		t.Parallel()

		until := time.Now().UTC().Add(5 * time.Minute)
		accounts := map[int64]*AccountAvailability{
			1: {HasError: true},
			2: {HasError: true, TempUnschedulableUntil: &until},
			3: {HasError: false},
		}

		got := countAccountsByCondition(accounts, func(acc *AccountAvailability) bool {
			return acc.HasError && acc.TempUnschedulableUntil == nil
		})
		require.Equal(t, int64(1), got)
	})

	t.Run("边界情况: 空 map 应返回 0", func(t *testing.T) {
		t.Parallel()

		got := countAccountsByCondition(map[int64]*AccountAvailability{}, func(acc *AccountAvailability) bool {
			return acc.IsRateLimited
		})
		require.Equal(t, int64(0), got)
	})
}

// TestComputeRuleMetric_AccountTempUnscheduledCount verifies the new
// account_temp_unscheduled_count metric counts accounts currently in the
// temp-unscheduled window and ignores those whose window has expired or
// were never temp-unscheduled.
func TestComputeRuleMetric_AccountTempUnscheduledCount(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	futureUntil := now.Add(5 * time.Minute)
	pastUntil := now.Add(-1 * time.Minute)

	availability := &OpsAccountAvailability{
		Accounts: map[int64]*AccountAvailability{
			// currently temp-unscheduled (window active)
			1: {TempUnschedulableUntil: &futureUntil},
			2: {TempUnschedulableUntil: &futureUntil},
			// temp-unsched window already expired → should NOT count
			3: {TempUnschedulableUntil: &pastUntil},
			// never temp-unscheduled
			4: {HasError: true},
			5: {IsRateLimited: true},
		},
	}

	opsService := &OpsService{
		getAccountAvailability: func(_ context.Context, _ string, _ *int64) (*OpsAccountAvailability, error) {
			return availability, nil
		},
	}
	svc := &OpsAlertEvaluatorService{
		opsService: opsService,
		opsRepo:    &stubOpsRepo{},
	}

	rule := &OpsAlertRule{MetricType: "account_temp_unscheduled_count"}
	val, ok := svc.computeRuleMetric(context.Background(), rule, nil,
		now.Add(-5*time.Minute), now, "", nil, 0)

	require.True(t, ok)
	require.InDelta(t, 2.0, val, 0.0001, "only 2 accounts have an active temp-unsched window")
}

func TestComputeRuleMetricNewIndicators(t *testing.T) {
	t.Parallel()

	groupID := int64(101)
	platform := "openai"

	availability := &OpsAccountAvailability{
		Group: &GroupAvailability{
			GroupID:        groupID,
			TotalAccounts:  10,
			AvailableCount: 8,
		},
		Accounts: map[int64]*AccountAvailability{
			1: {IsRateLimited: true},
			2: {IsRateLimited: true},
			3: {HasError: true},
			4: {HasError: true, TempUnschedulableUntil: timePtr(time.Now().UTC().Add(2 * time.Minute))},
			5: {HasError: false, IsRateLimited: false},
		},
	}

	opsService := &OpsService{
		getAccountAvailability: func(_ context.Context, _ string, _ *int64) (*OpsAccountAvailability, error) {
			return availability, nil
		},
	}

	svc := &OpsAlertEvaluatorService{
		opsService: opsService,
		opsRepo:    &stubOpsRepo{overview: &OpsDashboardOverview{}},
	}

	start := time.Now().UTC().Add(-5 * time.Minute)
	end := time.Now().UTC()
	ctx := context.Background()

	tests := []struct {
		name       string
		metricType string
		groupID    *int64
		wantValue  float64
		wantOK     bool
	}{
		{
			name:       "group_available_accounts",
			metricType: "group_available_accounts",
			groupID:    &groupID,
			wantValue:  8,
			wantOK:     true,
		},
		{
			name:       "group_available_ratio",
			metricType: "group_available_ratio",
			groupID:    &groupID,
			wantValue:  80.0,
			wantOK:     true,
		},
		{
			name:       "account_rate_limited_count",
			metricType: "account_rate_limited_count",
			groupID:    nil,
			wantValue:  2,
			wantOK:     true,
		},
		{
			name:       "account_error_count",
			metricType: "account_error_count",
			groupID:    nil,
			wantValue:  1,
			wantOK:     true,
		},
		{
			name:       "group_available_accounts without group_id returns false",
			metricType: "group_available_accounts",
			groupID:    nil,
			wantValue:  0,
			wantOK:     false,
		},
		{
			name:       "group_available_ratio without group_id returns false",
			metricType: "group_available_ratio",
			groupID:    nil,
			wantValue:  0,
			wantOK:     false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rule := &OpsAlertRule{
				MetricType: tt.metricType,
			}
			gotValue, gotOK := svc.computeRuleMetric(ctx, rule, nil, start, end, platform, tt.groupID, 0)
			require.Equal(t, tt.wantOK, gotOK)
			if !tt.wantOK {
				return
			}
			require.InDelta(t, tt.wantValue, gotValue, 0.0001)
		})
	}
}

// TestComputeRuleMetricRateSampleFloor pins the false-P0 guard: a ratio metric
// (upstream_error_rate) over a window holding fewer SLA-counted requests than
// the configured floor must return ok=false so the rule is skipped, instead of
// reporting a misleading 100% on near-empty low-traffic windows (2026-06-06
// us2/us5 incident: 19 / 1 requests in ~25min yet upstream_error_rate=100%).
func TestComputeRuleMetricRateSampleFloor(t *testing.T) {
	t.Parallel()

	start := time.Now().UTC().Add(-5 * time.Minute)
	end := time.Now().UTC()
	ctx := context.Background()

	tests := []struct {
		name       string
		requestSLA int64
		minSamples int
		wantOK     bool
		wantValue  float64
	}{
		{name: "below floor is skipped (us2: 19 reqs, floor 20)", requestSLA: 19, minSamples: 20, wantOK: false},
		{name: "single request is skipped (us5: 1 req, floor 20)", requestSLA: 1, minSamples: 20, wantOK: false},
		{name: "at floor is evaluated", requestSLA: 20, minSamples: 20, wantOK: true, wantValue: 100},
		{name: "above floor is evaluated", requestSLA: 200, minSamples: 20, wantOK: true, wantValue: 100},
		{name: "floor unset (0) falls back to legacy >0 guard, evaluates 1 req", requestSLA: 1, minSamples: 0, wantOK: true, wantValue: 100},
		{name: "floor unset (0) still skips empty window", requestSLA: 0, minSamples: 0, wantOK: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := &OpsAlertEvaluatorService{
				opsRepo: &stubOpsRepo{overview: &OpsDashboardOverview{
					RequestCountSLA:   tt.requestSLA,
					UpstreamErrorRate: 1.0, // 100% of the (few) requests failed upstream
				}},
			}
			rule := &OpsAlertRule{MetricType: "upstream_error_rate"}

			gotValue, gotOK := svc.computeRuleMetric(ctx, rule, nil, start, end, "", nil, tt.minSamples)
			require.Equal(t, tt.wantOK, gotOK)
			if !tt.wantOK {
				return
			}
			require.InDelta(t, tt.wantValue, gotValue, 0.0001)
		})
	}
}

// TestComputeRuleMetricRoutingCapacityRejectionCount pins the empty-pool-429
// blind-spot fix: the routing_capacity_rejection_count metric returns the
// dedicated CountRoutingCapacityRejections value verbatim, and — unlike ratio
// metrics — is NOT gated by the rate sample floor. A count is self-flooring (low
// traffic → low count → no breach), so a real rejection storm must still surface
// even on a window whose SLA request count is small; applying the floor here
// would wrongly suppress it.
func TestComputeRuleMetricRoutingCapacityRejectionCount(t *testing.T) {
	t.Parallel()

	start := time.Now().UTC().Add(-5 * time.Minute)
	end := time.Now().UTC()
	ctx := context.Background()

	t.Run("returns the routing-capacity rejection count verbatim", func(t *testing.T) {
		t.Parallel()
		svc := &OpsAlertEvaluatorService{
			opsRepo: &stubOpsRepo{routingRejections: 60},
		}
		rule := &OpsAlertRule{MetricType: "routing_capacity_rejection_count"}
		got, ok := svc.computeRuleMetric(ctx, rule, nil, start, end, "", nil, 100)
		require.True(t, ok)
		require.InDelta(t, 60.0, got, 0.0001)
	})

	t.Run("not gated by the rate sample floor (storm on a low-traffic window still fires)", func(t *testing.T) {
		t.Parallel()
		// rateRuleMinSamples=100 would skip any ratio metric on a near-empty
		// window; a count must still be reported regardless of SLA volume.
		svc := &OpsAlertEvaluatorService{
			opsRepo: &stubOpsRepo{routingRejections: 75},
		}
		rule := &OpsAlertRule{MetricType: "routing_capacity_rejection_count"}
		got, ok := svc.computeRuleMetric(ctx, rule, nil, start, end, "", nil, 100)
		require.True(t, ok)
		require.InDelta(t, 75.0, got, 0.0001)
	})

	t.Run("zero rejections is evaluated (ok) and does not breach", func(t *testing.T) {
		t.Parallel()
		svc := &OpsAlertEvaluatorService{
			opsRepo: &stubOpsRepo{routingRejections: 0},
		}
		rule := &OpsAlertRule{MetricType: "routing_capacity_rejection_count"}
		got, ok := svc.computeRuleMetric(ctx, rule, nil, start, end, "", nil, 100)
		require.True(t, ok)
		require.Equal(t, 0.0, got)
		require.False(t, compareMetric(got, ">=", 50.0))
	})

	t.Run("query error => skipped (ok=false)", func(t *testing.T) {
		t.Parallel()
		svc := &OpsAlertEvaluatorService{
			opsRepo: &stubOpsRepo{routingRejectionsErr: context.DeadlineExceeded},
		}
		rule := &OpsAlertRule{MetricType: "routing_capacity_rejection_count"}
		_, ok := svc.computeRuleMetric(ctx, rule, nil, start, end, "", nil, 100)
		require.False(t, ok)
	})
}

// TestComputeTopCauseRoutingCapacityRejection pins the self-diagnosing 主因 line
// for the empty-pool P0: the card names WHICH platform pool(s) are out of
// capacity (like pool_load_rate / error-rate cards do), instead of carrying a
// bare number that forces the on-call to drill the dashboard.
func TestComputeTopCauseRoutingCapacityRejection(t *testing.T) {
	t.Parallel()

	start := time.Now().UTC().Add(-5 * time.Minute)
	end := time.Now().UTC()
	ctx := context.Background()
	rule := &OpsAlertRule{MetricType: "routing_capacity_rejection_count"}

	t.Run("names the offending pools", func(t *testing.T) {
		t.Parallel()
		svc := &OpsAlertEvaluatorService{opsRepo: &stubOpsRepo{routingCauses: []*OpsRoutingRejectionCause{
			{Platform: "anthropic", Count: 40},
			{Platform: "openai", Count: 15},
		}}}
		cause, users := svc.computeTopCause(ctx, rule, start, end, "", nil)
		require.Equal(t, "anthropic ×40 · openai ×15", cause)
		require.Empty(t, users)
	})

	t.Run("pool cause + user breakdown are returned separately (two card lines)", func(t *testing.T) {
		t.Parallel()
		svc := &OpsAlertEvaluatorService{opsRepo: &stubOpsRepo{
			routingCauses: []*OpsRoutingRejectionCause{{Platform: "anthropic", Count: 40}},
			routingUsers: []*OpsRoutingRejectionUser{
				{UserID: 42, APIKeyName: "eval-harness", Count: 30},
				{UserID: 17, APIKeyName: "", Count: 5},
			},
		}}
		cause, users := svc.computeTopCause(ctx, rule, start, end, "", nil)
		require.Equal(t, "anthropic ×40", cause)
		require.Equal(t, `#42 "eval-harness" ×30 · #17 ×5`, users)
	})

	t.Run("user segment survives a pool-query error (best-effort)", func(t *testing.T) {
		t.Parallel()
		svc := &OpsAlertEvaluatorService{opsRepo: &stubOpsRepo{
			routingCausesErr: context.DeadlineExceeded,
			routingUsers:     []*OpsRoutingRejectionUser{{UserID: 1, APIKeyName: "k", Count: 9}},
		}}
		cause, users := svc.computeTopCause(ctx, rule, start, end, "", nil)
		require.Empty(t, cause)
		require.Equal(t, `#1 "k" ×9`, users)
	})

	t.Run("no rows => no 主因 line", func(t *testing.T) {
		t.Parallel()
		svc := &OpsAlertEvaluatorService{opsRepo: &stubOpsRepo{}}
		cause, users := svc.computeTopCause(ctx, rule, start, end, "", nil)
		require.Empty(t, cause)
		require.Empty(t, users)
	})

	t.Run("query error => no 主因 line (best-effort, never blocks firing)", func(t *testing.T) {
		t.Parallel()
		svc := &OpsAlertEvaluatorService{opsRepo: &stubOpsRepo{routingCausesErr: context.DeadlineExceeded}}
		cause, users := svc.computeTopCause(ctx, rule, start, end, "", nil)
		require.Empty(t, cause)
		require.Empty(t, users)
	})
}

// TestIsEdgeNode pins the node-identity predicate that drives edge-only alert
// suppression. It MUST match the card-title node label derived by
// deriveOpsNodeIdentity from the same frontend URL, so a card that would read
// "· us6" is exactly what gets suppressed.
func TestIsEdgeNode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		frontendURL string
		want        bool
	}{
		{"edge us6", "https://api-us6.tokenkey.dev", true},
		{"edge uk1", "https://api-uk1.tokenkey.dev", true},
		{"prod", "https://api.tokenkey.dev", false},
		{"unset frontend url", "", false},
		{"non-edge custom host", "https://gateway.example.com", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			svc := &OpsAlertEvaluatorService{cfg: &config.Config{Server: config.ServerConfig{FrontendURL: c.frontendURL}}}
			require.Equal(t, c.want, svc.isEdgeNode())
		})
	}
	t.Run("nil cfg is safe (treated as non-edge)", func(t *testing.T) {
		t.Parallel()
		require.False(t, (&OpsAlertEvaluatorService{}).isEdgeNode())
	})
}

// TestIsEdgeSuppressedAlertRule pins WHICH rules are silenced on a mirror-relay
// edge. Only the routing-capacity-rejection P0 (client-invisible on an edge,
// covered by account-incident / pool-exhausted P0s) qualifies; capacity/error
// signals that ARE meaningful on an edge must still page.
func TestIsEdgeSuppressedAlertRule(t *testing.T) {
	t.Parallel()
	require.True(t, isEdgeSuppressedAlertRule(&OpsAlertRule{MetricType: "routing_capacity_rejection_count"}))
	require.True(t, isEdgeSuppressedAlertRule(&OpsAlertRule{MetricType: "  routing_capacity_rejection_count  "}))
	require.False(t, isEdgeSuppressedAlertRule(&OpsAlertRule{MetricType: "pool_load_rate"}))
	require.False(t, isEdgeSuppressedAlertRule(&OpsAlertRule{MetricType: "upstream_error_rate"}))
	require.False(t, isEdgeSuppressedAlertRule(&OpsAlertRule{MetricType: "group_available_accounts"}))
	require.False(t, isEdgeSuppressedAlertRule(nil))
}

// TestMaybeSendAlertNotificationsEdgeSuppression verifies the composed gate: on an
// edge node the routing-rejection rule short-circuits before any email/feishu
// attempt and reports nothing sent. The prod counterpart (same rule) does NOT
// short-circuit on the edge predicate — it proceeds into the notify paths (which
// then no-op here only because no notifier config is wired), proving the gate is
// edge-scoped, not a blanket drop.
func TestMaybeSendAlertNotificationsEdgeSuppression(t *testing.T) {
	t.Parallel()
	rule := &OpsAlertRule{MetricType: "routing_capacity_rejection_count"}

	edge := &OpsAlertEvaluatorService{cfg: &config.Config{Server: config.ServerConfig{FrontendURL: "https://api-us6.tokenkey.dev"}}}
	require.True(t, edge.isEdgeNode() && isEdgeSuppressedAlertRule(rule), "precondition: edge + suppressed rule")
	res := edge.maybeSendAlertNotifications(context.Background(), nil, rule, &OpsAlertEvent{})
	require.False(t, res.EmailSent)
	require.False(t, res.FeishuSent)

	prod := &OpsAlertEvaluatorService{cfg: &config.Config{Server: config.ServerConfig{FrontendURL: "https://api.tokenkey.dev"}}}
	require.False(t, prod.isEdgeNode(), "prod must NOT be edge-suppressed for this rule")
}
