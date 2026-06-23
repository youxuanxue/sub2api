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

type rateLimitAccountRepoStub struct {
	mockAccountRepoForGemini
	setErrorCalls          int
	tempCalls              int
	updateCredentialsCalls int
	clearErrorCalls        int
	setSchedulableCalls    int
	clearTempCalls         int
	lastCredentials        map[string]any
	lastErrorMsg           string
	lastSchedulable        bool
	lastTempReason         string
	accountOnGet           *Account

	// PR #338 (P3): track exact-reset-time writes so tests can assert
	// handle429 / handle529 ran the upstream-precise path before the
	// ladder write got suppressed via skipCooldownWrite.
	setRateLimitedCalls    int
	setOverloadedCalls     int
	lastRateLimitedResetAt time.Time
	lastOverloadedUntil    time.Time

	// PR #338 (P4): seed account state for the 403 second-hit escalation
	// path. When set, GetByID returns an account whose
	// TempUnschedulableReason holds the prior 403 TempUnschedState so
	// wasTempUnschedByStatusCode(reason, 403) returns true.
	tempReasonOnGet string

	// TK G4: track per-(account × scope) model-rate-limit writes so tests can
	// assert that an Anthropic unified-window 429 cools the model class scope
	// instead of the whole account.
	modelRateLimitCalls []rateLimitStubModelCall
	modelRateLimitErr   error

	// TK G4: assert the account-global 5h session window is still recorded on
	// the model-scoped cooldown path (operator usage gauge depends on it).
	updateSessionWindowCalls int
	lastSessionWindowStatus  string
}

type rateLimitStubModelCall struct {
	accountID int64
	scope     string
	resetAt   time.Time
	reason    string
}

func (r *rateLimitAccountRepoStub) SetModelRateLimit(ctx context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	call := rateLimitStubModelCall{accountID: id, scope: scope, resetAt: resetAt}
	if len(reason) > 0 {
		call.reason = reason[0]
	}
	r.modelRateLimitCalls = append(r.modelRateLimitCalls, call)
	return r.modelRateLimitErr
}

func (r *rateLimitAccountRepoStub) UpdateSessionWindow(ctx context.Context, id int64, start, end *time.Time, status string) error {
	r.updateSessionWindowCalls++
	r.lastSessionWindowStatus = status
	return nil
}

func (r *rateLimitAccountRepoStub) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.setErrorCalls++
	r.lastErrorMsg = errorMsg
	return nil
}

func (r *rateLimitAccountRepoStub) ClearError(ctx context.Context, id int64) error {
	r.clearErrorCalls++
	return nil
}

func (r *rateLimitAccountRepoStub) SetSchedulable(ctx context.Context, id int64, schedulable bool) error {
	r.setSchedulableCalls++
	r.lastSchedulable = schedulable
	return nil
}

func (r *rateLimitAccountRepoStub) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.tempCalls++
	r.lastTempReason = reason
	return nil
}

func (r *rateLimitAccountRepoStub) ClearTempUnschedulable(ctx context.Context, id int64) error {
	r.clearTempCalls++
	return nil
}

func (r *rateLimitAccountRepoStub) UpdateCredentials(ctx context.Context, id int64, credentials map[string]any) error {
	r.updateCredentialsCalls++
	r.lastCredentials = cloneCredentials(credentials)
	if r.accountOnGet != nil && r.accountOnGet.ID == id {
		r.accountOnGet.Credentials = cloneCredentials(credentials)
	}
	return nil
}

func (r *rateLimitAccountRepoStub) SetRateLimited(ctx context.Context, id int64, resetAt time.Time) error {
	r.setRateLimitedCalls++
	r.lastRateLimitedResetAt = resetAt
	return nil
}

func (r *rateLimitAccountRepoStub) SetOverloaded(ctx context.Context, id int64, until time.Time) error {
	r.setOverloadedCalls++
	r.lastOverloadedUntil = until
	return nil
}

func (r *rateLimitAccountRepoStub) GetByID(ctx context.Context, id int64) (*Account, error) {
	if r.accountOnGet != nil {
		return r.accountOnGet, nil
	}
	if r.tempReasonOnGet == "" {
		return nil, nil
	}
	return &Account{ID: id, TempUnschedulableReason: r.tempReasonOnGet}, nil
}

