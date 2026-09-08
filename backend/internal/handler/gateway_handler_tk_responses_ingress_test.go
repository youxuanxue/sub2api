//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type responsesIngressUsageRepo struct {
	service.UsageLogRepository
	logs []*service.UsageLog
}

func (r *responsesIngressUsageRepo) Create(_ context.Context, usage *service.UsageLog) (bool, error) {
	r.logs = append(r.logs, usage)
	return true, nil
}

func newAntigravityIngressHarness(t *testing.T, groupPlatform string, target protocolrouter.Protocol, cfg *config.Config) (*GatewayHandler, *service.Group, *responsesIngressUsageRepo, *plannedOpenAIShapeUpstream) {
	t.Helper()
	group := &service.Group{ID: 21, Platform: groupPlatform, Status: service.StatusActive, Hydrated: true, RateMultiplier: 1}
	account := service.Account{
		ID: 62, Platform: service.PlatformAntigravity, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{
			"base_url": "https://api-us4.tokenkey.dev", "api_key": "edge-test-key",
			"model_mapping": map[string]any{"gemini-3.8-flash": "gemini-3.8-flash-medium"},
		},
	}
	attachHandlerTestProtocolCapability(t, &account, target)
	repo := openAIImagesFailoverAccountRepo{accounts: []service.Account{account}}
	usage := &responsesIngressUsageRepo{}
	upstream := &plannedOpenAIShapeUpstream{}
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	gateway := service.NewGatewayService(repo, &fakeGroupRepo{group: group}, usage, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCache, nil, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	openAI := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
	gemini := service.NewGeminiMessagesCompatService(repo, nil, nil, nil, nil, nil, upstream, nil, cfg)
	h := NewGatewayHandler(gateway, openAI, gemini, nil, nil, service.NewConcurrencyService(nil), billingCache, nil, nil, nil, nil, nil, nil, cfg, nil)
	h.SetProtocolRouter(service.NewProtocolRouter())
	return h, group, usage, upstream
}

func TestGatewayResponsesIngressAntigravityGemini(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, target := range []protocolrouter.Protocol{
		protocolrouter.ProtocolResponses,
		protocolrouter.ProtocolChatCompletions,
		protocolrouter.ProtocolGeminiGenerateContent,
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", target, stream), func(t *testing.T) {
				const model = "gemini-3.8-flash"
				const mappedModel = "gemini-3.8-flash-medium"
				cfg := &config.Config{RunMode: config.RunModeSimple}
				h, group, usage, upstream := newAntigravityIngressHarness(t, service.PlatformAntigravity, target, cfg)
				body := fmt.Sprintf(`{"model":%q,"input":"Reply OK only.","max_output_tokens":1024,"stream":%t}`, model, stream)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
				key := &service.APIKey{ID: 219, GroupID: &group.ID, Group: group, User: &service.User{ID: 1}}
				c.Set(string(middleware2.ContextKeyAPIKey), key)
				c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})
				h.Responses(c)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Contains(t, rec.Body.String(), "OK")
				require.NotContains(t, rec.Body.String(), "response.failed")
				if stream {
					require.Contains(t, rec.Body.String(), "response.completed")
				}
				require.NotNil(t, upstream.request)
				if target != protocolrouter.ProtocolGeminiGenerateContent {
					require.Equal(t, model, gjson.GetBytes(upstream.body, "model").String())
				}
				require.Len(t, usage.logs, 1)
				require.Equal(t, int64(62), usage.logs[0].AccountID)
				require.Equal(t, &group.ID, usage.logs[0].GroupID)
				require.Equal(t, model, usage.logs[0].RequestedModel)
				require.NotNil(t, usage.logs[0].UpstreamModel)
				require.Equal(t, mappedModel, *usage.logs[0].UpstreamModel)
				require.Equal(t, 3, usage.logs[0].InputTokens)
				require.Equal(t, 2, usage.logs[0].OutputTokens)
			})
		}
	}
}

