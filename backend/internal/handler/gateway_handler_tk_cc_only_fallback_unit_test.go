//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func i64ptr(v int64) *int64 { return &v }

func TestTkCCOnlyFallbackGroupValid(t *testing.T) {
	tests := []struct {
		name  string
		group *service.Group
		want  bool
	}{
		{name: "nil", group: nil, want: false},
		{
			name:  "valid_active_anthropic_non_cc",
			group: &service.Group{ID: 20, Platform: service.PlatformAnthropic, Status: service.StatusActive},
			want:  true,
		},
		{
			name:  "loop_guard_cc_only",
			group: &service.Group{ID: 20, Platform: service.PlatformAnthropic, Status: service.StatusActive, ClaudeCodeOnly: true},
			want:  false,
		},
		{
			name:  "inactive",
			group: &service.Group{ID: 20, Platform: service.PlatformAnthropic, Status: "disabled"},
			want:  false,
		},
		{
			name:  "wrong_platform",
			group: &service.Group{ID: 20, Platform: service.PlatformOpenAI, Status: service.StatusActive},
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tkCCOnlyFallbackGroupValid(tt.group))
		})
	}
}

// newCCOnlyFallbackContext builds a gin context with a request whose context
// optionally carries a hydrated group (so ResolveGroupByID resolves repo-free
// or via the fake repo).
func newCCOnlyFallbackContext() (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	c.Request = c.Request.WithContext(context.Background())
	return c, rec
}

func newCCOnlyFallbackHandler(t *testing.T, fallbackGroup *service.Group) (*GatewayHandler, func()) {
	t.Helper()
	gwSvc := service.NewGatewayService(
		nil,
		&fakeGroupRepo{group: fallbackGroup},
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil,
	)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	h := &GatewayHandler{
		gatewayService:      gwSvc,
		billingCacheService: billingCacheSvc,
		cfg:                 cfg,
	}
	return h, func() { billingCacheSvc.Stop() }
}

// ccOnlyAPIKey returns an apiKey bound to a CC-only group with the given
// fallback_group_id (nil = none).
func ccOnlyAPIKey(fallbackGroupID *int64) *service.APIKey {
	groupID := int64(1)
	return &service.APIKey{
		ID:      11,
		GroupID: &groupID,
		User:    &service.User{ID: 13},
		Group: &service.Group{
			ID:              1,
			Platform:        service.PlatformAnthropic,
			Status:          service.StatusActive,
			ClaudeCodeOnly:  true,
			FallbackGroupID: fallbackGroupID,
		},
	}
}

