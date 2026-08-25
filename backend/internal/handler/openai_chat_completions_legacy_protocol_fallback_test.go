//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIChatCompletionsCutoverDisabledKeepsNewAPILegacyDispatcher(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var upstreamPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		require.Equal(t, "Bearer dashscope-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-legacy","object":"chat.completion","model":"qwen3.7-max","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()

	groupID := int64(18)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformNewAPI, Status: service.StatusActive}
	account := service.Account{
		ID:          60,
		Name:        "Qwen",
		Platform:    service.PlatformNewAPI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{
			"api_key":  "dashscope-key",
			"base_url": upstream.URL,
			"model_mapping": map[string]any{
				"client-qwen": "qwen3.7-max",
			},
		},
	}
	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{account}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.MaxAccountSwitches = 1
	schedulerSnapshot := service.NewSchedulerSnapshotService(
		&fakeSchedulerCache{accounts: []*service.Account{&account}},
		nil,
		accountRepo,
		&fakeGroupRepo{group: group},
		cfg,
	)
	wrongNativePath := &openAIHTTPPassthroughFailoverUpstream{}
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	defer billingCache.Stop()
	gateway := service.NewOpenAIGatewayService(
		accountRepo, nil, nil, nil, nil, nil, nil, cfg, schedulerSnapshot, nil,
		service.NewBillingService(cfg, nil), nil, billingCache, wrongNativePath,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
	)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	h := NewOpenAIGatewayHandler(
		gateway,
		service.NewConcurrencyService(cache),
		billingCache,
		&service.APIKeyService{},
		nil, nil, nil, nil,
		cfg,
	)
	// Deliberately leave protocolRouter nil: this is the CutoverReady=false path.

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(
		`{"model":"client-qwen","messages":[{"role":"user","content":"hello"}],"stream":false}`,
	))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID: 1806, GroupID: &groupID,
		User:  &service.User{ID: 1706, Status: service.StatusActive},
		Group: group,
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1706, Concurrency: 1})

	h.ChatCompletions(c)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "/compatible-mode/v1/chat/completions", upstreamPath)
	require.Equal(t, "OK", gjson.GetBytes(recorder.Body.Bytes(), "choices.0.message.content").String())
	require.Empty(t, wrongNativePath.calls(), "legacy fallback must not leak the NewAPI credential into the native OpenAI path")
}
