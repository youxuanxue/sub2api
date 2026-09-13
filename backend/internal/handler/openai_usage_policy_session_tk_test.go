package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type usagePolicySessionUpstream struct {
	service.HTTPUpstream
	calls int
}

func (u *usagePolicySessionUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_prompt","message":"Invalid prompt: violating our usage policy"}}`)),
	}, nil
}

func TestOpenAIUsagePolicyHTTPSessionIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"responses", "chat/completions", "messages"} {
		for _, sessionSignal := range []string{"prompt_cache_key", "transcript"} {
			t.Run(endpoint+"/"+sessionSignal, func(t *testing.T) {
				groupID := int64(4203)
				account := service.Account{
					ID: 9910, Name: "policy-test", Platform: service.PlatformOpenAI,
					Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
					Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.example.test"},
					Extra:       map[string]any{"openai_passthrough": true},
				}
				attachHandlerTestProtocolCapability(t, &account, protocolrouter.ProtocolResponses)
				cfg := &config.Config{RunMode: config.RunModeSimple}
				cfg.Default.RateMultiplier = 1
				cfg.Gateway.MaxAccountSwitches = 1
				upstream := &usagePolicySessionUpstream{}
				cache := testutil.NewRedisGatewayCache(t)
				settings := service.NewSettingService(&contentModerationHandlerSettingRepo{values: map[string]string{
					service.SettingKeyCyberSessionBlockEnabled: "true",
				}}, nil)
				billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				t.Cleanup(billing.Stop)
				gateway := service.NewOpenAIGatewayService(
					&openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{account}},
					nil, nil, nil, nil, nil, cache, cfg, nil, nil, service.NewBillingService(cfg, nil),
					nil, billing, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, settings, nil,
				)
				h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing,
					service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
				h.SetProtocolRouter(service.NewProtocolRouter())
				body := `{"model":"gpt-5.5","max_tokens":64,"input":"hello","messages":[{"role":"user","content":"hello"}],"stream":false`
				if sessionSignal == "prompt_cache_key" {
					body += `,"prompt_cache_key":"policy-session"`
				}
				body += `}`
				router := gin.New()
				router.Use(func(c *gin.Context) {
					c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
						ID: 1803, GroupID: &groupID, User: &service.User{ID: 1703, Status: service.StatusActive},
						Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, AllowMessagesDispatch: true},
					})
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1703})
				})
				router.POST("/openai/v1/responses", h.Responses)
				router.POST("/openai/v1/chat/completions", h.ChatCompletions)
				router.POST("/openai/v1/messages", h.Messages)
				request := func() *httptest.ResponseRecorder {
					rec := httptest.NewRecorder()
					req := httptest.NewRequest(http.MethodPost, "/openai/v1/"+endpoint, strings.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					router.ServeHTTP(rec, req)
					return rec
				}
				first := request()
				require.Equal(t, http.StatusBadRequest, first.Code, first.Body.String())
				require.Equal(t, 1, upstream.calls, first.Body.String())
				second := request()
				require.Equal(t, http.StatusForbidden, second.Code, second.Body.String())
				require.Equal(t, 1, upstream.calls, "blocked session must not reach upstream again")
			})
		}
	}
}
