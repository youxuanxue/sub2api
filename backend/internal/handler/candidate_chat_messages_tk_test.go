//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
)

func TestCandidateChatMessagesToolsPriorityAndSupplierFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{service.RoutingModeDirect, service.RoutingModeUniversal} {
		for _, stream := range []bool{false, true} {
			for _, first := range []string{"messages", "supplier_forbidden", "supplier_empty", "ordinary_500", "thinking_forced", "implicit_thinking_forced", "supplier_capability"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", mode, stream, first), func(t *testing.T) {
					var mu sync.Mutex
					var hits []string
					var forwarded apicompat.AnthropicRequest
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						mu.Lock()
						defer mu.Unlock()
						hits = append(hits, r.URL.Path)
						if r.URL.Path == "/v1/chat/completions" {
							status := http.StatusInternalServerError
							message := "Upstream access forbidden, please contact administrator (request id: fixture)"
							if first == "supplier_empty" {
								message = "没有可用账号，请稍后重试 (request id: fixture)"
							}
							if first == "ordinary_500" {
								message = "internal error"
							}
							if first == "supplier_capability" {
								status, message = http.StatusBadRequest, "[preflight:R3.forced_tool_choice_incompatible] model has always-on thinking"
							}
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(status)
							_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "unknown_error", "message": message}})
							return
						}
						require.Equal(t, "/v1/messages", r.URL.Path)
						require.Equal(t, "Bearer messages-key", r.Header.Get("Authorization"))
						require.Equal(t, "2023-06-01", r.Header.Get("Anthropic-Version"))
						require.NoError(t, json.NewDecoder(r.Body).Decode(&forwarded))
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_ok\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-fable-5\",\"content\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":0,\"cache_read_input_tokens\":3}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_next\",\"name\":\"lookup_0\",\"input\":{}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"q\\\":\\\"next\\\"}\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					}))
					defer upstream.Close()
					group := service.Group{ID: 1, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1, Hydrated: true}
					state := &candidateWSSocketState{user: service.User{ID: 7, Status: service.StatusActive, Balance: 100, Concurrency: 2}, groups: []service.Group{group}}
					userRepo, groupRepo, subRepo := candidateWSUserRepo{state: state}, candidateWSGroupRepo{state: state}, candidateWSSubscriptionRepo{state: state}
					accounts := make([]service.Account, 0, 2)
					for _, id := range []int64{115, 136} {
						priority, channel, credential, protocol := 250, 1, "chat-key", protocolrouter.ProtocolChatCompletions
						if id == 136 {
							priority, channel, credential, protocol = 110, 14, "messages-key", protocolrouter.ProtocolMessages
						}
						if id == 115 && first != "messages" {
							priority = 1
						}
						a := service.Account{ID: id, Name: credential, Platform: service.PlatformNewAPI, Type: service.AccountTypeAPIKey, ChannelType: channel, Status: service.StatusActive, Schedulable: true, Priority: priority, Concurrency: 10, GroupIDs: []int64{1}, Credentials: map[string]any{"api_key": credential, "base_url": upstream.URL, "model_mapping": map[string]any{"claude-fable-5": "claude-fable-5"}}, Extra: map[string]any{"supplier_source_id": id}}
						attachHandlerTestProtocolCapability(t, &a, protocol)
						accounts = append(accounts, a)
					}
					repo := &candidateNativeRepo{accounts: accounts}
					cfg := &config.Config{RunMode: config.RunModeSimple}
					cfg.Default.RateMultiplier = 1
					cfg.Security.URLAllowlist.AllowInsecureHTTP = true
					cfg.Security.URLAllowlist.AllowPrivateHosts = true
					usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 8)}
					billingCache := service.NewBillingCacheService(nil, userRepo, subRepo, nil, nil, nil, cfg, nil)
					defer billingCache.Stop()
					concurrency := service.NewConcurrencyService(&concurrencyCacheMock{acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }, acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }})
					billing := service.NewBillingService(cfg, nil)
					transport := candidateNativeHTTPUpstream{baseURL: upstream.URL}
					openai := service.NewOpenAIGatewayService(repo, usage, nil, userRepo, subRepo, nil, nil, cfg, nil, concurrency, billing, nil, billingCache, transport, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
					gateway := service.NewGatewayService(repo, groupRepo, usage, nil, userRepo, subRepo, nil, nil, cfg, nil, concurrency, billing, nil, billingCache, nil, transport, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
					keys := service.NewAPIKeyService(nil, userRepo, groupRepo, subRepo, nil, nil, cfg)
					pr := service.NewProtocolRouter()
					service.ProvideTKUniversalModelsProvider(keys, gateway, nil, openai, pr)
					key := &service.APIKey{ID: 1, UserID: 7, Status: service.StatusActive, RoutingMode: mode, User: &state.user}
					if mode == service.RoutingModeDirect {
						key.GroupID = &group.ID
						key.Group = &group
					}
					h := &OpenAIGatewayHandler{gatewayService: openai, nativeGatewayService: gateway, billingCacheService: billingCache, apiKeyService: keys, concurrencyHelper: NewConcurrencyHelper(concurrency, SSEPingFormatNone, time.Second), cfg: cfg, protocolRouter: pr, maxAccountSwitches: 5}
					body := candidateMessagesToolBody(t, stream)
					if first == "thinking_forced" || first == "implicit_thinking_forced" || first == "supplier_capability" {
						body, _ = sjson.SetBytes(body, "tool_choice", map[string]any{"type": "function", "function": map[string]any{"name": "lookup_0"}})
						if first == "thinking_forced" {
							body, _ = sjson.SetBytes(body, "thinking", map[string]any{"type": "adaptive"})
						}
					}
					router := gin.New()
					router.POST("/v1/chat/completions", func(c *gin.Context) {
						c.Set(string(middleware.ContextKeyAPIKey), key)
						c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7, Concurrency: 2})
						_, err := keys.UniversalResolver().PrepareCandidateIngress(c, key, service.ShapeOpenAIChat, "/v1/chat/completions", "claude-fable-5", body, "")
						require.NoError(t, err)
						h.ChatCompletions(c)
					})
					response := httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body))))
					mu.Lock()
					gotHits := append([]string(nil), hits...)
					got := forwarded
					mu.Unlock()
					if first == "ordinary_500" {
						require.GreaterOrEqual(t, response.Code, 500)
						require.Equal(t, []string{"/v1/chat/completions"}, gotHits)
						require.Empty(t, usage.created)
						return
					}
					require.Equal(t, 200, response.Code, response.Body.String())
					wantHits := []string{"/v1/messages"}
					if first != "messages" {
						wantHits = append([]string{"/v1/chat/completions"}, wantHits...)
					}
					require.Equal(t, wantHits, gotHits)
					require.Len(t, got.Tools, 11)
					require.Equal(t, "claude-fable-5", got.Model)
					require.Equal(t, 64, got.MaxTokens)
					require.JSONEq(t, `{"type":"auto","disable_parallel_tool_use":true}`, string(got.ToolChoice))
					if first == "thinking_forced" {
						require.NotNil(t, got.Thinking)
						require.Equal(t, "adaptive", got.Thinking.Type)
					}
					var system []apicompat.AnthropicContentBlock
					require.NoError(t, json.Unmarshal(got.System, &system))
					require.Equal(t, "1h", system[0].CacheControl.TTL)
					require.Contains(t, response.Body.String(), "tool_calls")
					require.Contains(t, response.Body.String(), "lookup_0")
					require.NotContains(t, response.Body.String(), "Upstream access forbidden")
					if !stream {
						var chat apicompat.ChatCompletionsResponse
						require.NoError(t, json.Unmarshal(response.Body.Bytes(), &chat))
						require.Len(t, chat.Choices, 1)
						require.Equal(t, "tool_calls", chat.Choices[0].FinishReason)
						require.Len(t, chat.Choices[0].Message.ToolCalls, 1)
						require.JSONEq(t, `{"q":"next"}`, chat.Choices[0].Message.ToolCalls[0].Function.Arguments)
					} else {
						var arguments strings.Builder
						finish := ""
						for _, line := range strings.Split(response.Body.String(), "\n") {
							payload, ok := strings.CutPrefix(line, "data: ")
							if !ok || payload == "[DONE]" {
								continue
							}
							var chunk apicompat.ChatCompletionsChunk
							require.NoError(t, json.Unmarshal([]byte(payload), &chunk))
							for _, choice := range chunk.Choices {
								if choice.FinishReason != nil {
									finish = *choice.FinishReason
								}
								for _, call := range choice.Delta.ToolCalls {
									arguments.WriteString(call.Function.Arguments)
								}
							}
						}
						require.JSONEq(t, `{"q":"next"}`, arguments.String())
						require.Equal(t, "tool_calls", finish)
					}
					if stream {
						require.Contains(t, response.Body.String(), "[DONE]")
					}
					select {
					case row := <-usage.created:
						require.Equal(t, int64(136), row.AccountID)
						require.Equal(t, 7, row.InputTokens)
						require.Equal(t, 5, row.OutputTokens)
						require.Equal(t, 3, row.CacheReadTokens)
					case <-time.After(2 * time.Second):
						t.Fatal("missing successful usage")
					}
					require.Empty(t, usage.created, "failed attempts must not be billed")
				})
			}
		}
	}
}

func candidateMessagesToolBody(t *testing.T, stream bool) []byte {
	t.Helper()
	tools := make([]any, 11)
	for i := range tools {
		tools[i] = map[string]any{"type": "function", "function": map[string]any{"name": fmt.Sprintf("lookup_%d", i), "parameters": map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}}}
	}
	body, err := json.Marshal(map[string]any{"model": "claude-fable-5", "max_tokens": 64, "stream": stream, "stream_options": map[string]any{"include_usage": true}, "tools": tools, "tool_choice": "auto", "parallel_tool_calls": false, "messages": []any{map[string]any{"role": "system", "content": []any{map[string]any{"type": "text", "text": "Use the available tools.", "cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"}}}}, map[string]any{"role": "user", "content": "Look up the result."}}})
	require.NoError(t, err)
	return body
}
