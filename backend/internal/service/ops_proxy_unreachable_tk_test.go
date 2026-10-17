//go:build unit

package service

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type stubProxyRepoForUnreachable struct {
	proxies             []ProxyWithAccountCount
	schedulableCounts   map[int64]int64
	err                 error
	schedulableCountErr error
}

var _ ProxyRepository = (*stubProxyRepoForUnreachable)(nil)

func (s *stubProxyRepoForUnreachable) ListActiveWithAccountCount(context.Context) ([]ProxyWithAccountCount, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]ProxyWithAccountCount(nil), s.proxies...), nil
}

func (s *stubProxyRepoForUnreachable) CountSchedulableActiveByProxyIDs(_ context.Context, proxyIDs []int64) (map[int64]int64, error) {
	if s.schedulableCountErr != nil {
		return nil, s.schedulableCountErr
	}
	out := make(map[int64]int64, len(proxyIDs))
	for _, id := range proxyIDs {
		if s.schedulableCounts != nil {
			out[id] = s.schedulableCounts[id]
			continue
		}
		// Default: treat AccountCount as the schedulable binding count.
		for _, p := range s.proxies {
			if p.ID == id {
				out[id] = p.AccountCount
				break
			}
		}
	}
	return out, nil
}

// Satisfy unused interface methods that panic if accidentally called.
func (s *stubProxyRepoForUnreachable) Create(context.Context, *Proxy) error { panic("unused") }
func (s *stubProxyRepoForUnreachable) GetByID(context.Context, int64) (*Proxy, error) {
	panic("unused")
}
func (s *stubProxyRepoForUnreachable) ListByIDs(context.Context, []int64) ([]Proxy, error) {
	panic("unused")
}
func (s *stubProxyRepoForUnreachable) Update(context.Context, *Proxy) error { panic("unused") }
func (s *stubProxyRepoForUnreachable) Delete(context.Context, int64) error  { panic("unused") }
func (s *stubProxyRepoForUnreachable) List(context.Context, pagination.PaginationParams) ([]Proxy, *pagination.PaginationResult, error) {
	panic("unused")
}
func (s *stubProxyRepoForUnreachable) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string) ([]Proxy, *pagination.PaginationResult, error) {
	panic("unused")
}
func (s *stubProxyRepoForUnreachable) ListWithFiltersAndAccountCount(context.Context, pagination.PaginationParams, string, string, string) ([]ProxyWithAccountCount, *pagination.PaginationResult, error) {
	panic("unused")
}
func (s *stubProxyRepoForUnreachable) ListActive(context.Context) ([]Proxy, error) {
	panic("unused")
}
func (s *stubProxyRepoForUnreachable) ExistsByHostPortAuth(context.Context, string, int, string, string) (bool, error) {
	panic("unused")
}
func (s *stubProxyRepoForUnreachable) CountAccountsByProxyID(context.Context, int64) (int64, error) {
	panic("unused")
}
func (s *stubProxyRepoForUnreachable) ListAccountSummariesByProxyID(context.Context, int64) ([]ProxyAccountSummary, error) {
	panic("unused")
}
func (s *stubProxyRepoForUnreachable) SweepExpiredProxies(context.Context, time.Time) (int64, error) {
	panic("unused")
}
func (s *stubProxyRepoForUnreachable) ListAllForFallback(context.Context) ([]Proxy, error) {
	panic("unused")
}
func (s *stubProxyRepoForUnreachable) CountExpired(context.Context) (int64, error) {
	panic("unused")
}
func (s *stubProxyRepoForUnreachable) CountExpiringSoon(context.Context, time.Time) (int64, error) {
	panic("unused")
}

func TestComputeProxyUnreachableCount_SkipsUnschedulableOnlyBindings(t *testing.T) {
	t.Parallel()

	repo := &stubProxyRepoForUnreachable{
		proxies: []ProxyWithAccountCount{
			{Proxy: Proxy{ID: 5, Name: "jp-us5", Host: "52.68.182.0", Port: 1080, Status: StatusActive}, AccountCount: 10},
		},
		schedulableCounts: map[int64]int64{5: 0},
	}
	svc := &OpsAlertEvaluatorService{proxyRepo: repo}
	dialed := 0
	svc.SetProxyTCPDialForTest(func(context.Context, string, string) error {
		dialed++
		return errors.New("should not dial")
	})

	n, ok := svc.computeProxyUnreachableCount(context.Background())
	require.True(t, ok)
	require.Equal(t, 0.0, n)
	require.Equal(t, 0, dialed)
}

