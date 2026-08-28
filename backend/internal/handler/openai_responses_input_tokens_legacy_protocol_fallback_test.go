//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesInputTokensOpenAICodexOAuthUsesRootRouteForLocalEstimate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(19)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformOpenAI, Status: service.StatusActive}
	account := service.Account{
		ID:          73,
		Name:        "Codex OAuth",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
		Extra:       map[string]any{},
	}
	attachHandlerTestProtocolCapability(t, &account, protocolrouter.ProtocolResponses)
	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{account}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	schedulerSnapshot := service.NewSchedulerSnapshotService(
		&fakeSchedulerCache{accounts: []*service.Account{&account}},
		nil,
		accountRepo,
		&fakeGroupRepo{group: group},
		cfg,
	)
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	gateway := service.NewOpenAIGatewayService(
		accountRepo, nil, nil, nil, nil, nil, nil, cfg, schedulerSnapshot, nil,
		service.NewBillingService(cfg, nil), nil, billingCache, nil,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
	)
	h := NewOpenAIGatewayHandler(
		gateway,
		service.NewConcurrencyService(nil),
		billingCache,
		&service.APIKeyService{},
		nil, nil, nil, nil,
		cfg,
	)
	h.SetProtocolRouter(service.NewProtocolRouter())

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses/input_tokens", strings.NewReader(
		`{"model":"gpt-5.6-sol","input":"hello"}`,
	))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID: 1818, GroupID: &groupID,
		User:  &service.User{ID: 1718, Status: service.StatusActive},
		Group: group,
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1718, Concurrency: 1})

	h.ResponsesInputTokens(c)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Greater(t, gjson.GetBytes(recorder.Body.Bytes(), "input_tokens").Int(), int64(0), recorder.Body.String())
}

func TestResponsesInputTokensCutoverDisabledKeepsLegacyExecutor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(18)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformNewAPI, Status: service.StatusActive}
	account := service.Account{
		ID:          72,
		Name:        "Qwen",
		Platform:    service.PlatformNewAPI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{
			"api_key":  "dashscope-key",
			"base_url": "https://dashscope.aliyuncs.com",
			"model_mapping": map[string]any{
				"client-qwen": "qwen3.7-max",
			},
		},
		Extra: map[string]any{
			openai_compat.ExtraKeyResponsesSupported: true,
		},
	}
	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{account}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	schedulerSnapshot := service.NewSchedulerSnapshotService(
		&fakeSchedulerCache{accounts: []*service.Account{&account}},
		nil,
		accountRepo,
		&fakeGroupRepo{group: group},
		cfg,
	)
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	gateway := service.NewOpenAIGatewayService(
		accountRepo, nil, nil, nil, nil, nil, nil, cfg, schedulerSnapshot, nil,
		service.NewBillingService(cfg, nil), nil, billingCache, nil,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
	)
	h := NewOpenAIGatewayHandler(
		gateway,
		service.NewConcurrencyService(nil),
		billingCache,
		&service.APIKeyService{},
		nil, nil, nil, nil,
		cfg,
	)
	// Deliberately leave protocolRouter nil: this is the CutoverReady=false path.

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses/input_tokens", strings.NewReader(
		`{"model":"client-qwen","input":"hello"}`,
	))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID: 1817, GroupID: &groupID,
		User:  &service.User{ID: 1717, Status: service.StatusActive},
		Group: group,
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1717, Concurrency: 1})

	h.ResponsesInputTokens(c)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Greater(t, gjson.GetBytes(recorder.Body.Bytes(), "input_tokens").Int(), int64(0), recorder.Body.String())
}
