package routes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type handoffCountingCache struct{ calls int }

func (*handoffCountingCache) Create(context.Context, string, string, service.EdgeHandoffClaims, time.Duration) error {
	return service.ErrEdgeHandoffInvalid
}
func (c *handoffCountingCache) Consume(context.Context, string, string, string) (*service.EdgeHandoffClaims, error) {
	c.calls++
	return nil, service.ErrEdgeHandoffInvalid
}

func handoffRateLimitRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

func TestEdgeHandoffRateLimitProtectsExchangeAndIsolatesFailure(t *testing.T) {
	const origin = "https://edge.example"
	cfg := config.EdgeHandoffConfig{Version: 1, Receiver: &config.EdgeHandoffReceiver{
		Origin: origin, Issuer: "https://prod.example", AdminUserID: 9,
		PublicKeys: map[string]string{"key": base64.RawURLEncoding.EncodeToString(make([]byte, 32))},
	}}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "trust.json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	cache := &handoffCountingCache{}
	owner := service.NewEdgeAdminHandoff(&config.Config{EdgeHandoffFile: path}, cache)
	mr, client := handoffRateLimitRedis(t)
	r := gin.New()
	h := handler.NewEdgeAdminSessionHandler(owner, &service.UserService{}, &service.AuthService{})
	RegisterTKEdgeRoutes(r.Group("/api/v1"), &handler.Handlers{EdgeAdminSession: h}, nil, nil, client)
	r.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })
	proof := strings.Repeat("A", 43)
	body, err := json.Marshal(service.EdgeHandoffExchange{Code: proof, Verifier: proof, Attempt: proof})
	require.NoError(t, err)
	request := func(endpoint, ip string, payload []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/edge/admin-handoff/"+endpoint, strings.NewReader(string(payload)))
		req.RemoteAddr = ip + ":1234"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		r.ServeHTTP(w, req)
		return w
	}
	for _, endpoint := range []string{"mint", "exchange"} {
		payload := body
		if endpoint == "mint" {
			payload = []byte(`{}`)
		}
		for i := 0; i < 25; i++ {
			w := request(endpoint, "192.0.2.1", payload)
			if i < 20 {
				require.Equal(t, http.StatusForbidden, w.Code)
			} else {
				require.Equal(t, http.StatusTooManyRequests, w.Code)
				require.NotEmpty(t, w.Header().Get("Retry-After"))
			}
		}
	}
	require.Equal(t, 20, cache.calls, "excess invalid codes must not reach code consumption")
	require.Equal(t, http.StatusForbidden, request("exchange", "192.0.2.2", body).Code, "one client must not exhaust another client")
	mr.FastForward(time.Minute)
	require.Equal(t, http.StatusForbidden, request("exchange", "192.0.2.1", body).Code, "the client recovers after the window")
	require.NoError(t, client.Close())
	for _, endpoint := range []string{"mint", "exchange"} {
		require.Equal(t, http.StatusServiceUnavailable, request(endpoint, "192.0.2.1", body).Code, "Redis failure must neither bypass protection nor return Caddy-unhealthy 502/504")
	}
	require.Equal(t, 22, cache.calls)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	require.Equal(t, http.StatusOK, w.Code)
}
