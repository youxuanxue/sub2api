//go:build unit

package handler

import (
	"io"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestUS052_GeminiChatSelectedHTTPTransport(t *testing.T) {
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformNewAPI} {
		for _, stream := range []bool{false, true} {
			t.Run(platform+map[bool]string{false: "/json", true: "/stream"}[stream], func(t *testing.T) {
				calls := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					body, err := io.ReadAll(r.Body)
					assert.NoError(t, err)
					assert.Equal(t, "/v1/chat/completions", r.URL.Path)
					assert.Equal(t, "Bearer upstream-key", r.Header.Get("Authorization"))
					assert.Equal(t, "wire-model", gjson.GetBytes(body, "model").String())
					assert.Equal(t, "hello", gjson.GetBytes(body, "messages.0.content").String())
					assert.False(t, gjson.GetBytes(body, "contents").Exists())
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
						w.(http.Flusher).Flush()
						_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4,\"total_tokens\":13}}\n\ndata: [DONE]\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`)
					}
				}))
				defer upstream.Close()
				account := service.Account{ID: 501, Platform: platform, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 10, GroupIDs: []int64{10}, Credentials: map[string]any{"api_key": "upstream-key", "base_url": upstream.URL, "model_mapping": map[string]any{"gpt-5.4": "wire-model"}}}
				if platform == service.PlatformNewAPI {
					account.ChannelType = newapiconstant.ChannelTypeOpenAI
				}
				attachHandlerTestProtocolCapability(t, &account, protocolrouter.ProtocolChatCompletions)
				repo := &candidateNativeRepo{accounts: []service.Account{account}}
				cfg := &config.Config{RunMode: config.RunModeSimple}
				cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				cfg.Security.URLAllowlist.AllowPrivateHosts = true
				transport := candidateNativeHTTPUpstream{baseURL: upstream.URL}
				gateway := service.NewGatewayService(repo, nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, transport, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				openai := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, transport, nil, nil, nil, nil, nil, nil, nil, nil)
				router := service.NewProtocolRouter()
				h := &GatewayHandler{protocolRouter: router, gatewayService: gateway, openAIGatewayService: openai}
				api := service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
				service.ProvideTKUniversalModelsProvider(api, gateway, nil, openai, router)
				group := service.Group{ID: 10, Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1, Hydrated: true}
				key := &service.APIKey{ID: 1, UserID: 7, Group: &group, GroupID: &group.ID, User: &service.User{ID: 7, Balance: 10}}
				body := []byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`)
				action := "generateContent"
				if stream {
					action = "streamGenerateContent"
				}
				path := "/v1beta/models/gpt-5.4:" + action
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", path, strings.NewReader(string(body)))
				c.Set(string(middleware.ContextKeyAPIKey), key)
				_, err := api.UniversalResolver().PrepareCandidateIngress(c, key, service.ShapeGemini, path, "gpt-5.4", body, "")
				require.NoError(t, err)
				selection, err := gateway.SelectAccountWithLoadAwareness(c.Request.Context(), key.GroupID, "", "gpt-5.4", nil, "", key.UserID)
				require.NoError(t, err)
				defer selection.ReleaseFunc()
				require.Equal(t, protocolrouter.AdapterGeminiToChat, selection.ProtocolPlan.AdapterID())
				result, err := h.executeGeminiV1BetaSelectedProtocol(c, c.Request.Context(), selection, selection.Account, "gpt-5.4", action, stream, false, 10, "", false)
				require.NoError(t, err, rec.Body.String())
				require.NotNil(t, result)
				require.Equal(t, 1, calls)
				require.Equal(t, "gpt-5.4", result.Model)
				require.Equal(t, "wire-model", result.UpstreamModel)
				require.Equal(t, 9, result.Usage.InputTokens)
				require.Equal(t, 4, result.Usage.OutputTokens)
				require.Contains(t, rec.Body.String(), `"text":"hello"`)
				require.Contains(t, rec.Body.String(), `"finishReason":"STOP"`)
				require.NotContains(t, rec.Body.String(), `"choices"`)
				require.Equal(t, path, c.Request.URL.Path)
			})
		}
	}
}