type tokenCacheInvalidatorRecorder struct {
	accounts []*Account
	err      error
}

type openAI403CounterCacheStub struct {
	counts        []int64
	incrementIDs  []int64
	windowMinutes []int
	resetCalls    []int64
	err           error
}

func (s *openAI403CounterCacheStub) IncrementOpenAI403Count(_ context.Context, accountID int64, windowMinutes int) (int64, error) {
	s.incrementIDs = append(s.incrementIDs, accountID)
	s.windowMinutes = append(s.windowMinutes, windowMinutes)
	if s.err != nil {
		return 0, s.err
	}
	if len(s.counts) == 0 {
		return 1, nil
	}
	count := s.counts[0]
	s.counts = s.counts[1:]
	return count, nil
}

func (s *openAI403CounterCacheStub) ResetOpenAI403Count(_ context.Context, accountID int64) error {
	s.resetCalls = append(s.resetCalls, accountID)
	return nil
}

type anthropicUpstreamErrorCounterCacheStub struct {
	counts        []int64
	incrementIDs  []int64
	windowMinutes []int
	resetCalls    []int64
	err           error

	// Bodyless-403 terminal counter (separate namespace from the general
	// error counter). bodyless403Counts scripts the returned count in order
	// so a test can drive the threshold; an empty slice returns 1 each call.
	bodyless403Counts       []int64
	bodyless403IncrementIDs []int64
	bodyless403WindowMin    []int
	bodyless403DebounceSec  []int
	bodyless403ResetCalls   []int64

	tierCounts       []int64
	tierIncrementIDs []int64
	tierTTLMinutes   []int
	tierResetCalls   []int64

	// Global "tier >= 1" escalation counter (PR #338 follow-up to PR #337).
	// Increments are recorded so tests can assert that tier escalations
	// emit the ops_alert_evaluator metric signal. Get returns the running
	// total of the escalations slice length so reads stay consistent with
	// writes without a real Redis backend.
	escalationTTLMinutes []int

	// Per-episode escalation slot guard (issue #623). slotResults scripts the
	// AcquireAnthropicCooldownEscalationSlot return values in order; an empty
	// slice means the slot is always free (won=true), preserving the default
	// "escalate on every threshold trip" behaviour for tests that don't model
	// bursts. slotErr forces an acquire error to exercise the best-effort
	// fall-through path.
	slotResults    []bool
	slotErr        error
	slotAcquireIDs []int64
	slotTTLSeconds []int
	slotResetCalls []int64
}

func (s *anthropicUpstreamErrorCounterCacheStub) IncrementAnthropicUpstreamErrorCount(_ context.Context, accountID int64, windowMinutes int) (int64, error) {
	s.incrementIDs = append(s.incrementIDs, accountID)
	s.windowMinutes = append(s.windowMinutes, windowMinutes)
	if s.err != nil {
		return 0, s.err
	}
	if len(s.counts) == 0 {
		return 1, nil
	}
	count := s.counts[0]
	s.counts = s.counts[1:]
	return count, nil
}

func (s *anthropicUpstreamErrorCounterCacheStub) ResetAnthropicUpstreamErrorCount(_ context.Context, accountID int64) error {
	s.resetCalls = append(s.resetCalls, accountID)
	return nil
}

func (s *anthropicUpstreamErrorCounterCacheStub) IncrementAnthropicBodyless403Count(_ context.Context, accountID int64, windowMinutes, debounceSeconds int) (int64, error) {
	s.bodyless403IncrementIDs = append(s.bodyless403IncrementIDs, accountID)
	s.bodyless403WindowMin = append(s.bodyless403WindowMin, windowMinutes)
	s.bodyless403DebounceSec = append(s.bodyless403DebounceSec, debounceSeconds)
	if s.err != nil {
		return 0, s.err
	}
	if len(s.bodyless403Counts) == 0 {
		return 1, nil
	}
	count := s.bodyless403Counts[0]
	s.bodyless403Counts = s.bodyless403Counts[1:]
	return count, nil
}

