//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type candidateChatSlotCache struct {
	concurrencyCacheMock
	release func(int64)
}

func (c *candidateChatSlotCache) ReleaseAccountSlot(_ context.Context, id int64, _ string) error {
	c.release(id)
	return nil
}

func TestUS050_CandidateChatHangFailoverCompletesAndMetersOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{service.RoutingModeDirect, service.RoutingModeUniversal} {
		for _, scenario := range []string{"no_headers", "empty_200", "buffered", "disconnect", "partial_content", "partial_tool", "exhausted"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				var mu sync.Mutex
				var hits []string
				var released []int64
				canceled := make(chan struct{}, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					auth := r.Header.Get("Authorization")
					mu.Lock()
					hits = append(hits, auth)
					mu.Unlock()
					if scenario == "exhausted" {
						w.WriteHeader(http.StatusBadGateway)
						_, _ = io.WriteString(w, `{"error":{"message":"upstream unavailable","type":"server_error"}}`)
						return
					}
					if auth == "Bearer bad" {
						if scenario == "disconnect" {
							conn, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								return
							}
							_ = conn.Close()
							canceled <- struct{}{}
							return
						}
						if strings.HasPrefix(scenario, "partial_") {
							w.Header().Set("Content-Type", "text/event-stream")
							delta := `{"content":"partial"}`
							if scenario == "partial_tool" {
								delta = `{"tool_calls":[{"index":0,"id":"call_partial","type":"function","function":{"name":"lookup","arguments":"{}"}}]}`
							}
							_, _ = io.WriteString(w, "data: {\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":"+delta+"}]}\n\ndata: {\"model\":\"gpt-5.4\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n")
							return
						}
						if scenario == "empty_200" {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, ": waiting\n\ndata: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n")
							w.(http.Flusher).Flush()
						}
						select {
						case <-r.Context().Done():
							canceled <- struct{}{}
						case <-time.After(5 * time.Second):
						}
						return
					}
					if scenario == "buffered" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"id":"chat-recovered","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
					} else {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"id\":\"chat-recovered\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"recovered\"}}]}\n\ndata: {\"id\":\"chat-recovered\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n")
					}
				}))
				defer upstream.Close()
				group := service.Group{ID: 10, Platform: service.PlatformNewAPI, Status: service.StatusActive, RateMultiplier: 1, Hydrated: true, AllowMessagesDispatch: true}
				state := &candidateWSSocketState{user: service.User{ID: 7, Status: service.StatusActive, Balance: 100, Concurrency: 2}, groups: []service.Group{group}}
				userRepo, groupRepo, subRepo := candidateWSUserRepo{state: state}, candidateWSGroupRepo{state: state}, candidateWSSubscriptionRepo{state: state}
				accounts := []service.Account{}
				credentials := []string{"bad", "good"}
				if scenario == "exhausted" {
					credentials = []string{"bad", "good", "third", "fourth"}
				}
				for i, credential := range credentials {
					a := service.Account{ID: int64(115 + i), Name: credential, Platform: service.PlatformNewAPI, ChannelType: 1, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: i + 1, Concurrency: 10, GroupIDs: []int64{10}, Credentials: map[string]any{"api_key": credential, "base_url": upstream.URL, "model_mapping": map[string]any{"gpt-5.4": "gpt-5.4"}}}
					attachHandlerTestProtocolCapability(t, &a, protocolrouter.ProtocolChatCompletions)
					accounts = append(accounts, a)
				}
				repo := &candidateNativeRepo{accounts: accounts}
				cfg := &config.Config{RunMode: config.RunModeSimple}
				cfg.Default.RateMultiplier = 1
				cfg.Gateway.NewAPIChatFirstOutputTimeout = 1
				cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				cfg.Security.URLAllowlist.AllowPrivateHosts = true
				usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 8)}
				billingCache := service.NewBillingCacheService(nil, userRepo, subRepo, nil, nil, nil, cfg, nil)
				defer billingCache.Stop()
				concurrency := service.NewConcurrencyService(&candidateChatSlotCache{concurrencyCacheMock: concurrencyCacheMock{
					acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
					acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
				}, release: func(id int64) {
					mu.Lock()
					released = append(released, id)
					mu.Unlock()
				}})
				billing := service.NewBillingService(cfg, nil)
				transport := candidateNativeHTTPUpstream{baseURL: upstream.URL}
				openai := service.NewOpenAIGatewayService(repo, usage, nil, userRepo, subRepo, nil, nil, cfg, nil, concurrency, billing, nil, billingCache, transport, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
				gateway := service.NewGatewayService(repo, groupRepo, usage, nil, userRepo, subRepo, nil, nil, cfg, nil, concurrency, billing, nil, billingCache, nil, transport, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				keyService := service.NewAPIKeyService(nil, userRepo, groupRepo, subRepo, nil, nil, cfg)
				pr := service.NewProtocolRouter()
				service.ProvideTKUniversalModelsProvider(keyService, gateway, nil, openai, pr)
				key := &service.APIKey{ID: 1, UserID: 7, Status: service.StatusActive, RoutingMode: mode, User: &state.user}
				if mode == service.RoutingModeDirect {
					key.GroupID = &group.ID
					key.Group = &group
				}
				h := &OpenAIGatewayHandler{gatewayService: openai, nativeGatewayService: gateway, billingCacheService: billingCache, apiKeyService: keyService, concurrencyHelper: NewConcurrencyHelper(concurrency, SSEPingFormatNone, time.Second), cfg: cfg, protocolRouter: pr, maxAccountSwitches: 5}
				body := `{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`
				if scenario == "buffered" {
					body = strings.Replace(body, `"stream":true`, `"stream":false`, 1)
				}
				router := gin.New()
				router.POST("/v1/chat/completions", func(c *gin.Context) {
					c.Set(string(middleware.ContextKeyAPIKey), key)
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7, Concurrency: 2})
					_, err := keyService.UniversalResolver().PrepareCandidateIngress(c, key, service.ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", []byte(body), "")
					if err != nil {
						c.String(500, "prepare: %v", err)
						return
					}
					h.ChatCompletions(c)
				})
				server := httptest.NewServer(router)
				defer server.Close()
				client := &http.Client{Timeout: 4 * time.Second}
				res, err := client.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
				require.NoError(t, err)
				response, err := io.ReadAll(res.Body)
				_ = res.Body.Close()
				require.NoError(t, err)
				if scenario == "exhausted" {
					require.GreaterOrEqual(t, res.StatusCode, 500, string(response))
					mu.Lock()
					actualHits := append([]string(nil), hits...)
					actualReleased := append([]int64(nil), released...)
					mu.Unlock()
					require.Equal(t, []string{"Bearer bad", "Bearer good", "Bearer third"}, actualHits)
					require.ElementsMatch(t, []int64{115, 116, 117}, actualReleased)
					select {
					case row := <-usage.created:
						t.Fatalf("failed attempts billed account %d", row.AccountID)
					default:
					}
					return
				}
				if strings.HasPrefix(scenario, "partial_") {
					require.Contains(t, string(response), "partial")
					require.NotContains(t, string(response), "recovered")
					require.NotContains(t, string(response), "[DONE]")
					mu.Lock()
					actualHits := append([]string(nil), hits...)
					actualReleased := append([]int64(nil), released...)
					mu.Unlock()
					require.Equal(t, []string{"Bearer bad"}, actualHits)
					require.Equal(t, []int64{115}, actualReleased)
					select {
					case row := <-usage.created:
						require.Equal(t, int64(115), row.AccountID)
						require.Equal(t, 2, row.InputTokens)
						require.Equal(t, 1, row.OutputTokens)
					case <-time.After(time.Second):
						t.Fatal("missing partial usage")
					}
					return
				}
				require.Equal(t, 200, res.StatusCode, string(response))
				require.Contains(t, string(response), "recovered")
				require.NotContains(t, string(response), "waiting")
				if scenario != "buffered" {
					require.Contains(t, string(response), "[DONE]")
				}
				select {
				case <-canceled:
				case <-time.After(time.Second):
					t.Fatal("failed upstream not canceled")
				}
				mu.Lock()
				actualHits := append([]string(nil), hits...)
				actualReleased := append([]int64(nil), released...)
				mu.Unlock()
				require.Equal(t, []string{"Bearer bad", "Bearer good"}, actualHits)
				require.ElementsMatch(t, []int64{115, 116}, actualReleased)
				select {
				case row := <-usage.created:
					require.Equal(t, int64(116), row.AccountID)
					require.Equal(t, int64(1), row.APIKeyID)
					require.Equal(t, 2, row.InputTokens)
					require.Equal(t, 1, row.OutputTokens)
				case <-time.After(2 * time.Second):
					t.Fatal("missing successful usage")
				}
				select {
				case row := <-usage.created:
					t.Fatalf("duplicate usage for account %d", row.AccountID)
				default:
				}
			})
		}
	}
}
