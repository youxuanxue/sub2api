//go:build unit

package service

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func newOpenAIGatewayWithRateLimit(repo *rateLimitAccountRepoStub) (*OpenAIGatewayService, *RateLimitService) {
	gateway := &OpenAIGatewayService{}
	rls := newG4RateLimitService(repo)
	rls.settingService = settingServiceWithMaxCooldown("")
	rls.SetAccountRuntimeBlocker(gateway)
	gateway.rateLimitService = rls
	return gateway, rls
}

func codex7dExhaustedHeaders(reset7dSeconds int) http.Header {
	h := codexGeneralWindowHeaders(1, 100)
	h.Set("x-codex-secondary-reset-after-seconds", strconv.Itoa(reset7dSeconds))
	return h
}

func TestOpenAI429FastPath_MarksOAuthAccountCoolingDown(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKeyAccount := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	shouldDisable := svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, nil)
	apiKeyShouldDisable := svc.handleOpenAIAccountUpstreamError(context.Background(), apiKeyAccount, http.StatusTooManyRequests, http.Header{}, nil)

	require.False(t, shouldDisable)
	require.False(t, apiKeyShouldDisable)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(apiKeyAccount))
}

// TestOpenAI429FastPath_SkipsSparkShadow 外审第8轮 P1:spark 影子被选中后若 /responses 返回 429,
// 不得按 global x-codex-* 信号写内存运行时熔断(否则 spark 被冷却到 global reset、单影子场景无可用账号)。
func TestOpenAI429FastPath_SkipsSparkShadow(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	gateway, _ := newOpenAIGatewayWithRateLimit(repo)
	parentID := int64(800)
	shadow := &Account{
		ID:              801,
		Platform:        PlatformOpenAI,
		Type:            AccountTypeOAuth,
		ParentAccountID: &parentID,
		QuotaDimension:  QuotaDimensionSpark,
	}
	normal := &Account{ID: 802, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-reset-after-seconds", "18000")
	headers.Set("x-codex-primary-window-minutes", "300")

	gateway.handleOpenAIAccountUpstreamError(context.Background(), shadow, http.StatusTooManyRequests, headers, nil, "gpt-5.4")
	gateway.handleOpenAIAccountUpstreamError(context.Background(), normal, http.StatusTooManyRequests, headers, nil, "gpt-5.4")

	require.False(t, gateway.isOpenAIAccountRuntimeBlocked(shadow), "spark shadow must not be runtime-blocked by /responses global 429")
	require.True(t, gateway.isOpenAIAccountRuntimeBlocked(normal), "normal OpenAI OAuth account should still be runtime-blocked")
}

func TestOpenAIRuntimeBlock_AppliesToOpenAIAPIKeyWhenRateLimitServiceStopsScheduling(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 44, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	svc.BlockAccountScheduling(account, time.Time{}, "custom_error_code")

	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIRuntimeBlock_DoesNotApplyToOtherPlatforms(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 45, Platform: PlatformGemini, Type: AccountTypeOAuth}

	svc.BlockAccountScheduling(account, time.Time{}, "custom_error_code")

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIRuntimeBlocker_IgnoresNonOpenAIFromRateLimitService(t *testing.T) {
	gateway := &OpenAIGatewayService{}
	repo := &rateLimitAccountRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rateLimitService.SetAccountRuntimeBlocker(gateway)
	account := &Account{ID: 45, Platform: PlatformGemini, Type: AccountTypeOAuth}

	shouldDisable := rateLimitService.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte("forbidden"))

	require.True(t, shouldDisable)
	require.False(t, gateway.isOpenAIAccountRuntimeBlocked(account))
}

// 自 #4547（issue 4527 第4点）起，临时不可调度规则命中已知模型时按模型隔离：
// 只封 (账号, 模型) 对，不再账号级一刀切；未知模型仍走账号级兜底
// （见 TestOpenAITempUnschedulable_UnknownModelKeepsAccountRuntimeBlock）。
// 池模式规则仍然生效（issue 4470）：停止同账号重试并对命中模型设临时封锁。
func TestOpenAIPoolModeTempRule_StopsSameAccountRetryAndIsolatesBlockToModel(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	gateway := &OpenAIGatewayService{
		cfg:              &config.Config{},
		rateLimitService: rateLimitService,
	}
	rateLimitService.SetAccountRuntimeBlocker(gateway)
	account := &Account{
		ID:          46,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"pool_mode":                    true,
			"pool_mode_retry_status_codes": []any{float64(http.StatusServiceUnavailable)},
			"temp_unschedulable_enabled":   true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusServiceUnavailable),
					"keywords":         []any{"unavailable"},
					"duration_minutes": float64(30),
				},
			},
		},
	}
	body := []byte(`{"error":{"message":"Service temporarily unavailable"}}`)
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{},
	}

	failoverErr := gateway.failoverOpenAIUpstreamHTTPError(
		context.Background(),
		nil,
		account,
		resp,
		body,
		"Service temporarily unavailable",
		"gpt-5.4",
	)

	require.NotNil(t, failoverErr)
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Zero(t, repo.tempCalls)
	require.Equal(t, 0, repo.setErrCalls)
	require.Equal(t, StatusActive, account.Status)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.modelRateLimitCalls[0].scope)
	require.False(t, gateway.isOpenAIAccountRuntimeBlocked(account))
	require.False(t, gateway.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.5"))
}

