package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// bannedShortCircuitRepo 在 forceRefreshRepo 之上补 ClearError，因为可恢复-错误用例会
// 走到成功路径的 tryClearRecoverableAccountError（status=error + 可恢复消息 → ClearError）。
type bannedShortCircuitRepo struct {
	forceRefreshRepo
	clearErrorCalls int
}

func (r *bannedShortCircuitRepo) ClearError(context.Context, int64) error {
	r.clearErrorCalls++
	return nil
}

// Gap C: 被上游封禁(status=error 且 forbidden 类错误)的 anthropic OAuth 账号,admin
// 用量查询(active)必须短路、不打上游——否则查看卡片/前端轮询会给已封 org 再加一次 403。
func TestAccountUsageService_GetUsage_BannedAccountSkipsUpstream(t *testing.T) {
	account := Account{
		ID:           8801,
		Platform:     PlatformAnthropic,
		Type:         AccountTypeOAuth,
		Status:       StatusError,
		ErrorMessage: "Organization OAuth ban (403): OAuth authentication is currently not allowed for this organization",
		Credentials:  map[string]any{"access_token": "tkn"},
	}
	fetcher := &countingUsageFetcher{resp: &ClaudeUsageResponse{}}
	cache := NewUsageCache()
	cache.windowStatsCache.Store(account.ID, &windowStatsCache{stats: &WindowStats{}, timestamp: time.Now()})
	svc := &AccountUsageService{
		accountRepo:  &forceRefreshRepo{stubOpenAIAccountRepo{accounts: []Account{account}}},
		usageFetcher: fetcher,
		cache:        cache,
	}

	info, err := svc.GetUsage(context.Background(), account.ID) // active source, force=false
	require.NoError(t, err)
	require.NotNil(t, info)
	require.Equal(t, 0, fetcher.calls, "banned account must NOT hit the upstream usage endpoint")
	require.True(t, info.IsForbidden, "stored forbidden error must be surfaced instead of a live fetch")
}

// 即便 force=true（手动「查询」），被封账号仍短路不打上游。
func TestAccountUsageService_GetUsage_BannedAccountForceStillSkipsUpstream(t *testing.T) {
	account := Account{
		ID:           8803,
		Platform:     PlatformAnthropic,
		Type:         AccountTypeOAuth,
		Status:       StatusError,
		ErrorMessage: "Persistent bodyless/unstructured Anthropic 403 (likely org ban or infra/WAF block)",
		Credentials:  map[string]any{"access_token": "tkn"},
	}
	fetcher := &countingUsageFetcher{resp: &ClaudeUsageResponse{}}
	cache := NewUsageCache()
	cache.windowStatsCache.Store(account.ID, &windowStatsCache{stats: &WindowStats{}, timestamp: time.Now()})
	svc := &AccountUsageService{
		accountRepo:  &forceRefreshRepo{stubOpenAIAccountRepo{accounts: []Account{account}}},
		usageFetcher: fetcher,
		cache:        cache,
	}

	_, err := svc.GetUsage(context.Background(), account.ID, true)
	require.NoError(t, err)
	require.Equal(t, 0, fetcher.calls, "force query on a banned account must still skip upstream")
}

// 可恢复 token 错误(status=error 但非 forbidden)不在 Gap C scope——仍走实时拉取,保留
// 既有「成功一次即清错误」自愈探测,不被回退。
func TestAccountUsageService_GetUsage_RecoverableErrorStillFetches(t *testing.T) {
	account := Account{
		ID:           8802,
		Platform:     PlatformAnthropic,
		Type:         AccountTypeOAuth,
		Status:       StatusError,
		ErrorMessage: "Token refresh failed (non-retryable): invalid_grant",
		Credentials:  map[string]any{"access_token": "tkn"},
	}
	fetcher := &countingUsageFetcher{resp: &ClaudeUsageResponse{}}
	cache := NewUsageCache()
	cache.windowStatsCache.Store(account.ID, &windowStatsCache{stats: &WindowStats{}, timestamp: time.Now()})
	repo := &bannedShortCircuitRepo{forceRefreshRepo: forceRefreshRepo{stubOpenAIAccountRepo{accounts: []Account{account}}}}
	svc := &AccountUsageService{
		accountRepo:  repo,
		usageFetcher: fetcher,
		cache:        cache,
	}

	_, err := svc.GetUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, 1, fetcher.calls, "recoverable token error must still probe upstream (auto-recovery preserved)")
	require.Equal(t, 1, repo.clearErrorCalls, "successful probe on a recoverable error must clear it (auto-recovery)")
}
