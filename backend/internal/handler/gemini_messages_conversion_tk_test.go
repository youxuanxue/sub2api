//go:build unit

package handler

import (
	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeminiMessagesSelectedHTTPTransport(t *testing.T) {
	for _, platform := range []string{service.PlatformAnthropic, service.PlatformNewAPI} {
		for _, stream := range []bool{false, true} {
			t.Run(platform+map[bool]string{false: "/json", true: "/stream"}[stream], func(t *testing.T) {
				calls := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					raw, err := io.ReadAll(r.Body)
					assert.NoError(t, err)
					assert.Equal(t, "/v1/messages", r.URL.Path)
					assert.Equal(t, "claude-sonnet-4-6", gjson.GetBytes(raw, "model").String())
					assert.Equal(t, "hello", gjson.GetBytes(raw, "messages.0.content.0.text").String())
					if platform == service.PlatformAnthropic {
						assert.Equal(t, "upstream-key", r.Header.Get("x-api-key"))
					} else {
						assert.Equal(t, "Bearer upstream-key", r.Header.Get("Authorization"))
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						for _, e := range []string{
							`{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":9}}}`,
							`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
							`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
							`{"type":"content_block_stop","index":0}`,
							`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
							`{"type":"message_stop"}`,
						} {
							_, _ = io.WriteString(w, "data: "+e+"\n\n")
							w.(http.Flusher).Flush()
						}
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":9,"output_tokens":4}}`)
					}
				}))
				defer upstream.Close()
				account := service.Account{ID: 501, Platform: platform, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{10}, Credentials: map[string]any{"api_key": "upstream-key", "base_url": upstream.URL, "model_mapping": map[string]any{"claude-client": "claude-sonnet-4-6"}}, Extra: map[string]any{"anthropic_passthrough": true}}
				if platform == service.PlatformNewAPI {
					account.ChannelType = newapiconstant.ChannelTypeAnthropic
				}
				attachHandlerTestProtocolCapability(t, &account, protocolrouter.ProtocolMessages)
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
				path := "/v1beta/models/claude-client:" + action
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", path, strings.NewReader(string(body)))
				c.Set(string(middleware.ContextKeyAPIKey), key)
				_, err := api.UniversalResolver().PrepareCandidateIngress(c, key, service.ShapeGemini, path, "claude-client", body, "")
				require.NoError(t, err)
				selection, err := gateway.SelectAccountWithLoadAwareness(c.Request.Context(), key.GroupID, "", "claude-client", nil, "", key.UserID)
				require.NoError(t, err)
				defer selection.ReleaseFunc()
				require.Equal(t, protocolrouter.AdapterGeminiToMessages, selection.ProtocolPlan.AdapterID())
				result, err := h.executeGeminiV1BetaSelectedProtocol(c, c.Request.Context(), selection, selection.Account, "claude-client", action, stream, false, 10, "", false)
				require.NoError(t, err)
				require.Equal(t, 1, calls)
				require.Equal(t, "claude-client", result.Model)
				require.Equal(t, "claude-sonnet-4-6", result.UpstreamModel)
				require.Equal(t, 9, result.Usage.InputTokens)
				require.Equal(t, 4, result.Usage.OutputTokens)
				require.Contains(t, rec.Body.String(), `"finishReason":"STOP"`)
				require.Equal(t, path, c.Request.URL.Path)
			})
		}
	}
}