func TestOpenAIPoolModeRetryable5xx_DoesNotCreateModelTransientBlock(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	gateway := &OpenAIGatewayService{rateLimitService: rateLimitService}
	account := &Account{
		ID:       47,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode":                    true,
			"pool_mode_retry_status_codes": []any{float64(524)},
		},
	}

	for i := 0; i < 2; i++ {
		shouldDisable := gateway.handleOpenAIAccountUpstreamError(
			context.Background(),
			account,
			524,
			http.Header{},
			[]byte(`{"error":{"message":"upstream timeout"}}`),
			"gpt-5.4",
		)
		require.False(t, shouldDisable)
	}

	require.False(t, gateway.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.4"))
}

func TestOpenAIPoolModeNonRetryable5xx_StillCreatesModelTransientBlock(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	gateway := &OpenAIGatewayService{rateLimitService: rateLimitService}
	account := &Account{
		ID:       48,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode":                    true,
			"pool_mode_retry_status_codes": []any{float64(http.StatusGatewayTimeout)},
		},
	}

	for i := 0; i < 2; i++ {
		shouldDisable := gateway.handleOpenAIAccountUpstreamError(
			context.Background(),
			account,
			http.StatusServiceUnavailable,
			http.Header{},
			[]byte(`{"error":{"message":"upstream unavailable"}}`),
			"gpt-5.4",
		)
		require.False(t, shouldDisable)
	}

	require.True(t, gateway.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.4"))
}

func TestOpenAINonPoolAPIKey5xx_StillCreatesModelTransientBlock(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	gateway := &OpenAIGatewayService{rateLimitService: rateLimitService}
	account := &Account{
		ID:       49,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
	}

	for i := 0; i < 2; i++ {
		shouldDisable := gateway.handleOpenAIAccountUpstreamError(
			context.Background(),
			account,
			http.StatusGatewayTimeout,
			http.Header{},
			[]byte(`{"error":{"message":"upstream timeout"}}`),
			"gpt-5.4",
		)
		require.False(t, shouldDisable)
	}

	require.True(t, gateway.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.4"))
}

func TestOpenAIModelNotFound_DoesNotRuntimeBlockWholeAccount(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &OpenAIGatewayService{
		rateLimitService: &RateLimitService{accountRepo: repo},
	}
	account := openAIModelNotFoundTempAccount()

	shouldDisable := svc.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusNotFound,
		http.Header{},
		[]byte(`{"error":{"code":"model_not_found","message":"model not found"}}`),
		"gpt-5.4",
	)

	require.True(t, shouldDisable)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
}

func TestOpenAIRuntimeBlock_DoesNotShortenExistingBlock(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 46, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	longUntil := time.Now().Add(10 * time.Minute)

	svc.BlockAccountScheduling(account, longUntil, "oauth_401")
	svc.BlockAccountScheduling(account, time.Time{}, "upstream_disable")

	value, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, ok)
	actualEntry, ok := loadOpenAIAccountRuntimeBlockEntry(value)
	require.True(t, ok)
	require.WithinDuration(t, longUntil, actualEntry.Until, time.Second)
}

