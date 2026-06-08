//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// oauth401AfterRefreshCounterStub 是 OAuth401AfterRefreshCounterCache 的可控 fake：
// counts / sameCounts 按调用顺序弹出 version-bump / same-version 返回值（空则返回 0，
// 对应「种 baseline / 不计数」）；err 注入故障。
type oauth401AfterRefreshCounterStub struct {
	counts          []int64
	sameCounts      []int64
	recordIDs       []int64
	recordVersions  []int64
	recordWindows   []int
	recordDebounces []int
	resetCalls      []int64
	err             error
}

func (s *oauth401AfterRefreshCounterStub) RecordOAuth401AfterRefresh(_ context.Context, accountID int64, tokenVersion int64, windowMinutes, debounceSeconds int) (int64, int64, error) {
	s.recordIDs = append(s.recordIDs, accountID)
	s.recordVersions = append(s.recordVersions, tokenVersion)
	s.recordWindows = append(s.recordWindows, windowMinutes)
	s.recordDebounces = append(s.recordDebounces, debounceSeconds)
	if s.err != nil {
		return 0, 0, s.err
	}
	var count, same int64
	if len(s.counts) > 0 {
		count = s.counts[0]
		s.counts = s.counts[1:]
	}
	if len(s.sameCounts) > 0 {
		same = s.sameCounts[0]
		s.sameCounts = s.sameCounts[1:]
	}
	return count, same, nil
}

func (s *oauth401AfterRefreshCounterStub) ResetOAuth401AfterRefresh(_ context.Context, accountID int64) error {
	s.resetCalls = append(s.resetCalls, accountID)
	return nil
}

func newOAuth401AnthropicAccount(id int64, tokenVersion int64) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token":  "rt",
			"_token_version": tokenVersion,
		},
	}
}

// 达阈值（默认 1）时：版本递增的 401 升级为 SetError 永久停调度 + 告警，
// 不再走 temp_unschedulable 冷却，错误信息提示需手工重授权。
func TestRateLimitService_OAuth401AfterRefresh_EscalatesAtThreshold(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &oauth401AfterRefreshCounterStub{counts: []int64{1}}
	blocker := &runtimeBlockRecorder{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOAuth401AfterRefreshCounter(counter)
	service.SetAccountRuntimeBlocker(blocker)
	account := newOAuth401AnthropicAccount(701, 1737654321000)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "version-bumped 401 at threshold must SetError (permanent)")
	require.Equal(t, 0, repo.tempCalls, "must NOT fall through to temp_unschedulable cooldown")
	require.Contains(t, repo.lastErrorMsg, "refresh_token likely revoked")
	require.Contains(t, repo.lastErrorMsg, "manual re-authorization")
	require.Equal(t, []int64{701}, counter.recordIDs)
	require.Equal(t, []int64{1737654321000}, counter.recordVersions)
	require.Equal(t, []int{oauth401AfterRefreshWindowMinutesDefault}, counter.recordWindows)
	require.Len(t, blocker.accounts, 1, "escalation must notify scheduling-blocked for alerting")
	require.Equal(t, account.ID, blocker.accounts[0].ID)
	require.Equal(t, "auth_error", blocker.reasons[0])
}

// 首次 401（counter 返回 0 = 种 baseline）不升级，回退到既有 temp_unschedulable 冷却。
func TestRateLimitService_OAuth401AfterRefresh_SeedFallsThroughToCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &oauth401AfterRefreshCounterStub{counts: []int64{0}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOAuth401AfterRefreshCounter(counter)
	account := newOAuth401AnthropicAccount(702, 1737654321000)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "seed 401 must not escalate")
	require.Equal(t, 1, repo.tempCalls, "seed 401 falls through to temp_unschedulable cooldown")
	require.Equal(t, []int64{702}, counter.recordIDs)
}

// 缺少 _token_version（极老数据从未刷新过）：无法判断是否换过新 token，
// 不查计数器、直接回退冷却（fail-open，不误升级）。
func TestRateLimitService_OAuth401AfterRefresh_MissingTokenVersionFallsThrough(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &oauth401AfterRefreshCounterStub{counts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOAuth401AfterRefreshCounter(counter)
	account := &Account{
		ID:       703,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt", // 无 _token_version
		},
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.Empty(t, counter.recordIDs, "missing _token_version must not consult the counter")
}

// 计数器返回错误：fail-open 到既有冷却，绝不因计数器故障误判永久禁用。
func TestRateLimitService_OAuth401AfterRefresh_CounterErrorFailsOpen(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &oauth401AfterRefreshCounterStub{err: errors.New("redis down")}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOAuth401AfterRefreshCounter(counter)
	account := newOAuth401AnthropicAccount(704, 1737654321000)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "counter failure must NOT permanently disable")
	require.Equal(t, 1, repo.tempCalls)
}

