package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type capacityRetryAfterCall struct {
	accountID int64
	scope     string
	resetAt   time.Time
	reason    string
}

type capacityRetryAfterRepo struct {
	stubOpenAIAccountRepo
	modelCalls       []capacityRetryAfterCall
	overloadCalls    int
	tempUnschedCalls int
}

func (r *capacityRetryAfterRepo) SetModelRateLimit(_ context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	call := capacityRetryAfterCall{accountID: id, scope: scope, resetAt: resetAt}
	if len(reason) > 0 {
		call.reason = reason[0]
	}
	r.modelCalls = append(r.modelCalls, call)
	return nil
}

func (r *capacityRetryAfterRepo) SetOverloaded(context.Context, int64, time.Time) error {
	r.overloadCalls++
	return nil
}

func (r *capacityRetryAfterRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.tempUnschedCalls++
	return nil
}

func capacityRetryAfterService(repo *capacityRetryAfterRepo) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg: &config.Config{},
		rateLimitService: &RateLimitService{
			accountRepo: repo,
			cfg:         &config.Config{},
		},
	}
}

func capacityRetryAfterAccount(enabled bool) *Account {
	credentials := map[string]any{}
	if enabled {
		credentials["temp_unschedulable_enabled"] = true
		credentials["temp_unschedulable_rules"] = []any{map[string]any{
			"error_code":       float64(503),
			"keywords":         []any{"server_is_overloaded"},
			"duration_minutes": float64(2),
		}}
	}
	return &Account{
		ID:          71,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: credentials,
	}
}

func capacityOverloadedPayload(retryAfter string) []byte {
	if retryAfter == "" {
		return []byte(`{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`)
	}
	return []byte(`{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later.","headers":{"retry-after":"` + retryAfter + `"}}}}`)
}

func TestOpenAICapacityRetryAfter_CoolsAccountModelWithoutSleeping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &capacityRetryAfterRepo{}
	svc := capacityRetryAfterService(repo)
	account := capacityRetryAfterAccount(false)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	start := time.Now()
	err := svc.newOpenAIStreamFailoverErrorWithModel(
		c, account, false, "upstream-id", capacityOverloadedPayload("309"),
		"Our servers are currently overloaded. Please try again later.", "gpt-5.4",
	)

	require.Equal(t, http.StatusServiceUnavailable, err.StatusCode)
	require.True(t, err.RequestScopedTransient, "in-request retry stays request-scoped")
	require.True(t, err.RetryableOnSameAccount)
	require.Zero(t, err.SameAccountRetryDelay, "the request must not adopt the 309s Retry-After as its sleep")
	require.Len(t, repo.modelCalls, 1)
	call := repo.modelCalls[0]
	require.Equal(t, account.ID, call.accountID)
	require.Equal(t, "gpt-5.4", call.scope)
	require.Equal(t, openAICapacityRetryAfterModelCooldownReason, call.reason)
	require.WithinDuration(t, start.Add(openAICapacityRetryAfterModelCooldownCap), call.resetAt, 2*time.Second)
	require.Equal(t, 3*time.Minute, openAICapacityRetryAfterModelCooldownCap)
	require.Less(t, call.resetAt.Sub(start), 309*time.Second)
	require.Zero(t, repo.overloadCalls)
	require.Zero(t, repo.tempUnschedCalls)
	require.True(t, account.IsSchedulable(), "the account itself stays schedulable")
	require.Nil(t, account.OverloadUntil)
	require.Nil(t, account.TempUnschedulableUntil)
	require.False(t, account.IsSchedulableForModel("gpt-5.4"))
	require.True(t, account.IsSchedulableForModel("gpt-5.1"), "a sibling model is not cooled")

	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{*account}}
	cooled, cooledErr := svc.SelectAccountForModelWithExclusions(context.Background(), nil, "", "gpt-5.4", nil)
	require.Error(t, cooledErr)
	require.Nil(t, cooled)
	other, otherErr := svc.SelectAccountForModelWithExclusions(context.Background(), nil, "", "gpt-5.1", nil)
	require.NoError(t, otherErr)
	require.NotNil(t, other)
	require.Equal(t, account.ID, other.ID)
}

