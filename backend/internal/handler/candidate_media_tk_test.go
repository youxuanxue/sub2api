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

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/config"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type candidateMediaRouteRepo struct {
	service.CompositeModelRouteRepository
	model string
}

func TestUS050_CandidateStoredVideoKeepsSubmissionRouteAcrossOrigins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/v3/contents/generations/tasks/submitted-upstream-id", r.URL.Path)
		assert.Equal(t, "Bearer submitted-credential", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"submitted-upstream-id","status":"succeeded","content":{"video_url":"https://example.com/result.mp4"}}`)
	}))
	defer upstream.Close()
	cache := repository.NewVideoTaskCache(nil)
	record := &service.VideoTaskRecord{
		PublicTaskID: "vt_submitted", UpstreamTaskID: "submitted-upstream-id",
		UserID: 30, APIKeyID: 20, GroupID: 8, AccountID: 12,
		ChannelType: newapiconstant.ChannelTypeVolcEngine, BaseURL: upstream.URL,
		APIKey: "submitted-credential", OriginModel: "doubao-seedance-1-0-pro-250528",
	}
	require.NoError(t, cache.Save(context.Background(), record))
	openai := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h := &OpenAIGatewayHandler{gatewayService: openai}
	h.SetVideoTaskCache(cache)
	for _, scenario := range []struct {
		name, mode string
		userID     int64
		status     int
	}{
		{"direct-new-key-and-group", service.RoutingModeDirect, 30, http.StatusOK},
		{"universal-new-key-and-group", service.RoutingModeUniversal, 30, http.StatusOK},
		{"foreign-user", service.RoutingModeUniversal, 31, http.StatusNotFound},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			group := &service.Group{ID: 99, Platform: service.PlatformAnthropic, Status: service.StatusActive}
			key := &service.APIKey{ID: 21, UserID: scenario.userID, RoutingMode: scenario.mode, GroupID: &group.ID, Group: group}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/vt_submitted", nil)
			c.Params = gin.Params{{Key: "task_id", Value: record.PublicTaskID}}
			c.Set(string(middleware.ContextKeyAPIKey), key)
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: scenario.userID})
			require.False(t, middleware.MaybeResolveUniversal(c, key, service.NewUniversalRoutingResolver(nil)))
			require.Nil(t, service.CandidateRequestFromContext(c.Request.Context()))
			h.VideoFetch(c)
			require.Equal(t, scenario.status, w.Code, w.Body.String())
			if scenario.status == http.StatusOK {
				require.Equal(t, "https://example.com/result.mp4", gjson.Get(w.Body.String(), "content.video_url").String())
			}
		})
	}
	require.Equal(t, int32(2), calls.Load(), "only the task owner may poll its pinned upstream route")
	saved, ok := cache.Lookup(context.Background(), record.PublicTaskID)
	require.True(t, ok)
	require.Equal(t, int64(8), saved.GroupID)
	require.Equal(t, int64(20), saved.APIKeyID)
	require.Equal(t, int64(12), saved.AccountID)
}

func (r candidateMediaRouteRepo) ListByGroup(context.Context, int64, bool) ([]service.CompositeModelRoute, error) {
	return []service.CompositeModelRoute{{PublicModel: "client-alias", UpstreamModel: r.model, MatchType: service.CompositeRouteMatchExact, Endpoint: service.CompositeRouteEndpointAny, Enabled: true}}, nil
}

func TestUS050_CandidateMediaUsesSelectedModelAndReleasesSlot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []struct {
		name, path, model string
		shape             service.UniversalShape
	}{
		{"embeddings", "/v1/embeddings", "text-embedding-3-small", service.ShapeOpenAIEmbeddings},
		{"images", "/v1/images/generations", "gpt-image-2", service.ShapeOpenAIImages},
		{"video", "/v1/videos", "doubao-seedance-1-0-pro-250528", service.ShapeOpenAIVideo},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var upstreamCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamCalls.Add(1)
				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				assert.Equal(t, scenario.model, gjson.GetBytes(body, "model").String())
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"fixture rejection"}}`)
			}))
			defer upstream.Close()
			group := service.Group{ID: 8, Platform: service.PlatformComposite, Status: service.StatusActive, Hydrated: true, RateMultiplier: 1, AllowImageGeneration: true}
			account := service.Account{ID: 12, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, GroupIDs: []int64{8},
				Credentials: map[string]any{"api_key": "media-key", "base_url": upstream.URL, "model_mapping": map[string]any{scenario.model: scenario.model}}}
			if scenario.shape == service.ShapeOpenAIVideo {
				account.Platform, account.ChannelType = service.PlatformNewAPI, 54
				account.Credentials["base_url"] = newapiintegration.XRTokenBaseURL
			}
			repo := &candidateNativeRepo{accounts: []service.Account{account}}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Security.URLAllowlist.AllowInsecureHTTP, cfg.Security.URLAllowlist.AllowPrivateHosts = true, true
			cache := &concurrencyCacheMock{acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }}
			concurrency := service.NewConcurrencyService(cache)
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			transport := candidateNativeHTTPUpstream{baseURL: upstream.URL}
			composite := service.NewCompositeRouteResolver(candidateMediaRouteRepo{model: scenario.model})
			gateway := service.NewGatewayService(repo, nil, nil, nil, nil, nil, nil, nil, cfg, nil, concurrency, nil, nil, billing, nil, transport, nil, nil, nil, nil, nil, nil, nil, nil, nil, composite, nil, nil, nil)
			openai := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, concurrency, nil, nil, billing, transport, nil, nil, nil, nil, nil, nil, nil, nil)
			api := service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
			service.ProvideTKUniversalModelsProvider(api, gateway, nil, openai, service.NewProtocolRouter())
			key := &service.APIKey{ID: 20, UserID: 30, Group: &group, GroupID: &group.ID, User: &service.User{ID: 30, Balance: 10}}
			body := `{"model":"client-alias","input":"hello","prompt":"test","duration":1}`
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, scenario.path, strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(string(middleware.ContextKeyAPIKey), key)
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 30})
			_, err := api.UniversalResolver().PrepareCandidateIngress(c, key, scenario.shape, scenario.path, "client-alias", []byte(body), "")
			require.NoError(t, err)
			h := &OpenAIGatewayHandler{gatewayService: openai, billingCacheService: billing, apiKeyService: api, concurrencyHelper: NewConcurrencyHelper(concurrency, SSEPingFormatNone, time.Second), cfg: cfg}
			switch scenario.shape {
			case service.ShapeOpenAIEmbeddings:
				h.Embeddings(c)
			case service.ShapeOpenAIImages:
				h.Images(c)
			case service.ShapeOpenAIVideo:
				h.SetVideoTaskCache(repository.NewVideoTaskCache(nil))
				h.VideoSubmit(c)
			}
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			require.Equal(t, int32(1), atomic.LoadInt32(&cache.releaseAccountCalled), "selected capacity must be released on every terminal path: %s", recorder.Body.String())
			if scenario.shape == service.ShapeOpenAIVideo {
				require.Zero(t, upstreamCalls.Load(), "invalid provider duration must reject before paid submission")
				require.Contains(t, recorder.Body.String(), "duration must be at least")
			} else {
				require.Equal(t, int32(1), upstreamCalls.Load())
			}
		})
	}
}