func TestOpenAIRuntimeBlock_ClearAccountSchedulingBlock(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 47, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	svc.BlockAccountScheduling(account, time.Now().Add(time.Minute), "429")
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))

	svc.ClearAccountSchedulingBlock(account.ID)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestShouldStopOpenAIOAuth429Failover_OnlyDuringStorm(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKeyAccount := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	var state OpenAIOAuth429FailoverState

	require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusTooManyRequests, 1, &state))

	for i := 0; i < openAIOAuth429StormThreshold; i++ {
		svc.recordOpenAIOAuth429()
	}

	require.True(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusTooManyRequests, 1, &state))
	require.False(t, svc.ShouldStopOpenAIOAuth429Failover(apiKeyAccount, http.StatusTooManyRequests, 1, &state))
	require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusInternalServerError, 1, &state))
	require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusTooManyRequests, 0, &state))
}

func TestShouldStopOpenAIOAuth429Failover_TracksOneGrokFollowupAttempt(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 44, Platform: PlatformGrok, Type: AccountTypeOAuth}
	apiKeyAccount := &Account{ID: 45, Platform: PlatformGrok, Type: AccountTypeAPIKey}

	t.Run("429 then 500 stops after one followup", func(t *testing.T) {
		var state OpenAIOAuth429FailoverState
		require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusTooManyRequests, 1, &state))
		require.True(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusInternalServerError, 2, &state))
	})

	t.Run("500 then 429 still allows one followup", func(t *testing.T) {
		var state OpenAIOAuth429FailoverState
		require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusInternalServerError, 1, &state))
		require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusTooManyRequests, 2, &state))
		require.True(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusBadGateway, 3, &state))
	})

	t.Run("OAuth 429 then API-key failure consumes the same followup", func(t *testing.T) {
		var state OpenAIOAuth429FailoverState
		require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusTooManyRequests, 1, &state))
		require.True(t, svc.ShouldStopOpenAIOAuth429Failover(apiKeyAccount, http.StatusInternalServerError, 2, &state))
	})

	var state OpenAIOAuth429FailoverState
	require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusTooManyRequests, 0, &state))
	require.False(t, svc.ShouldStopOpenAIOAuth429Failover(apiKeyAccount, http.StatusTooManyRequests, 2, &state))
}

// Prod incident 2026-07-06 (GPT-pro1): spark usage_limit_reached 429 with a healthy
// account-wide codex window must model-scope — not whole-account runtime-block or
// SetRateLimited — so gpt-5.4/5.5 keep scheduling on the same OAuth account.
func TestHandleOpenAIAccountUpstreamError_Spark429HealthyWindow_ModelScopedOnly(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc, _ := newOpenAIGatewayWithRateLimit(repo)
	account := newOpenAICodexAccount(9, AccountTypeOAuth)
	body := []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"pro","resets_at":1783336071,"resets_in_seconds":14903}}`)

	shouldDisable := svc.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		codexGeneralWindowHeaders(4, 1),
		body,
		codexSparkModel,
	)

	require.False(t, shouldDisable)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "spark sub-limit must not whole-account runtime-block")
	require.Len(t, repo.modelRateLimitCalls, 1, "spark cooldown must be model-scoped")
	require.Equal(t, codexSparkModel, repo.modelRateLimitCalls[0].scope)
	require.Zero(t, repo.setRateLimitedCalls, "healthy general window must not SetRateLimited whole account")
}

func TestHandleOpenAIAccountUpstreamError_Spark429GeneralWindowExhausted_WholeAccount(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc, _ := newOpenAIGatewayWithRateLimit(repo)
	account := newOpenAICodexAccount(9, AccountTypeOAuth)
	body := codexUsageLimitBody
	headers := codexGeneralWindowHeaders(100, 1)
	headers.Set("x-codex-primary-reset-after-seconds", "7620")

	shouldDisable := svc.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		headers,
		body,
		codexSparkModel,
	)

	require.False(t, shouldDisable)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account), "account-wide window exhaustion must runtime-block")
	require.Empty(t, repo.modelRateLimitCalls)
	require.Equal(t, 1, repo.setRateLimitedCalls)
}

func TestPersistOpenAIWSRateLimitSignal_Spark429HealthyWindow_ModelScopedOnly(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc, _ := newOpenAIGatewayWithRateLimit(repo)
	account := newOpenAICodexAccount(9, AccountTypeOAuth)
	body := []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"pro","resets_at":1783336071,"resets_in_seconds":14903}}`)

	svc.persistOpenAIWSRateLimitSignal(
		context.Background(),
		account,
		codexGeneralWindowHeaders(4, 1),
		body,
		"rate_limit_exceeded",
		"usage_limit_reached",
		"The usage limit has been reached",
		codexSparkModel,
	)

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "WS spark sub-limit must not whole-account runtime-block")
	require.Len(t, repo.modelRateLimitCalls, 1, "WS spark cooldown must be model-scoped")
	require.Equal(t, codexSparkModel, repo.modelRateLimitCalls[0].scope)
	require.Zero(t, repo.setRateLimitedCalls, "healthy general window must not SetRateLimited whole account")
}

