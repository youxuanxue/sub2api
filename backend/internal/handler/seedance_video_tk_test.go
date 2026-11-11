//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSeedanceVideoDirectAndUniversalHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const model = "doubao-seedance-2-5-260628"
	for _, mode := range []string{service.RoutingModeDirect, service.RoutingModeUniversal} {
		t.Run(mode, func(t *testing.T) {
			var submits, polls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer upstream-key", r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					submits.Add(1)
					assert.Equal(t, "/api/v3/contents/generations/tasks", r.URL.Path)
					body, err := io.ReadAll(r.Body)
					assert.NoError(t, err)
					assert.Equal(t, model, gjson.GetBytes(body, "model").String())
					assert.Contains(t, string(body), "A red ball")
					assert.Contains(t, string(body), "480p")
					_, _ = io.WriteString(w, `{"id":"upstream-private-id"}`)
					return
				}
				polls.Add(1)
				assert.Equal(t, "/api/v3/contents/generations/tasks/upstream-private-id", r.URL.Path)
				_, _ = io.WriteString(w, `{"id":"upstream-private-id","model":"private-sku","status":"succeeded","content":{"video_url":"https://example.com/video.mp4"}}`)
			}))
			defer upstream.Close()
			group := service.Group{ID: 19, Name: "china", Platform: service.PlatformNewAPI, Status: service.StatusActive, Hydrated: true, RateMultiplier: 1}
			state := &candidateWSSocketState{user: service.User{ID: 30, Status: service.StatusActive, Balance: 100}, groups: []service.Group{group}}
			users, groups, subscriptions := candidateWSUserRepo{state: state}, candidateWSGroupRepo{state: state}, candidateWSSubscriptionRepo{state: state}
			account := service.Account{ID: 118, Platform: service.PlatformNewAPI, ChannelType: 45, Type: service.AccountTypeAPIKey,
				Status: service.StatusActive, Schedulable: true, Concurrency: 2, GroupIDs: []int64{19},
				Credentials: map[string]any{"api_key": "upstream-key", "base_url": upstream.URL, "model_mapping": map[string]any{model: model}}}
			accounts := &candidateNativeRepo{accounts: []service.Account{account}}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Security.URLAllowlist.AllowInsecureHTTP, cfg.Security.URLAllowlist.AllowPrivateHosts = true, true
			slots := &concurrencyCacheMock{acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }}
			concurrency := service.NewConcurrencyService(slots)
			billingCache := service.NewBillingCacheService(nil, users, subscriptions, nil, nil, nil, cfg, nil)
			t.Cleanup(billingCache.Stop)
			billing := service.NewBillingService(cfg, nil)
			usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 8)}
			api := service.NewAPIKeyService(nil, users, groups, subscriptions, nil, nil, cfg)
			gateway := service.NewGatewayService(accounts, groups, usage, nil, users, subscriptions, nil, nil, cfg, nil, concurrency, billing, nil, billingCache, nil, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			openai := service.NewOpenAIGatewayService(accounts, usage, nil, users, subscriptions, nil, nil, cfg, nil, concurrency, billing, nil, billingCache, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
			service.ProvideTKUniversalModelsProvider(api, gateway, nil, openai, service.NewProtocolRouter())
			h := &OpenAIGatewayHandler{gatewayService: openai, billingCacheService: billingCache, apiKeyService: api, cfg: cfg,
				concurrencyHelper: NewConcurrencyHelper(concurrency, SSEPingFormatNone, time.Second)}
			cache := repository.NewVideoTaskCache(nil)
			h.SetVideoTaskCache(cache)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				userID := int64(30)
				if c.GetHeader("X-Test-Foreign-User") != "" {
					userID = 31
				}
				key := &service.APIKey{ID: 20, UserID: userID, User: &state.user, RoutingMode: mode, Status: service.StatusActive}
				if mode == service.RoutingModeDirect {
					key.Group, key.GroupID = &group, &group.ID
				}
				c.Set(string(middleware.ContextKeyAPIKey), key)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID})
				if middleware.MaybeResolveUniversal(c, key, api.UniversalResolver()) {
					return
				}
				c.Next()
			})
			router.POST("/v1/contents/generations/tasks", h.SeedanceTasks)
			router.GET("/v1/contents/generations/tasks/:task_id", h.SeedanceTasks)
			router.DELETE("/v1/contents/generations/tasks/:task_id", h.SeedanceTasks)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/contents/generations/tasks", strings.NewReader(`{"model":"`+model+`","content":[{"type":"text","text":"A red ball"}],"resolution":"480p","duration":5}`))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			id := gjson.Get(w.Body.String(), "id").String()
			require.True(t, strings.HasPrefix(id, "vt_"), w.Body.String())
			require.NotContains(t, w.Body.String(), "upstream-private-id")
			record, ok := cache.Lookup(context.Background(), id)
			require.True(t, ok)
			require.Equal(t, int64(118), record.AccountID)
			require.Equal(t, int64(19), record.GroupID)
			select {
			case log := <-usage.created:
				require.Equal(t, int64(118), log.AccountID)
				require.Equal(t, model, log.Model)
			case <-time.After(3 * time.Second):
				t.Fatal("missing submit usage record")
			}
			for _, scenario := range []struct {
				method  string
				foreign bool
				status  int
			}{{http.MethodGet, false, 200}, {http.MethodGet, false, 200}, {http.MethodGet, true, 404}, {http.MethodDelete, true, 404}, {http.MethodDelete, false, 405}} {
				w = httptest.NewRecorder()
				req = httptest.NewRequest(scenario.method, "/v1/contents/generations/tasks/"+id, nil)
				if scenario.foreign {
					req.Header.Set("X-Test-Foreign-User", "yes")
				}
				router.ServeHTTP(w, req)
				require.Equal(t, scenario.status, w.Code, w.Body.String())
				if scenario.status == 200 {
					require.Equal(t, id, gjson.Get(w.Body.String(), "id").String())
					require.Equal(t, model, gjson.Get(w.Body.String(), "model").String())
					require.Equal(t, "succeeded", gjson.Get(w.Body.String(), "status").String())
					require.Equal(t, "https://example.com/video.mp4", gjson.Get(w.Body.String(), "content.video_url").String())
					require.NotContains(t, w.Body.String(), "private")
				}
			}
			require.Equal(t, int32(1), submits.Load())
			require.Equal(t, int32(2), polls.Load())
			require.Empty(t, usage.created, "polling must not submit another charge")
			require.Equal(t, int32(1), atomic.LoadInt32(&slots.releaseAccountCalled))
		})
	}
}

func TestSeedanceVideoStatusProjection(t *testing.T) {
	require.Equal(t, "succeeded", seedanceVideoStatus(gjson.Parse(`{"status":"completed"}`)))
	require.Equal(t, "running", seedanceVideoStatus(gjson.Parse(`{"status":"IN_PROGRESS"}`)))
	require.Equal(t, "failed", seedanceVideoStatus(gjson.Parse(`{"status":"FAILURE"}`)))
}