// 计数器未注入（如未接 Redis 的精简部署）：保持既有 temp_unschedulable 行为不变。
func TestRateLimitService_OAuth401AfterRefresh_NilCounterPreservesCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := newOAuth401AnthropicAccount(705, 1737654321000)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
}

// 阈值可配（=2）：count=1 仍冷却，count=2 才升级；配置的 window 透传给计数器。
func TestRateLimitService_OAuth401AfterRefresh_ThresholdAndWindowConfigurable(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &oauth401AfterRefreshCounterStub{counts: []int64{1, 2}}
	cfg := &config.Config{}
	cfg.RateLimit.OAuth401AfterRefreshDisableThreshold = 2
	cfg.RateLimit.OAuth401AfterRefreshWindowMinutes = 45
	service := NewRateLimitService(repo, nil, cfg, nil, nil)
	service.SetOAuth401AfterRefreshCounter(counter)
	account := newOAuth401AnthropicAccount(706, 1737654321000)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))
	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "count=1 below threshold 2 must cooldown")
	require.Equal(t, 1, repo.tempCalls)

	shouldDisable = service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))
	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "count=2 at threshold must escalate")
	require.Equal(t, 1, repo.tempCalls, "no extra cooldown on escalation")
	require.Equal(t, []int{45, 45}, counter.recordWindows, "configured window minutes must pass through")
}

// 恢复路径 ClearRateLimit 必须重置计数与 baseline，避免良性 baseline 残留累积。
func TestRateLimitService_OAuth401AfterRefresh_ClearRateLimitResets(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &oauth401AfterRefreshCounterStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOAuth401AfterRefreshCounter(counter)

	require.NoError(t, service.ClearRateLimit(context.Background(), 707))
	require.Equal(t, []int64{707}, counter.resetCalls)
}

// same-version 达阈值（默认 1）：token 仍有效却跨冷却周期持续 401（版本未递增），
// 升级为 SetError + 告警，文案区别于 version-bump（提示 grant 在有效期内被吊销）。
func TestRateLimitService_OAuth401SameVersion_EscalatesAtThreshold(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	// version-bump 维度恒 0（无刷新），same-version 维度返回 1。
	counter := &oauth401AfterRefreshCounterStub{sameCounts: []int64{1}}
	blocker := &runtimeBlockRecorder{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOAuth401AfterRefreshCounter(counter)
	service.SetAccountRuntimeBlocker(blocker)
	account := newOAuth401AnthropicAccount(801, 1737654321000)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "same-version at threshold must SetError (permanent)")
	require.Equal(t, 0, repo.tempCalls, "must NOT fall through to temp_unschedulable cooldown")
	require.Contains(t, repo.lastErrorMsg, "still-valid token")
	require.Contains(t, repo.lastErrorMsg, "grant likely revoked upstream")
	require.Contains(t, repo.lastErrorMsg, "manual re-authorization")
	require.Len(t, blocker.accounts, 1, "escalation must notify scheduling-blocked for alerting")
	require.Equal(t, "auth_error", blocker.reasons[0])
	// 默认 cooldown 10min → 派生 debounce = 10*60/2 = 300s，透传给计数器。
	require.Equal(t, []int{300}, counter.recordDebounces)
}

// same-version 阈值可配（=3）：count=2 仍冷却，count=3 才升级。
func TestRateLimitService_OAuth401SameVersion_ThresholdConfigurable(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &oauth401AfterRefreshCounterStub{sameCounts: []int64{2, 3}}
	cfg := &config.Config{}
	cfg.RateLimit.OAuth401SameVersionDisableThreshold = 3
	service := NewRateLimitService(repo, nil, cfg, nil, nil)
	service.SetOAuth401AfterRefreshCounter(counter)
	account := newOAuth401AnthropicAccount(802, 1737654321000)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))
	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "same-version below threshold 3 must not escalate")
	require.Equal(t, 1, repo.tempCalls, "falls through to temp_unschedulable cooldown")

	shouldDisable = service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))
	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "same-version at configured threshold 3 must escalate")
	require.Equal(t, 1, repo.tempCalls, "no extra cooldown on escalation")
	require.Contains(t, repo.lastErrorMsg, "still-valid token")
}

// version-bump 与 same-version 同时达阈值时，version-bump 优先（信号更强，文案不同）。
func TestRateLimitService_OAuth401_VersionBumpTakesPriorityOverSameVersion(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &oauth401AfterRefreshCounterStub{counts: []int64{1}, sameCounts: []int64{5}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOAuth401AfterRefreshCounter(counter)
	account := newOAuth401AnthropicAccount(803, 1737654321000)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Contains(t, repo.lastErrorMsg, "refresh_token likely revoked", "version-bump branch wins")
	require.NotContains(t, repo.lastErrorMsg, "still-valid token")
}
