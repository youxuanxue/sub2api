//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// stubAntigravityValidationCounter 让测试直接指定阶梯轮数，避免依赖 Redis。
type stubAntigravityValidationCounter struct {
	count          int64
	incErr         error
	incrementCalls []int64
	resetCalls     []int64
	lastWindow     int
}

func (c *stubAntigravityValidationCounter) IncrementAntigravityValidationCount(
	_ context.Context, accountID int64, windowMinutes int,
) (int64, error) {
	c.incrementCalls = append(c.incrementCalls, accountID)
	c.lastWindow = windowMinutes
	if c.incErr != nil {
		return 0, c.incErr
	}
	return c.count, nil
}

func (c *stubAntigravityValidationCounter) ResetAntigravityValidationCount(
	_ context.Context, accountID int64,
) error {
	c.resetCalls = append(c.resetCalls, accountID)
	return nil
}

func antigravityValidationBody() []byte {
	return []byte(`{"error":{"status":"PERMISSION_DENIED","message":"VALIDATION_REQUIRED: Verify your account to continue.","details":[{"metadata":{"validation_url":"https://accounts.google.com/verify"}}]}}`)
}

func newAntigravityValidationService(
	repo *rateLimitAccountRepoStub, counter AntigravityValidationCounterCache,
) *RateLimitService {
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAntigravityValidationCounterCache(counter)
	return service
}

func antigravityOAuthAccount(id int64) *Account {
	return &Account{ID: id, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
}

func TestAntigravityValidation403_第1轮临时停调30分钟(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{count: 1}
	service := newAntigravityValidationService(repo, counter)

	started := time.Now()
	shouldDisable := service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(801),
		http.StatusForbidden, http.Header{}, antigravityValidationBody(),
	)

	require.True(t, shouldDisable, "当前请求必须 failover")
	require.Equal(t, 0, repo.setErrorCalls, "第 1 轮不得永久禁用可恢复的验证挑战")
	require.Equal(t, 1, repo.tempCalls)
	require.Contains(t, repo.lastTempReason, "Validation required (403)")
	require.Contains(t, repo.lastTempReason, "validation_url: https://accounts.google.com/verify")
	require.Contains(t, repo.lastTempReason, "(1/3)")
	require.WithinDuration(t, started.Add(30*time.Minute), repo.lastTempUntil, 5*time.Second)
	require.Equal(t, []int64{801}, counter.incrementCalls)
	require.Equal(t, antigravityValidationCounterWindowMinutes, counter.lastWindow)
}

func TestAntigravityValidation403_第2轮升级到2小时(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{count: 2}
	service := newAntigravityValidationService(repo, counter)

	started := time.Now()
	shouldDisable := service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(802),
		http.StatusForbidden, http.Header{}, antigravityValidationBody(),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "第 2 轮仍是临时停调")
	require.Equal(t, 1, repo.tempCalls)
	require.Contains(t, repo.lastTempReason, "(2/3)")
	require.WithinDuration(t, started.Add(2*time.Hour), repo.lastTempUntil, 5*time.Second)
}

func TestAntigravityValidation403_第3轮永久禁用(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{count: 3}
	service := newAntigravityValidationService(repo, counter)

	shouldDisable := service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(803),
		http.StatusForbidden, http.Header{}, antigravityValidationBody(),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "第 3 轮转人工处理，停止无限重放")
	require.Equal(t, 0, repo.tempCalls, "永久禁用时不再写临时停调")
	require.Contains(t, repo.lastErrorMsg, "consecutive_validation_403=3/3")
	require.Contains(t, repo.lastErrorMsg, "Validation required (403)")
}

func TestAntigravityValidation403_计数器缺失时退回单档冷却(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	// 不接线计数器：修复前的行为必须原样保留，绝不因缺少可选依赖而永久禁用。
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	started := time.Now()
	shouldDisable := service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(804),
		http.StatusForbidden, http.Header{}, antigravityValidationBody(),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.WithinDuration(t, started.Add(30*time.Minute), repo.lastTempUntil, 5*time.Second)
	require.NotContains(t, repo.lastTempReason, "/3)", "无计数器时不应声称阶梯轮数")
}

func TestAntigravityValidation403_计数失败时退回单档冷却(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{incErr: errors.New("redis down")}
	service := newAntigravityValidationService(repo, counter)

	started := time.Now()
	shouldDisable := service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(805),
		http.StatusForbidden, http.Header{}, antigravityValidationBody(),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "计数失败不得把可恢复挑战升级成永久禁用")
	require.Equal(t, 1, repo.tempCalls)
	require.WithinDuration(t, started.Add(30*time.Minute), repo.lastTempUntil, 5*time.Second)
}

func TestAntigravityValidation403_持久化失败时failClosed(t *testing.T) {
	repo := &rateLimitAccountRepoStub{tempErr: errors.New("db down")}
	counter := &stubAntigravityValidationCounter{count: 1}
	service := newAntigravityValidationService(repo, counter)

	shouldDisable := service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(806),
		http.StatusForbidden, http.Header{}, antigravityValidationBody(),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, 1, repo.setErrorCalls, "冷却写不进去时必须 fail closed，避免紧循环重试")
}

func TestAntigravityValidation403_违规403仍然永久禁用且不计入验证阶梯(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{count: 1}
	service := newAntigravityValidationService(repo, counter)

	shouldDisable := service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(807),
		http.StatusForbidden, http.Header{},
		[]byte(`{"error":{"message":"Terms of service violation"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Contains(t, repo.lastErrorMsg, "Account violation (403)")
	require.Empty(t, counter.incrementCalls, "违规 403 不得消耗验证阶梯预算")
}

func TestResetAntigravityValidationCounter_清零轮数(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{count: 1}
	service := newAntigravityValidationService(repo, counter)

	service.ResetAntigravityValidationCounter(context.Background(), 808)
	require.Equal(t, []int64{808}, counter.resetCalls)

	// 无效 accountID 与未接线计数器都不应 panic 或产生写入。
	service.ResetAntigravityValidationCounter(context.Background(), 0)
	require.Equal(t, []int64{808}, counter.resetCalls)

	bare := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	bare.ResetAntigravityValidationCounter(context.Background(), 809)
}