// Prod incident 2026-07-28 (GPT-pro3/4): unclamped 7d header reset written by the
// fast-path outlived the clamped DB cooldown. Runtime blocks must follow the same
// notifyAccountSchedulingBlocked funnel as SetRateLimited.
func TestHandleOpenAIAccountUpstreamError_7d429_RuntimeBlockMatchesClampedDB(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	gateway, _ := newOpenAIGatewayWithRateLimit(repo)
	account := newOpenAICodexAccount(73, AccountTypeOAuth)
	headers := codex7dExhaustedHeaders(562950)

	before := time.Now()
	shouldDisable := gateway.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		headers,
		[]byte(`{"error":{"type":"usage_limit_reached","message":"limit reached"}}`),
		"gpt-5.4",
	)

	require.False(t, shouldDisable)
	require.Equal(t, 1, repo.setRateLimitedCalls)
	require.WithinDuration(t, time.Now().Add(5*time.Hour), repo.lastRateLimitedResetAt, 3*time.Second)

	require.True(t, gateway.isOpenAIAccountRuntimeBlocked(account))
	value, ok := gateway.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, ok)
	entry, ok := loadOpenAIAccountRuntimeBlockEntry(value)
	require.True(t, ok)
	require.Equal(t, "429", entry.Reason)
	require.WithinDuration(t, repo.lastRateLimitedResetAt, entry.Until, time.Second)
	require.Less(t, entry.Until.Sub(before), 6*time.Hour)
}

func TestReconcileOpenAIAccountRuntimeBlockWithDB_ClearsStale429Drift(t *testing.T) {
	svc := &OpenAIGatewayService{}
	pastReset := time.Now().Add(-time.Hour)
	account := &Account{
		ID:               73,
		Platform:         PlatformOpenAI,
		Type:             AccountTypeOAuth,
		RateLimitResetAt: &pastReset,
	}
	driftUntil := time.Now().Add(7 * 24 * time.Hour)
	svc.openaiAccountRuntimeBlockUntil.Store(account.ID, openAIAccountRuntimeBlockEntry{
		Until:  driftUntil,
		Reason: "429",
	})

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	_, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.False(t, ok)
}

func TestReconcileOpenAIAccountRuntimeBlockWithDB_PreservesShortOAuth401Block(t *testing.T) {
	svc := &OpenAIGatewayService{}
	pastReset := time.Now().Add(-time.Hour)
	account := &Account{
		ID:               73,
		Platform:         PlatformOpenAI,
		Type:             AccountTypeOAuth,
		RateLimitResetAt: &pastReset,
	}
	oauthUntil := time.Now().Add(10 * time.Minute)
	svc.openaiAccountRuntimeBlockUntil.Store(account.ID, openAIAccountRuntimeBlockEntry{
		Until:  oauthUntil,
		Reason: "oauth_401",
	})

	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}
