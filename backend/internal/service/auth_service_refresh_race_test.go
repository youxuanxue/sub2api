//go:build unit

package service_test

import (
	"context"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// barrierRefreshTokenCacheStub 让所有并发请求都先读到旧 token，再统一放行，
// 从而确定性地验证 TokenKey 短宽限期对并发旧快照的处理。
type barrierRefreshTokenCacheStub struct {
	*emailBindRefreshTokenCacheStub

	target  int
	barrier chan struct{}
	mu      sync.Mutex
	reads   int
}

func newBarrierRefreshTokenCacheStub(target int) *barrierRefreshTokenCacheStub {
	return &barrierRefreshTokenCacheStub{
		emailBindRefreshTokenCacheStub: newEmailBindRefreshTokenCacheStub(),
		target:                         target,
		barrier:                        make(chan struct{}),
	}
}

func (s *barrierRefreshTokenCacheStub) GetRefreshToken(ctx context.Context, tokenHash string) (*service.RefreshTokenData, error) {
	data, err := s.emailBindRefreshTokenCacheStub.GetRefreshToken(ctx, tokenHash)

	s.mu.Lock()
	s.reads++
	if s.reads == s.target {
		close(s.barrier)
	}
	s.mu.Unlock()
	<-s.barrier
	return data, err
}

// TokenKey 的短宽限期允许同一旧 token 并发刷新，仍必须留在原用户和 family。
func TestAuthServiceRefreshTokenPairConcurrentReadsStayWithinGrace(t *testing.T) {
	ctx := context.Background()
	const workers = 8
	cache := newBarrierRefreshTokenCacheStub(workers)

	user := &service.User{
		ID:           41,
		Email:        "refresh-race@example.com",
		Username:     "refresh-race",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
		TokenVersion: 1,
	}
	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:                   "test-refresh-race-secret",
			ExpireHour:               1,
			AccessTokenExpireMinutes: 60,
			RefreshTokenExpireDays:   7,
		},
	}
	svc := service.NewAuthService(nil, newEmailBindUserRepoStub(user), nil, cache, cfg, nil, nil, nil, nil, nil, nil, nil, nil)

	pair, err := svc.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)

	start := make(chan struct{})
	errs := make(chan error, workers)
	pairs := make(chan *service.TokenPairWithUser, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			refreshed, refreshErr := svc.RefreshTokenPair(ctx, pair.RefreshToken)
			errs <- refreshErr
			pairs <- refreshed
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for refreshErr := range errs {
		require.NoError(t, refreshErr)
	}
	close(pairs)
	for refreshed := range pairs {
		require.NotNil(t, refreshed)
		require.Equal(t, service.RoleUser, refreshed.UserRole)
		require.EqualValues(t, 3600, refreshed.ExpiresIn)
		require.NotEqual(t, pair.RefreshToken, refreshed.RefreshToken)
	}
	require.Len(t, cache.families, 1, "concurrent rotation must preserve the original family")
	require.Len(t, cache.tokens, workers+1, "rotated old token remains during grace alongside every replacement")
	for _, data := range cache.tokens {
		require.Equal(t, user.ID, data.UserID)
		_, sameFamily := cache.families[data.FamilyID]
		require.True(t, sameFamily)
	}
}
