package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	gocache "github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
)

type userGroupRateResolverRepoStub struct {
	UserGroupRateRepository

	rate  *float64
	err   error
	calls int
	get   func(context.Context) (*float64, error)
}

func (s *userGroupRateResolverRepoStub) GetByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error) {
	s.calls++
	if s.get != nil {
		return s.get(ctx)
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.rate, nil
}

func TestNewUserGroupRateResolver_Defaults(t *testing.T) {
	resolver := newUserGroupRateResolver(nil, nil, 0, nil, "")

	require.NotNil(t, resolver)
	require.NotNil(t, resolver.cache)
	require.Equal(t, defaultUserGroupRateCacheTTL, resolver.cacheTTL)
	require.NotNil(t, resolver.sf)
	require.Equal(t, "service.gateway", resolver.logComponent)
}

func TestUserGroupRateResolverResolve_FallbackForNilResolverAndInvalidIDs(t *testing.T) {
	var nilResolver *userGroupRateResolver
	require.Equal(t, 1.4, nilResolver.Resolve(context.Background(), 101, 202, 1.4))

	resolver := newUserGroupRateResolver(nil, nil, time.Second, nil, "service.test")
	require.Equal(t, 1.4, resolver.Resolve(context.Background(), 0, 202, 1.4))
	require.Equal(t, 1.4, resolver.Resolve(context.Background(), 101, 0, 1.4))
}

func TestUserGroupRateResolverResolve_InvalidCacheEntryLoadsRepoAndCaches(t *testing.T) {
	resetGatewayHotpathStatsForTest()

	rate := 1.7
	repo := &userGroupRateResolverRepoStub{rate: &rate}
	cache := gocache.New(time.Minute, time.Minute)
	cache.Set("101:202", "bad-cache", time.Minute)
	resolver := newUserGroupRateResolver(repo, cache, time.Minute, nil, "service.test")

	got := resolver.Resolve(context.Background(), 101, 202, 1.2)
	require.Equal(t, rate, got)
	require.Equal(t, 1, repo.calls)

	cached, ok := cache.Get("101:202")
	require.True(t, ok)
	require.Equal(t, rate, cached)

	hit, miss, load, _, fallback := GatewayUserGroupRateCacheStats()
	require.Equal(t, int64(0), hit)
	require.Equal(t, int64(1), miss)
	require.Equal(t, int64(1), load)
	require.Equal(t, int64(0), fallback)
}

func TestGatewayServiceGetUserGroupRateMultiplier_FallbacksAndUsesExistingResolver(t *testing.T) {
	var nilSvc *GatewayService
	require.Equal(t, 1.3, nilSvc.getUserGroupRateMultiplier(context.Background(), 101, 202, 1.3))

	rate := 1.9
	repo := &userGroupRateResolverRepoStub{rate: &rate}
	resolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "service.gateway")
	svc := &GatewayService{userGroupRateResolver: resolver}

	got := svc.getUserGroupRateMultiplier(context.Background(), 101, 202, 1.2)
	require.Equal(t, rate, got)
	require.Equal(t, 1, repo.calls)
}

func TestUserGroupRateResolverResolve_QueryFailureUsesOneAndRecovers(t *testing.T) {
	for _, groupDefault := range []float64{0.25, 1.6} {
		repo := &userGroupRateResolverRepoStub{err: errors.New("database unavailable")}
		resolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "service.test")
		require.Equal(t, 1.0, resolver.Resolve(context.Background(), 101, 202, groupDefault))
		_, cached := resolver.cache.Get("101:202")
		require.False(t, cached, "a transient fallback must not replace the user's rate")

		rate := 0.6
		repo.err, repo.rate = nil, &rate
		require.Equal(t, rate, resolver.Resolve(context.Background(), 101, 202, groupDefault))
		require.Equal(t, 2, repo.calls)
	}
}

func TestUserGroupRateResolverResolve_NoOverrideUsesGroupDefault(t *testing.T) {
	repo := &userGroupRateResolverRepoStub{}
	resolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "service.test")
	require.Equal(t, 0.25, resolver.Resolve(context.Background(), 101, 202, 0.25))
	require.Equal(t, 0.25, resolver.Resolve(context.Background(), 101, 202, 0.25))
	require.Equal(t, 1, repo.calls)
}

func TestUserGroupRateResolverResolve_CanceledLeaderDoesNotCancelSharedRead(t *testing.T) {
	resetGatewayHotpathStatsForTest()
	started := make(chan context.Context, 1)
	unblock := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(unblock) })
	rate := 0.25
	repo := &userGroupRateResolverRepoStub{get: func(ctx context.Context) (*float64, error) {
		started <- ctx
		<-unblock
		return &rate, ctx.Err()
	}}
	resolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "service.test")
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan float64, 2)
	go func() { results <- resolver.Resolve(caller, 101, 202, 1.6) }()
	queryCtx := <-started
	go func() { results <- resolver.Resolve(context.Background(), 101, 202, 1.6) }()
	require.Eventually(t, func() bool {
		return userGroupRateCacheMissTotal.Load() == 2
	}, time.Second, time.Millisecond)
	cancel()
	release.Do(func() { close(unblock) })
	for range 2 {
		require.Equal(t, rate, <-results)
	}
	require.Equal(t, 1, repo.calls)
	deadline, bounded := queryCtx.Deadline()
	require.True(t, bounded, "detached queries must still have a time limit")
	require.WithinDuration(t, time.Now(), deadline, 4*time.Second)
}