func (s *anthropicUpstreamErrorCounterCacheStub) ResetAnthropicBodyless403Count(_ context.Context, accountID int64) error {
	s.bodyless403ResetCalls = append(s.bodyless403ResetCalls, accountID)
	return nil
}

func (s *anthropicUpstreamErrorCounterCacheStub) IncrementAnthropicCooldownTier(_ context.Context, accountID int64, ttlMinutes int) (int64, error) {
	s.tierIncrementIDs = append(s.tierIncrementIDs, accountID)
	s.tierTTLMinutes = append(s.tierTTLMinutes, ttlMinutes)
	if len(s.tierCounts) == 0 {
		return 1, nil
	}
	count := s.tierCounts[0]
	s.tierCounts = s.tierCounts[1:]
	return count, nil
}

func (s *anthropicUpstreamErrorCounterCacheStub) ResetAnthropicCooldownTier(_ context.Context, accountID int64) error {
	s.tierResetCalls = append(s.tierResetCalls, accountID)
	return nil
}

func (s *anthropicUpstreamErrorCounterCacheStub) IncrementAnthropicCooldownTierEscalations(_ context.Context, ttlMinutes int) (int64, error) {
	s.escalationTTLMinutes = append(s.escalationTTLMinutes, ttlMinutes)
	return int64(len(s.escalationTTLMinutes)), nil
}

func (s *anthropicUpstreamErrorCounterCacheStub) GetAnthropicCooldownTierEscalations(_ context.Context) (int64, error) {
	return int64(len(s.escalationTTLMinutes)), nil
}

func (s *anthropicUpstreamErrorCounterCacheStub) AcquireAnthropicCooldownEscalationSlot(_ context.Context, accountID int64, _ int) (bool, error) {
	s.slotAcquireIDs = append(s.slotAcquireIDs, accountID)
	if s.slotErr != nil {
		return false, s.slotErr
	}
	if len(s.slotResults) == 0 {
		return true, nil
	}
	won := s.slotResults[0]
	s.slotResults = s.slotResults[1:]
	return won, nil
}

func (s *anthropicUpstreamErrorCounterCacheStub) SetAnthropicCooldownEscalationSlotTTL(_ context.Context, _ int64, ttlSeconds int) error {
	s.slotTTLSeconds = append(s.slotTTLSeconds, ttlSeconds)
	return nil
}

func (s *anthropicUpstreamErrorCounterCacheStub) ResetAnthropicCooldownEscalationSlot(_ context.Context, accountID int64) error {
	s.slotResetCalls = append(s.slotResetCalls, accountID)
	return nil
}

func (r *tokenCacheInvalidatorRecorder) InvalidateToken(ctx context.Context, account *Account) error {
	r.accounts = append(r.accounts, account)
	return r.err
}

func TestRateLimitService_HandleUpstreamError_OAuth401SetsTempUnschedulable(t *testing.T) {
	t.Run("gemini", func(t *testing.T) {
		repo := &rateLimitAccountRepoStub{}
		invalidator := &tokenCacheInvalidatorRecorder{}
		service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		service.SetTokenCacheInvalidator(invalidator)
		account := &Account{
			ID:       100,
			Platform: PlatformGemini,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"refresh_token":              "rt-100",
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{
					map[string]any{
						"error_code":       401,
						"keywords":         []any{"unauthorized"},
						"duration_minutes": 30,
						"description":      "custom rule",
					},
				},
			},
		}

		shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

		require.True(t, shouldDisable)
		require.Equal(t, 0, repo.setErrorCalls)
		require.Equal(t, 1, repo.tempCalls)
		require.Len(t, invalidator.accounts, 1)
	})

	t.Run("antigravity_401_uses_SetError", func(t *testing.T) {
		// Antigravity 401 由 applyErrorPolicy 的 temp_unschedulable_rules 控制，
		// HandleUpstreamError 中走 SetError 路径。
		repo := &rateLimitAccountRepoStub{}
		invalidator := &tokenCacheInvalidatorRecorder{}
		service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		service.SetTokenCacheInvalidator(invalidator)
		account := &Account{
			ID:       100,
			Platform: PlatformAntigravity,
			Type:     AccountTypeOAuth,
		}

		shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

		require.True(t, shouldDisable)
		require.Equal(t, 1, repo.setErrorCalls)
		require.Equal(t, 0, repo.tempCalls)
		require.Empty(t, invalidator.accounts)
	})
}