func TestOpenAICapacityRetryAfter_ShortDelayIsNotPaddedToCap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &capacityRetryAfterRepo{}
	svc := capacityRetryAfterService(repo)
	account := capacityRetryAfterAccount(false)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	start := time.Now()
	_ = svc.newOpenAIStreamFailoverErrorWithModel(
		c, account, false, "upstream-id", capacityOverloadedPayload("30"),
		"Our servers are currently overloaded. Please try again later.", "gpt-5.4",
	)

	require.Len(t, repo.modelCalls, 1)
	require.WithinDuration(t, start.Add(30*time.Second), repo.modelCalls[0].resetAt, 2*time.Second)
	require.Less(t, repo.modelCalls[0].resetAt.Sub(start), openAICapacityRetryAfterModelCooldownCap)
}

func TestOpenAICapacityRetryAfter_MissingOrUnparseableKeepsCurrentBehavior(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name       string
		retryAfter string
	}{
		{name: "missing", retryAfter: ""},
		{name: "unparseable", retryAfter: "later"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &capacityRetryAfterRepo{}
			svc := capacityRetryAfterService(repo)
			account := capacityRetryAfterAccount(false)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

			err := svc.newOpenAIStreamFailoverErrorWithModel(
				c, account, false, "upstream-id", capacityOverloadedPayload(tc.retryAfter),
				"Our servers are currently overloaded. Please try again later.", "gpt-5.4",
			)

			require.Empty(t, repo.modelCalls)
			require.Zero(t, repo.overloadCalls)
			require.Zero(t, repo.tempUnschedCalls)
			require.True(t, err.RequestScopedTransient)
			require.True(t, err.RetryableOnSameAccount)
			require.True(t, account.IsSchedulableForModel("gpt-5.4"))
			require.True(t, account.IsSchedulable())
		})
	}
}

func TestOpenAICapacityRetryAfter_EnabledRuleKeepsItsOwnCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &capacityRetryAfterRepo{}
	svc := capacityRetryAfterService(repo)
	account := capacityRetryAfterAccount(true)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	start := time.Now()
	err := svc.newOpenAIStreamFailoverErrorWithModel(
		c, account, false, "upstream-id", capacityOverloadedPayload("309"),
		"Our servers are currently overloaded. Please try again later.", "gpt-5.4",
	)

	require.False(t, err.RetryableOnSameAccount, "the matched model rule still switches accounts immediately")
	require.False(t, err.RequestScopedTransient)
	require.Len(t, repo.modelCalls, 1, "retry-after must not add a second, longer cooldown")
	require.Equal(t, "gpt-5.4", repo.modelCalls[0].scope)
	require.NotEqual(t, openAICapacityRetryAfterModelCooldownReason, repo.modelCalls[0].reason)
	require.WithinDuration(t, start.Add(2*time.Minute), repo.modelCalls[0].resetAt, 2*time.Second)
	require.Less(t, repo.modelCalls[0].resetAt.Sub(start), openAICapacityRetryAfterModelCooldownCap)
	require.Zero(t, repo.overloadCalls)
	require.Zero(t, repo.tempUnschedCalls)
	require.True(t, account.Schedulable)
}

func TestOpenAICapacityRetryAfter_529DoesNotOverloadWholeAccount(t *testing.T) {
	repo := &capacityRetryAfterRepo{}
	svc := capacityRetryAfterService(repo)
	account := capacityRetryAfterAccount(false)
	body := []byte(`{"error":{"message":"server overloaded","type":"server_error"}}`)
	headers := http.Header{"Retry-After": []string{"309"}}

	start := time.Now()
	disabled := svc.handleOpenAIAccountUpstreamError(context.Background(), account, 529, headers, body, "gpt-5.4")

	require.False(t, disabled)
	require.Zero(t, repo.overloadCalls, "529 Retry-After must not bench the whole account")
	require.Zero(t, repo.tempUnschedCalls)
	require.Len(t, repo.modelCalls, 1)
	require.Equal(t, "gpt-5.4", repo.modelCalls[0].scope)
	require.WithinDuration(t, start.Add(3*time.Minute), repo.modelCalls[0].resetAt, 2*time.Second)
	require.True(t, account.IsSchedulable())
	require.False(t, account.IsSchedulableForModel("gpt-5.4"))
	require.True(t, account.IsSchedulableForModel("gpt-5.1"))
}
