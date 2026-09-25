//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
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
	"github.com/tidwall/gjson"
)

// Exercise real candidate middleware, including Direct Anthropic billing groups
// backed by Gemini accounts. Quota identity must not select execution guards.
func TestUS057_GeminiOpenAICompatMiddlewareIngressUsesAdmittedPlan(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var pixel bytes.Buffer
	require.NoError(t, png.Encode(&pixel, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	imageData := base64.StdEncoding.EncodeToString(pixel.Bytes())
	for _, endpoint := range []string{"chat", "responses"} {
		for _, mode := range []string{service.RoutingModeDirect, service.RoutingModeUniversal} {
			for _, stream := range []bool{false, true} {
				for _, scenario := range []string{"image", "text", "billing_guard", "execution_guard", "generic"} {
					if scenario != "image" && (stream || mode == service.RoutingModeUniversal || endpoint != "responses") {
						continue
					}
					generateImage := scenario != "text"
					name := endpoint + "/" + mode + "/" + scenario + "/text/nonstream"
					if generateImage {
						name = strings.Replace(name, "text", "image", 1)
					}
					if stream {
						name = strings.Replace(name, "nonstream", "stream", 1)
					}
					t.Run(name, func(t *testing.T) {
						model := "gemini-3.8-flash"
						if generateImage {
							model = "gemini-3.1-flash-image"
						}
						var mu sync.Mutex
						var calls int
						var forwarded []byte
						var upstreamPath, upstreamKey string
						upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							body, err := io.ReadAll(r.Body)
							require.NoError(t, err)
							mu.Lock()
							calls++
							forwarded = body
							upstreamPath = r.URL.Path
							upstreamKey = r.Header.Get("x-goog-api-key")
							mu.Unlock()
							// Match the actual Web Worker rejection, so bypassing Plan fails visibly.
							if scenario != "generic" && gjson.GetBytes(body, "generationConfig.maxOutputTokens").Exists() {
								w.WriteHeader(http.StatusBadRequest)
								_, _ = io.WriteString(w, `{"error":{"code":400,"message":"maxOutputTokens is unsupported"}}`)
								return
							}
							parts := []any{map[string]any{"text": "GEMINI_OK"}}
							if generateImage {
								parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": imageData}})
							}
							response, err := json.Marshal(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": parts}, "finishReason": "STOP"}}, "usageMetadata": map[string]any{"promptTokenCount": 3, "candidatesTokenCount": 4}})
							require.NoError(t, err)
							if stream {
								w.Header().Set("Content-Type", "text/event-stream")
								_, _ = io.WriteString(w, "data: "+string(response)+"\n\n")
							} else {
								w.Header().Set("Content-Type", "application/json")
								_, _ = w.Write(response)
							}
						}))
						defer upstream.Close()
						group := service.Group{ID: 740, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1, Hydrated: true, AllowMessagesDispatch: true, AllowImageGeneration: true, SupportedModelScopes: []string{"claude", "gemini_text", "gemini_image"}}
						state := &candidateWSSocketState{user: service.User{ID: 7, Status: service.StatusActive, Balance: 100, Concurrency: 2}, groups: []service.Group{group}}
						userRepo, groupRepo, subRepo := candidateWSUserRepo{state: state}, candidateWSGroupRepo{state: state}, candidateWSSubscriptionRepo{state: state}
						account := service.Account{ID: 200, Name: "gemini-web-fixture", Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 10, GroupIDs: []int64{group.ID}, Credentials: map[string]any{"api_key": "web-test-key", "base_url": "https://gemini.example.com", service.GeminiWebRelayCredentialKey: scenario != "generic", "model_mapping": map[string]any{model: model}}}
						attachHandlerTestProtocolCapability(t, &account, protocolrouter.ProtocolGeminiGenerateContent)
						account.ProtocolEndpointCapability.ProbeEvidence = service.ProtocolProbeEvidence{NativeDeclaration: true}
						repo := &geminiMessagesIngressRepo{candidateNativeRepo{accounts: []service.Account{account}}}
						cfg := &config.Config{RunMode: config.RunModeSimple}
						cfg.Default.RateMultiplier = 1
						if scenario == "billing_guard" || scenario == "execution_guard" {
							guardPlatform := service.PlatformAnthropic
							if scenario == "execution_guard" {
								guardPlatform = service.PlatformGemini
							}
							cfg.Gateway.UpstreamBodyGuards = []config.UpstreamBodyGuardConfig{{Platform: guardPlatform, RejectBytes: 1}}
						}
						cfg.Security.URLAllowlist.AllowInsecureHTTP = true
						cfg.Security.URLAllowlist.AllowPrivateHosts = true
						usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 8)}
						billingCache := service.NewBillingCacheService(nil, userRepo, subRepo, nil, nil, nil, cfg, nil)
						defer billingCache.Stop()
						concurrency := service.NewConcurrencyService(&concurrencyCacheMock{acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }, acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }})
						billing := service.NewBillingService(cfg, nil)
						transport := candidateNativeHTTPUpstream{baseURL: upstream.URL}
						gateway := service.NewGatewayService(repo, groupRepo, usage, nil, userRepo, subRepo, nil, nil, cfg, nil, concurrency, billing, nil, billingCache, nil, transport, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
						openai := service.NewOpenAIGatewayService(repo, usage, nil, userRepo, subRepo, nil, nil, cfg, nil, concurrency, billing, nil, billingCache, transport, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
						keys := service.NewAPIKeyService(nil, userRepo, groupRepo, subRepo, nil, nil, cfg)
						protocolRouter := service.NewProtocolRouter()
						service.ProvideTKUniversalModelsProvider(keys, gateway, nil, openai, protocolRouter)
						key := &service.APIKey{ID: 1, UserID: 7, Status: service.StatusActive, RoutingMode: mode, User: &state.user}
						if mode == service.RoutingModeDirect {
							key.GroupID = &group.ID
							key.Group = &group
						}
						h := &GatewayHandler{gatewayService: gateway, geminiCompatService: service.NewGeminiMessagesCompatService(repo, groupRepo, nil, nil, nil, nil, transport, nil, cfg), openAIGatewayService: openai, billingCacheService: billingCache, apiKeyService: keys, concurrencyHelper: NewConcurrencyHelper(concurrency, SSEPingFormatNone, time.Second), cfg: cfg, protocolRouter: protocolRouter, maxAccountSwitches: 5, maxAccountSwitchesGemini: 5}
						path := "/v1/chat/completions"
						request := map[string]any{"model": model, "max_completion_tokens": 128, "stream": stream, "messages": []any{map[string]any{"role": "user", "content": "Draw a small red apple"}}}
						if endpoint == "responses" {
							path = "/v1/responses"
							request = map[string]any{"model": model, "max_output_tokens": 128, "stream": stream, "input": "Draw a small red apple"}
						}
						if generateImage {
							request["generationConfig"] = map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}, "imageConfig": map[string]any{"aspectRatio": "4:3"}}
						}
						body, err := json.Marshal(request)
						require.NoError(t, err)
						router := gin.New()
						router.POST(path, func(c *gin.Context) {
							c.Set(string(middleware.ContextKeyAPIKey), key)
							c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7, Concurrency: 2})
							if middleware.MaybeResolveUniversal(c, key, keys.UniversalResolver()) {
								return
							}
							actual, ok := service.CandidateExecutionPlatform(c.Request.Context())
							require.True(t, ok)
							require.Equal(t, service.PlatformGemini, actual)
							quotaBefore := service.QuotaPlatform(c.Request.Context(), key)
							wantQuota := service.PlatformAnthropic
							if mode == service.RoutingModeUniversal {
								wantQuota = ""
							}
							require.Equal(t, wantQuota, quotaBefore)
							if endpoint == "responses" {
								h.Responses(c)
							} else {
								h.ChatCompletions(c)
							}
							require.Equal(t, quotaBefore, service.QuotaPlatform(c.Request.Context(), key), "execution must preserve quota semantics")
						})
						rec := httptest.NewRecorder()
						router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
						if scenario == "execution_guard" {
							require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
							require.Contains(t, rec.Body.String(), "pre-flight limit")
							mu.Lock()
							require.Zero(t, calls, "Gemini execution body guard must reject before upstream")
							mu.Unlock()
							require.Empty(t, usage.created)
							return
						}
						require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
						mu.Lock()
						gotCalls, gotPath, gotKey, gotBody := calls, upstreamPath, upstreamKey, append([]byte(nil), forwarded...)
						mu.Unlock()
						require.Equal(t, 1, gotCalls)
						action := "generateContent"
						if stream {
							action = "streamGenerateContent"
						}
						require.Equal(t, "/v1beta/models/"+model+":"+action, gotPath)
						require.Equal(t, "web-test-key", gotKey)
						if scenario == "generic" {
							require.Equal(t, int64(128), gjson.GetBytes(gotBody, "generationConfig.maxOutputTokens").Int(), "generic Gemini preserves the requested budget")
						} else {
							require.False(t, gjson.GetBytes(gotBody, "generationConfig.maxOutputTokens").Exists(), "Web execution must use the admitted effective request")
						}
						require.Equal(t, "Draw a small red apple", gjson.GetBytes(gotBody, "contents.0.parts.0.text").String())
						require.Contains(t, rec.Body.String(), "GEMINI_OK")
						if generateImage {
							require.Equal(t, "4:3", gjson.GetBytes(gotBody, "generationConfig.imageConfig.aspectRatio").String())
							if !stream {
								outputText := "choices.0.message.content"
								if endpoint == "responses" {
									outputText = "output.0.content.#.text"
								}
								require.Contains(t, gjson.Get(rec.Body.String(), outputText).String(), "data:image/png;base64,"+imageData, "image must be projected into the legal client text field")
							} else {
								require.Contains(t, rec.Body.String(), "data:image/png;base64,"+imageData)
								terminal := "data: [DONE]"
								if endpoint == "responses" {
									terminal = "response.completed"
								}
								require.Contains(t, rec.Body.String(), terminal)
							}
						}
						select {
						case row := <-usage.created:
							require.Equal(t, account.ID, row.AccountID)
							require.Equal(t, 3, row.InputTokens)
							require.Equal(t, 4, row.OutputTokens)
							if generateImage {
								require.Equal(t, 1, row.ImageCount)
							} else {
								require.Zero(t, row.ImageCount)
							}
						default:
							t.Fatal("successful compatibility request must be recorded once")
						}
						require.Empty(t, usage.created, "one client request must not meter twice")
					})
				}
			}
		}
	}
}