func TestComputeProxyUnreachableCount_CountsDialFailuresOnlyForBoundProxies(t *testing.T) {
	t.Parallel()

	repo := &stubProxyRepoForUnreachable{
		proxies: []ProxyWithAccountCount{
			{Proxy: Proxy{ID: 5, Name: "jp-us5", Host: "52.68.182.0", Port: 1080, Status: StatusActive}, AccountCount: 10},
			{Proxy: Proxy{ID: 4, Name: "jp-us4", Host: "35.77.89.227", Port: 1080, Status: StatusActive}, AccountCount: 21},
			{Proxy: Proxy{ID: 99, Name: "idle", Host: "1.2.3.4", Port: 1080, Status: StatusActive}, AccountCount: 0},
		},
	}
	svc := &OpsAlertEvaluatorService{proxyRepo: repo}
	svc.SetProxyTCPDialForTest(func(_ context.Context, _ string, address string) error {
		host, _, err := net.SplitHostPort(address)
		require.NoError(t, err)
		if host == "52.68.182.0" {
			return errors.New("i/o timeout")
		}
		return nil
	})

	n, ok := svc.computeProxyUnreachableCount(context.Background())
	require.True(t, ok)
	require.Equal(t, 1.0, n)

	cause, users, models := svc.computeTopCause(context.Background(), &OpsAlertRule{
		MetricType: OpsAlertMetricProxyUnreachableCount,
	}, time.Time{}, time.Time{}, "", nil)
	require.Contains(t, cause, "jp-us5")
	require.Contains(t, cause, "52.68.182.0:1080")
	require.Contains(t, cause, "i/o timeout")
	require.NotContains(t, cause, "jp-us4")
	require.NotContains(t, cause, "idle")
	require.Empty(t, users)
	require.Empty(t, models)
}

func TestComputeProxyUnreachableCount_AllReachableIsZero(t *testing.T) {
	t.Parallel()

	repo := &stubProxyRepoForUnreachable{
		proxies: []ProxyWithAccountCount{
			{Proxy: Proxy{ID: 2, Name: "jp-uk1", Host: "46.51.238.48", Port: 1080, Status: StatusActive}, AccountCount: 3},
		},
	}
	svc := &OpsAlertEvaluatorService{proxyRepo: repo}
	svc.SetProxyTCPDialForTest(func(context.Context, string, string) error { return nil })

	n, ok := svc.computeProxyUnreachableCount(context.Background())
	require.True(t, ok)
	require.Equal(t, 0.0, n)
	require.Empty(t, formatProxyUnreachableCause(svc.loadProxyUnreachableSnapshot()))
}

func TestComputeProxyUnreachableCount_RepoErrorSkipsRule(t *testing.T) {
	t.Parallel()

	svc := &OpsAlertEvaluatorService{
		proxyRepo: &stubProxyRepoForUnreachable{err: errors.New("db down")},
	}
	n, ok := svc.computeProxyUnreachableCount(context.Background())
	require.False(t, ok)
	require.Equal(t, 0.0, n)
}

func TestProxyUnreachable_ProdOnlyAndEdgeSuppressed(t *testing.T) {
	t.Parallel()

	rule := &OpsAlertRule{MetricType: OpsAlertMetricProxyUnreachableCount}
	require.True(t, isProdOnlyAlertRule(rule))
	require.True(t, isEdgeSuppressedAlertRule(rule))

	edge := &OpsAlertEvaluatorService{cfg: &config.Config{Server: config.ServerConfig{FrontendURL: "https://api-us5.tokenkey.dev"}}}
	require.False(t, edge.shouldEvaluateAlertRuleOnNode(rule))

	prod := &OpsAlertEvaluatorService{cfg: &config.Config{Server: config.ServerConfig{FrontendURL: "https://api.tokenkey.dev"}}}
	require.True(t, prod.shouldEvaluateAlertRuleOnNode(rule))
}

func TestFormatProxyUnreachableCause(t *testing.T) {
	t.Parallel()
	require.Empty(t, formatProxyUnreachableCause(nil))
	got := formatProxyUnreachableCause([]proxyUnreachableInfo{
		{ID: 5, Name: "jp-us5", Host: "52.68.182.0", Port: 1080, DialErr: "i/o timeout"},
		{ID: 7, Name: "", Host: "10.0.0.1", Port: 1080, DialErr: ""},
	})
	require.Equal(t, "jp-us5 52.68.182.0:1080 (i/o timeout); proxy#7 10.0.0.1:1080", got)
}