func TestTkResolveCCOnlyFallback_NoFallbackKeeps403(t *testing.T) {
	h, cleanup := newCCOnlyFallbackHandler(t, nil)
	defer cleanup()
	c, rec := newCCOnlyFallbackContext()

	forbiddenCalled := false
	fallback, handled := h.tkResolveCCOnlyFallback(c, ccOnlyAPIKey(nil), zap.NewNop(),
		func() { forbiddenCalled = true; rec.WriteHeader(http.StatusForbidden) },
		func(status int, code, message string) {
			t.Fatalf("unexpected billing error %d %s %s", status, code, message)
		},
	)

	require.True(t, handled)
	require.Nil(t, fallback)
	require.True(t, forbiddenCalled)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestTkResolveCCOnlyFallback_CCOnlyFallbackKeeps403(t *testing.T) {
	// Fallback group is itself CC-only → loop guard → keep 403.
	fallbackGroup := &service.Group{
		ID:             20,
		Platform:       service.PlatformAnthropic,
		Status:         service.StatusActive,
		ClaudeCodeOnly: true,
	}
	h, cleanup := newCCOnlyFallbackHandler(t, fallbackGroup)
	defer cleanup()
	c, rec := newCCOnlyFallbackContext()

	forbiddenCalled := false
	fallback, handled := h.tkResolveCCOnlyFallback(c, ccOnlyAPIKey(i64ptr(20)), zap.NewNop(),
		func() { forbiddenCalled = true; rec.WriteHeader(http.StatusForbidden) },
		func(status int, code, message string) {
			t.Fatalf("unexpected billing error %d %s %s", status, code, message)
		},
	)

	require.True(t, handled)
	require.Nil(t, fallback)
	require.True(t, forbiddenCalled)
}

func TestTkResolveCCOnlyFallback_InactiveFallbackKeeps403(t *testing.T) {
	fallbackGroup := &service.Group{
		ID:       20,
		Platform: service.PlatformAnthropic,
		Status:   "disabled",
	}
	h, cleanup := newCCOnlyFallbackHandler(t, fallbackGroup)
	defer cleanup()
	c, rec := newCCOnlyFallbackContext()

	forbiddenCalled := false
	fallback, handled := h.tkResolveCCOnlyFallback(c, ccOnlyAPIKey(i64ptr(20)), zap.NewNop(),
		func() { forbiddenCalled = true; rec.WriteHeader(http.StatusForbidden) },
		func(status int, code, message string) {
			t.Fatalf("unexpected billing error %d %s %s", status, code, message)
		},
	)

	require.True(t, handled)
	require.Nil(t, fallback)
	require.True(t, forbiddenCalled)
}

func TestTkResolveCCOnlyFallback_ValidFallbackRoutes(t *testing.T) {
	fallbackGroup := &service.Group{
		ID:       20,
		Name:     "default-fallback",
		Platform: service.PlatformAnthropic,
		Status:   service.StatusActive,
	}
	h, cleanup := newCCOnlyFallbackHandler(t, fallbackGroup)
	defer cleanup()
	c, _ := newCCOnlyFallbackContext()

	origKey := ccOnlyAPIKey(i64ptr(20))
	fallback, handled := h.tkResolveCCOnlyFallback(c, origKey, zap.NewNop(),
		func() { t.Fatal("unexpected 403 for valid fallback") },
		func(status int, code, message string) {
			t.Fatalf("unexpected billing error %d %s %s", status, code, message)
		},
	)

	require.False(t, handled)
	require.NotNil(t, fallback)
	// The returned key must be re-bound to the fallback group (group 20), and the
	// original key must be untouched (still bound to the CC-only group 1).
	require.NotNil(t, fallback.GroupID)
	require.Equal(t, int64(20), *fallback.GroupID)
	require.Equal(t, int64(20), fallback.Group.ID)
	require.False(t, fallback.Group.ClaudeCodeOnly)
	require.Equal(t, int64(1), *origKey.GroupID, "original apiKey must not be mutated")
}

// TestChatCompletions_CCOnlyValidFallback_NotForbidden drives the full
// ChatCompletions handler entry point: a CC-only group with a valid non-CC
// fallback must NOT return the CC-only 403. The request proceeds past the guard
// and (with no schedulable accounts) fails later with a non-403 status — proving
// the 403 was bypassed and the request was routed through the fallback group.
func TestChatCompletions_CCOnlyValidFallback_NotForbidden(t *testing.T) {
	gin.SetMode(gin.TestMode)

	fallbackGroup := &service.Group{
		ID:       20,
		Name:     "default-fallback",
		Platform: service.PlatformAnthropic,
		Status:   service.StatusActive,
		Hydrated: true,
	}
	h, cleanup := newTestGatewayHandler(t, fallbackGroup, nil)
	defer cleanup()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := `{"model":"claude-3-5-sonnet","stream":false,"messages":[{"role":"user","content":"hi"}]}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	groupID := int64(1)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      11,
		GroupID: &groupID,
		User:    &service.User{ID: 13},
		Group: &service.Group{
			ID:              1,
			Platform:        service.PlatformAnthropic,
			Status:          service.StatusActive,
			ClaudeCodeOnly:  true,
			FallbackGroupID: i64ptr(20),
		},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 13, Concurrency: 1})

	h.ChatCompletions(c)

	require.NotEqual(t, http.StatusForbidden, rec.Code,
		"valid fallback must bypass the CC-only 403; got body %s", rec.Body.String())
	require.NotContains(t, rec.Body.String(), "restricted to Claude Code clients")
}
