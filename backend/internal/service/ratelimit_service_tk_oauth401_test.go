//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// newOAuth401AnthropicAccount 造一个 anthropic OAuth 账号，access_token 在 expiresAt 过期。
func newOAuth401AnthropicAccount(id int64, expiresAt time.Time) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"expires_at":    expiresAt.UTC().Format(time.RFC3339),
		},
	}
}

// 仍然有效的 access_token 上吃 401 = grant 被吊销 → 第一次即 SetError 永久停调度 + 告警，
// 不走 temp_unschedulable 冷却，错误信息提示需人工重授权。
func TestRateLimitService_OAuth401_ValidTokenDisablesFirstStrike(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	blocker := &runtimeBlockRecorder{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAccountRuntimeBlocker(blocker)
	account := newOAuth401AnthropicAccount(701, time.Now().Add(2*time.Hour))

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "valid-token 401 must SetError on first strike")
	require.Equal(t, 0, repo.tempCalls, "must NOT fall through to temp_unschedulable cooldown")
	require.Contains(t, repo.lastErrorMsg, "still-valid access token")
	require.Contains(t, repo.lastErrorMsg, "grant revoked upstream")
	require.Contains(t, repo.lastErrorMsg, "manual re-authorization")
	require.Len(t, blocker.accounts, 1, "disable must notify scheduling-blocked for alerting")
	require.Equal(t, account.ID, blocker.accounts[0].ID)
	require.Equal(t, "auth_error", blocker.reasons[0])
}

// access_token 已过期 → 过期抢跑良性 401 → 回退冷却，不永久禁用。
func TestRateLimitService_OAuth401_ExpiredTokenFallsThroughToCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := newOAuth401AnthropicAccount(702, time.Now().Add(-1*time.Hour))

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "expired-token 401 must not permanently disable")
	require.Equal(t, 1, repo.tempCalls, "expired-token 401 falls through to temp_unschedulable cooldown")
}

// access_token 在刷新窗口内（近过期，剩余 < margin floor 5min）→ 视为良性抢跑 → 回退冷却。
func TestRateLimitService_OAuth401_NearExpiryFallsThroughToCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := newOAuth401AnthropicAccount(703, time.Now().Add(1*time.Minute))

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "near-expiry 401 must fall through to cooldown")
	require.Equal(t, 1, repo.tempCalls)
}

// expires_at 缺失（无法判断有效性）→ fail-safe 回退冷却，绝不永久禁用。
func TestRateLimitService_OAuth401_MissingExpiryFallsThroughToCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       704,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt", // 无 expires_at
		},
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "unknown expiry must fail-safe to cooldown, not disable")
	require.Equal(t, 1, repo.tempCalls)
}

// 余量取刷新窗口：RefreshBeforeExpiryHours=1 → 窗口 1h。45min 剩余 < 1h 视为近过期回退冷却；
// 90min 剩余 ≥ 1h 视为 solidly valid → 第一次即禁。
func TestRateLimitService_OAuth401_MarginUsesRefreshWindow(t *testing.T) {
	cfg := &config.Config{}
	cfg.TokenRefresh.RefreshBeforeExpiryHours = 1

	repo1 := &rateLimitAccountRepoStub{}
	svc1 := NewRateLimitService(repo1, nil, cfg, nil, nil)
	within := newOAuth401AnthropicAccount(705, time.Now().Add(45*time.Minute))
	require.True(t, svc1.HandleUpstreamError(context.Background(), within, 401, http.Header{}, []byte("unauthorized")))
	require.Equal(t, 0, repo1.setErrorCalls, "45min < 1h refresh window → near-expiry → cooldown")
	require.Equal(t, 1, repo1.tempCalls)

	repo2 := &rateLimitAccountRepoStub{}
	svc2 := NewRateLimitService(repo2, nil, cfg, nil, nil)
	beyond := newOAuth401AnthropicAccount(706, time.Now().Add(90*time.Minute))
	require.True(t, svc2.HandleUpstreamError(context.Background(), beyond, 401, http.Header{}, []byte("unauthorized")))
	require.Equal(t, 1, repo2.setErrorCalls, "90min ≥ 1h refresh window → solidly valid → disable")
	require.Equal(t, 0, repo2.tempCalls)
}

// 缺 refresh_token 的 OAuth 账号：任意 401 立即 SetError（结构性不可自愈，既有行为，
// 在 valid-token 判定之前短路）。
func TestRateLimitService_OAuth401_MissingRefreshTokenImmediateDisable(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       707,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			// 有效但无 refresh_token：缺 refresh_token 分支先于 valid-token 判定。
			"expires_at": time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		},
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Contains(t, repo.lastErrorMsg, "refresh_token missing")
}

// Claude API 故障期间：有效-token 401 不永久禁用（防上游对全队有效 token 误发 401 时批量
// 禁全池），改回退 temp_unschedulable 冷却；故障结束后仍 401 才禁。与 403/429 路径同口径。
func TestRateLimitService_OAuth401_ValidTokenDeferredDuringClaudeIncident(t *testing.T) {
	setClaudeStatusForTest(t, ClaudeStatusSnapshot{IsIncident: true, Status: "major_outage", FetchedAt: time.Now()})
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := newOAuth401AnthropicAccount(708, time.Now().Add(2*time.Hour)) // solidly valid

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "incident 期间 valid-token 401 不得永久禁用")
	require.Equal(t, 1, repo.tempCalls, "incident 期间回退 temp_unschedulable 冷却")
}

// Claude API 故障期间：setup-token 账号的 401 也走统一豁免（顶层 anthropic gate 覆盖
// 非-OAuth/缺-refresh 等所有子路径），不永久禁用、改回退冷却。
func TestRateLimitService_OAuth401_SetupTokenDeferredDuringClaudeIncident(t *testing.T) {
	setClaudeStatusForTest(t, ClaudeStatusSnapshot{IsIncident: true, Status: "partial_outage", FetchedAt: time.Now()})
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       709,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
		Credentials: map[string]any{
			"expires_at": time.Now().Add(300 * 24 * time.Hour).UTC().Format(time.RFC3339),
		},
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "incident 期间 setup-token 401 不得永久禁用")
	require.Equal(t, 1, repo.tempCalls, "incident 期间回退 temp_unschedulable 冷却")
}

// 反向校准：incident gate 只对 anthropic 生效。Claude API 故障期间，一个 OpenAI OAuth
// 账号的有效-token 401 仍照常永久禁用（Anthropic 状态与它无关，不得被误延后）。
func TestRateLimitService_OAuth401_NonAnthropicNotDeferredDuringClaudeIncident(t *testing.T) {
	setClaudeStatusForTest(t, ClaudeStatusSnapshot{IsIncident: true, Status: "major_outage", FetchedAt: time.Now()})
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       710,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339), // solidly valid
		},
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "non-anthropic valid-token 401 不受 Claude incident gate 影响，照常永久禁用")
	require.Equal(t, 0, repo.tempCalls)
}
