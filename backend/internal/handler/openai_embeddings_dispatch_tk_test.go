//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestEmbeddings_DispatchesSelectedAccountProtocol(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{service.RoutingModeDirect, service.RoutingModeUniversal} {
		for _, platform := range []string{service.PlatformNewAPI, service.PlatformOpenAI} {
			for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
				t.Run(mode+"/"+platform+"/"+http.StatusText(status), func(t *testing.T) {
					const model = "text-embedding-v4"
					type request struct {
						path, auth string
						body       map[string]any
					}
					requests := make(chan request, 2)
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							http.Error(w, "invalid JSON", http.StatusBadRequest)
							return
						}
						requests <- request{r.URL.Path, r.Header.Get("Authorization"), body}
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(status)
						if status != http.StatusOK {
							_, _ = io.WriteString(w, `{"error":{"message":"invalid embedding input","type":"invalid_request_error","code":"invalid_input"}}`)
							return
						}
						_, _ = io.WriteString(w, `{"object":"list","model":"text-embedding-v4","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]},{"object":"embedding","index":1,"embedding":[0.3,0.4]}],"usage":{"prompt_tokens":4,"total_tokens":4}}`)
					}))
					defer upstream.Close()
					group := service.Group{ID: 10, Platform: platform, Status: service.StatusActive, RateMultiplier: 1, Hydrated: true, AllowMessagesDispatch: true}
					state := &candidateWSSocketState{user: service.User{ID: 7, Status: service.StatusActive, Balance: 100, Concurrency: 2}, groups: []service.Group{group}}
					userRepo, groupRepo, subRepo := candidateWSUserRepo{state: state}, candidateWSGroupRepo{state: state}, candidateWSSubscriptionRepo{state: state}
					account := service.Account{ID: 110, Name: "embedding-upstream", Platform: platform, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 10, GroupIDs: []int64{10}, Credentials: map[string]any{"api_key": "embedding-test-key", "base_url": upstream.URL, "model_mapping": map[string]any{model: model}}}
					if platform == service.PlatformNewAPI {
						account.ChannelType = 17
					}
					repo := &candidateNativeRepo{accounts: []service.Account{account}}
					cfg := &config.Config{RunMode: config.RunModeSimple}
					cfg.Default.RateMultiplier = 1
					cfg.Security.URLAllowlist.AllowInsecureHTTP = true
					cfg.Security.URLAllowlist.AllowPrivateHosts = true
					usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 2)}
					billingCache := service.NewBillingCacheService(nil, userRepo, subRepo, nil, nil, nil, cfg, nil)
					defer billingCache.Stop()
					concurrency := service.NewConcurrencyService(&concurrencyCacheMock{
						acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
						acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
					})
					billing := service.NewBillingService(cfg, nil)
					transport := candidateNativeHTTPUpstream{baseURL: upstream.URL}
					openai := service.NewOpenAIGatewayService(repo, usage, nil, userRepo, subRepo, nil, nil, cfg, nil, concurrency, billing, nil, billingCache, transport, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
					gateway := service.NewGatewayService(repo, groupRepo, usage, nil, userRepo, subRepo, nil, nil, cfg, nil, concurrency, billing, nil, billingCache, nil, transport, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
					keyService := service.NewAPIKeyService(nil, userRepo, groupRepo, subRepo, nil, nil, cfg)
					pr := service.NewProtocolRouter()
					service.ProvideTKUniversalModelsProvider(keyService, gateway, nil, openai, pr)
					key := &service.APIKey{ID: 1, UserID: 7, Status: service.StatusActive, RoutingMode: mode, User: &state.user}
					if mode == service.RoutingModeDirect {
						key.GroupID, key.Group = &group.ID, &group
					}
					h := &OpenAIGatewayHandler{gatewayService: openai, nativeGatewayService: gateway, billingCacheService: billingCache, apiKeyService: keyService, concurrencyHelper: NewConcurrencyHelper(concurrency, SSEPingFormatNone, time.Second), cfg: cfg, protocolRouter: pr}
					body := `{"model":"text-embedding-v4","input":["hello","world"],"encoding_format":"float","dimensions":2}`
					c, response := newGateEmbeddingContext(body)
					c.Set(string(middleware.ContextKeyAPIKey), key)
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7, Concurrency: 2})
					_, err := keyService.UniversalResolver().PrepareCandidateIngress(c, key, service.ShapeOpenAIEmbeddings, "/v1/embeddings", model, []byte(body), "")
					require.NoError(t, err)
					h.Embeddings(c)
					require.Equal(t, status, response.Code, response.Body.String())
					select {
					case got := <-requests:
						path := "/v1/embeddings"
						if platform == service.PlatformNewAPI {
							path = "/compatible-mode/v1/embeddings"
						}
						require.Equal(t, path, got.path)
						require.Equal(t, "Bearer embedding-test-key", got.auth)
						require.Equal(t, model, got.body["model"])
						require.Equal(t, []any{"hello", "world"}, got.body["input"])
						require.Equal(t, float64(2), got.body["dimensions"])
						require.Equal(t, "float", got.body["encoding_format"])
					default:
						t.Fatal("embedding upstream was not called")
					}
					if status != http.StatusOK {
						require.Contains(t, response.Body.String(), "invalid embedding input")
						select {
						case row := <-usage.created:
							t.Fatalf("failed embedding billed account %d", row.AccountID)
						default:
						}
						return
					}
					var result struct {
						Data []struct {
							Embedding []float64 `json:"embedding"`
						} `json:"data"`
					}
					require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
					require.Len(t, result.Data, 2)
					require.Equal(t, []float64{0.1, 0.2}, result.Data[0].Embedding)
					select {
					case row := <-usage.created:
						require.Equal(t, int64(110), row.AccountID)
						require.Equal(t, 4, row.InputTokens)
						require.Zero(t, row.OutputTokens)
					case <-time.After(time.Second):
						t.Fatal("embedding usage missing")
					}
				})
			}
		}
	}
}

func newGateEmbeddingContext(body string) (*gin.Context, *httptest.ResponseRecorder) {
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, response
}
