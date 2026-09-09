//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type signatureTestTokenCache struct{ service.GeminiTokenCache }

func (signatureTestTokenCache) GetAccessToken(context.Context, string) (string, error) {
	return "vertex-test-token", nil
}

func TestGeminiSelectedProtocolRepairsTransportSignature(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, transport := range []string{"native", "planned_edge", "planned_vertex"} {
		for _, clean := range []bool{false, true} {
			governed := transport != "native"
			name := transport
			if clean {
				name += "/switched_or_missing_binding"
			} else {
				name += "/same_binding"
			}
			t.Run(name, func(t *testing.T) {
				const model = "gemini-3.8-flash"
				body := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"id":"123"}},"thoughtSignature":"old-account-signature"}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"result":"OK"}}},{"inlineData":{"mimeType":"image/png","data":"test-image"}}]}]}`)
				account := &service.Account{ID: 47, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
					Status: service.StatusActive, Schedulable: true, Concurrency: 10, GroupIDs: []int64{10},
					Credentials: map[string]any{"api_key": "test-key", "base_url": "https://api-us4.tokenkey.dev", "model_mapping": map[string]any{model: model}}}
				if governed {
					account.Platform = service.PlatformAntigravity
					if transport == "planned_vertex" {
						account.Platform, account.Type = service.PlatformNewAPI, service.AccountTypeServiceAccount
						account.ChannelType = newapiconstant.ChannelTypeVertexAi
						account.Credentials["service_account_json"] = `{"project_id":"test-project","private_key":"test-only","client_email":"test@example.invalid"}`
					}
					attachHandlerTestProtocolCapability(t, account, protocolrouter.ProtocolGeminiGenerateContent)
				}
				request, err := newCanonicalProtocolRequest(protocolrouter.ProtocolGeminiGenerateContent, protocolrouter.ResponsesPathNone, model, true, body)
				require.NoError(t, err)
				router := service.NewProtocolRouter()
				repo := &candidateNativeRepo{accounts: []service.Account{*account}}
				upstream := &plannedOpenAIShapeUpstream{}
				cfg := &config.Config{RunMode: config.RunModeSimple}
				h := &GatewayHandler{
					protocolRouter:      router,
					gatewayService:      service.NewGatewayService(repo, nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil),
					geminiCompatService: service.NewGeminiMessagesCompatService(repo, nil, nil, nil, service.NewGeminiTokenProvider(repo, signatureTestTokenCache{}, nil), nil, upstream, nil, cfg),
				}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+":streamGenerateContent", strings.NewReader(string(body)))
				ctx := service.WithProtocolRouting(c.Request.Context(), router, request)
				c.Request = c.Request.WithContext(ctx)
				selection := &service.AccountSelectionResult{Account: account}
				if governed {
					group := &service.Group{ID: 10, Platform: service.PlatformGemini, Status: service.StatusActive, RateMultiplier: 1, Hydrated: true}
					key := &service.APIKey{ID: 1, UserID: 7, Group: group, GroupID: &group.ID, User: &service.User{ID: 7, Balance: 10}}
					api := service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
					service.ProvideTKUniversalModelsProvider(api, h.gatewayService, nil, &service.OpenAIGatewayService{}, router)
					c.Set(string(middleware.ContextKeyAPIKey), key)
					_, err = api.UniversalResolver().PrepareCandidateIngress(c, key, service.ShapeGemini, c.Request.URL.Path, model, body, "")
					require.NoError(t, err)
					ctx = c.Request.Context()
					selection, err = h.gatewayService.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", model, nil, "", key.UserID)
					require.NoError(t, err)
					defer selection.ReleaseFunc()
				}
				result, err := h.executeGeminiV1BetaSelectedProtocol(c, ctx, selection, account, model, "streamGenerateContent", true, true, 1, "session", clean)
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, recorder.Code)
				require.Equal(t, 3, result.Usage.InputTokens)
				want := "old-account-signature"
				if clean {
					want = "skip_thought_signature_validator"
				}
				require.Equal(t, want, gjson.GetBytes(upstream.body, "contents.0.parts.0.thoughtSignature").String())
				require.Equal(t, "123", gjson.GetBytes(upstream.body, "contents.0.parts.0.functionCall.args.id").String())
				require.Equal(t, "OK", gjson.GetBytes(upstream.body, "contents.1.parts.0.functionResponse.response.result").String())
				imagePath := "contents.1.parts.1.inlineData.data"
				if transport == "planned_vertex" {
					imagePath = "contents.1.parts.0.functionResponse.parts.0.inlineData.data"
				}
				require.Equal(t, "test-image", gjson.GetBytes(upstream.body, imagePath).String())
				require.Equal(t, body, request.Body(), "transport repair must not mutate canonical history")
				if governed {
					require.Equal(t, request.Digest(), selection.ProtocolPlan.RequestDigest())
				}
			})
		}
	}
}