func TestGatewayResponsesIngressPlatformGuards(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, groupPlatform, resolvedPlatform, forcedPlatform, guardPlatform string
		model                                                                string
		wantStatus                                                           int
		wantMessage                                                          string
	}{
		{name: "anthropic_rejects_gemini", groupPlatform: service.PlatformAnthropic, model: "gemini-3.8-flash", wantStatus: http.StatusBadRequest, wantMessage: service.TkUnsupportedModelMessage("gemini-3.8-flash")},
		{name: "antigravity_unknown_model", groupPlatform: service.PlatformAntigravity, model: "gemini-does-not-exist", wantStatus: http.StatusBadRequest, wantMessage: service.TkUnsupportedModelMessage("gemini-does-not-exist")},
		{name: "antigravity_body_guard", groupPlatform: service.PlatformAntigravity, guardPlatform: service.PlatformAntigravity, wantStatus: http.StatusRequestEntityTooLarge, wantMessage: "pre-flight limit"},
		{name: "anthropic_body_guard", groupPlatform: service.PlatformAnthropic, guardPlatform: service.PlatformAnthropic, wantStatus: http.StatusRequestEntityTooLarge, wantMessage: "pre-flight limit"},
		{name: "antigravity_ignores_anthropic_guard", groupPlatform: service.PlatformAntigravity, guardPlatform: service.PlatformAnthropic, wantStatus: http.StatusOK},
		{name: "resolved_antigravity_ignores_anthropic_guard", groupPlatform: service.PlatformComposite, resolvedPlatform: service.PlatformAntigravity, guardPlatform: service.PlatformAnthropic, wantStatus: http.StatusOK},
		{name: "forced_antigravity_overrides_anthropic", groupPlatform: service.PlatformAnthropic, resolvedPlatform: service.PlatformAnthropic, forcedPlatform: service.PlatformAntigravity, guardPlatform: service.PlatformAnthropic, wantStatus: http.StatusOK},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				model := tc.model
				if model == "" {
					model = "gemini-3.8-flash"
				}
				cfg := &config.Config{RunMode: config.RunModeSimple}
				cfg.Gateway.UpstreamBodyGuards = []config.UpstreamBodyGuardConfig{{Platform: tc.guardPlatform, RejectBytes: 1}}
				h, group, usage, upstream := newAntigravityIngressHarness(t, tc.groupPlatform, protocolrouter.ProtocolResponses, cfg)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				body := fmt.Sprintf(`{"model":%q,"input":"Reply OK only.","stream":%t}`, model, stream)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
				ctx := c.Request.Context()
				if tc.resolvedPlatform != "" {
					ctx = service.WithResolvedTargetPlatform(ctx, tc.resolvedPlatform)
				}
				if tc.forcedPlatform != "" {
					ctx = context.WithValue(ctx, ctxkey.ForcePlatform, tc.forcedPlatform)
				}
				c.Request = c.Request.WithContext(ctx)
				key := &service.APIKey{ID: 219, GroupID: &group.ID, Group: group, User: &service.User{ID: 1}}
				c.Set(string(middleware2.ContextKeyAPIKey), key)
				c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})
				h.Responses(c)
				wantStatus := tc.wantStatus
				if tc.name == "anthropic_rejects_gemini" && stream {
					wantStatus = http.StatusOK
					require.Contains(t, rec.Body.String(), "response.failed")
				}
				require.Equal(t, wantStatus, rec.Code, rec.Body.String())
				if tc.wantStatus != http.StatusOK {
					require.Contains(t, rec.Body.String(), tc.wantMessage)
					require.Nil(t, upstream.request)
					require.Empty(t, usage.logs)
					return
				}
				require.Contains(t, rec.Body.String(), "OK")
				require.NotContains(t, rec.Body.String(), "response.failed")
				require.NotNil(t, upstream.request)
				require.Len(t, usage.logs, 1)
			})
		}
	}
}