// TestRateLimitService_HandleUpstreamError_OAuth401InvalidatorError
// OpenAI OAuth 401 缓存失效出错时仍走 temp_unschedulable。
// 注意：401 handler 不再回写 credentials(避免请求开始时的快照整列覆盖 DB
// 把另一个 worker 刚刷新出来的新 refresh_token 回滚为旧值),
// 因此 updateCredentialsCalls 应当为 0。
func TestRateLimitService_HandleUpstreamError_OAuth401InvalidatorError(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	invalidator := &tokenCacheInvalidatorRecorder{err: errors.New("boom")}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetTokenCacheInvalidator(invalidator)
	account := &Account{
		ID:       101,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt-101",
		},
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, 0, repo.updateCredentialsCalls)
	require.Len(t, invalidator.accounts, 1)
}

func TestRateLimitService_HandleUpstreamError_NonOAuth401(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	invalidator := &tokenCacheInvalidatorRecorder{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetTokenCacheInvalidator(invalidator)
	account := &Account{
		ID:       102,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Empty(t, invalidator.accounts)
}

// TestRateLimitService_HandleUpstreamError_OAuth401DoesNotOverwriteCredentials
// 回归测试:确保 401 handler 不再使用请求开始时的 account 快照写回 credentials。
// 原实现会通过 persistAccountCredentials → UpdateCredentials → SetCredentials
// 整列覆盖 credentials JSONB,在另一个 worker 刚刷新完 refresh_token 的窄窗口内
// 会把新 refresh_token 回滚为快照中的旧值,导致下一周期拿 invalid_grant 被错误 disable。
func TestRateLimitService_HandleUpstreamError_OAuth401DoesNotOverwriteCredentials(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       103,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "token",
			"refresh_token": "rt-103",
		},
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.updateCredentialsCalls, "401 handler must not write credentials back from the request-start snapshot")
	require.Equal(t, 1, repo.tempCalls, "401 handler should still set temp-unschedulable cooldown")
	require.Nil(t, repo.lastCredentials, "no credentials should have been persisted")
}

// 缺少 refresh_token 的 OAuth 账号 401 应直接 SetError 永久禁用，
// 不再走 10 分钟冷却（冷却期内无人能刷新它，结束后还会被选中再 502 一次）。
func TestRateLimitService_HandleUpstreamError_OAuth401NoRefreshTokenSetsError(t *testing.T) {
	t.Run("openai_no_refresh_token", func(t *testing.T) {
		repo := &rateLimitAccountRepoStub{}
		invalidator := &tokenCacheInvalidatorRecorder{}
		service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		service.SetTokenCacheInvalidator(invalidator)
		account := &Account{
			ID:       2881,
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"access_token": "expired-at",
				// no refresh_token
			},
		}

		shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

		require.True(t, shouldDisable)
		require.Equal(t, 1, repo.setErrorCalls, "AT-only OAuth 401 must SetError")
		require.Equal(t, 0, repo.tempCalls, "AT-only OAuth 401 must NOT temp-unschedule")
		require.Equal(t, 0, repo.updateCredentialsCalls, "no point forcing expires_at when refresh is impossible")
		require.Contains(t, repo.lastErrorMsg, "refresh_token missing")
		require.Len(t, invalidator.accounts, 1, "cache should still be invalidated")
	})

	t.Run("openai_blank_refresh_token_treated_as_missing", func(t *testing.T) {
		repo := &rateLimitAccountRepoStub{}
		service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		account := &Account{
			ID:       2882,
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"access_token":  "expired-at",
				"refresh_token": "   ",
			},
		}

		shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

		require.True(t, shouldDisable)
		require.Equal(t, 1, repo.setErrorCalls)
		require.Equal(t, 0, repo.tempCalls)
	})
}
