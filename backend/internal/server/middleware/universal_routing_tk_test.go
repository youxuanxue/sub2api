//go:build unit

package middleware

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type stubSpanLister struct {
	groups []service.Group
	err    error
}

func (s *stubSpanLister) GetAvailableGroups(_ context.Context, _ int64) ([]service.Group, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.groups, nil
}

func activeGroup(id int64, platform string) service.Group {
	return service.Group{ID: id, Platform: platform, Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard}
}

// universalGroupRepoStub satisfies service.GroupRepository via an embedded nil
// interface; only ListActive + GetByID are overridden (the methods the universal
// resolver / GetAvailableGroups path touches). Any other method call panics,
// which is the intended "unexpected call" signal in tests.
type universalGroupRepoStub struct {
	service.GroupRepository
	active []service.Group
}

func (s *universalGroupRepoStub) ListActive(_ context.Context) ([]service.Group, error) {
	return s.active, nil
}

func (s *universalGroupRepoStub) GetByID(_ context.Context, id int64) (*service.Group, error) {
	for i := range s.active {
		if s.active[i].ID == id {
			g := s.active[i]
			return &g, nil
		}
	}
	return nil, service.ErrGroupNotFound
}

// TestAuthMiddleware_UniversalKeySwapsBackingGroupEndToEnd drives a universal key
// through the REAL NewAPIKeyAuthMiddleware and asserts the resolver runs, swaps to
// the entitled backing group, passes the post-swap group checks, and reaches the
// handler — i.e. the §2 seam is wired end-to-end (not just unit-tested in isolation).
// Uses a standard (balance) group with positive balance to keep it robust; the
// subscription-type swap is asserted at the resolver level in
// TestMaybeResolveUniversal_SwapsToSubscriptionGroupForBilling.
func TestAuthMiddleware_UniversalKeySwapsBackingGroupEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)

	openaiGroup := service.Group{ID: 20, Name: "uni-openai", Status: service.StatusActive, Platform: service.PlatformOpenAI, SubscriptionType: service.SubscriptionTypeStandard, Hydrated: true}
	user := &service.User{ID: 7, Role: service.RoleUser, Status: service.StatusActive, Balance: 100, Concurrency: 3}
	apiKey := &service.APIKey{ID: 100, UserID: user.ID, Key: "uni-key", Status: service.StatusActive, RoutingMode: service.RoutingModeUniversal, User: user}

	apiKeyRepo := &stubApiKeyRepo{getByKey: func(_ context.Context, key string) (*service.APIKey, error) {
		if key != apiKey.Key {
			return nil, service.ErrAPIKeyNotFound
		}
		clone := *apiKey
		cu := *user
		clone.User = &cu
		return &clone, nil
	}}
	userRepo := &stubUserRepo{getByID: func(_ context.Context, id int64) (*service.User, error) {
		if id != user.ID {
			return nil, service.ErrUserNotFound
		}
		cu := *user
		return &cu, nil
	}}
	groupRepo := &universalGroupRepoStub{active: []service.Group{openaiGroup}}
	subRepo := &stubUserSubscriptionRepo{} // no subscriptions; standard/balance path

	cfg := &config.Config{RunMode: config.RunModeStandard}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, userRepo, groupRepo, subRepo, nil, nil, cfg)
	subscriptionService := service.NewSubscriptionService(groupRepo, subRepo, nil, nil, cfg)

	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, subscriptionService, cfg)))
	var seenGroupID int64
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		if k, ok := GetAPIKeyFromContext(c); ok && k.GroupID != nil {
			seenGroupID = *k.GroupID
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5"}`))
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("universal key should be authorized end-to-end, got %d body=%s", w.Code, w.Body.String())
	}
	if seenGroupID != openaiGroup.ID {
		t.Fatalf("auth middleware should swap universal key to backing group %d, handler saw %d", openaiGroup.ID, seenGroupID)
	}
}

func newTestCtx(method, path, body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	c.Request = httptest.NewRequest(method, path, r)
	return c, w
}

func TestPeekModelFromJSONBody_RestoresBody(t *testing.T) {
	const payload = `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`
	c, _ := newTestCtx(http.MethodPost, "/v1/chat/completions", payload)

	if got := peekModelFromJSONBody(c); got != "gpt-5" {
		t.Fatalf("peek model = %q want gpt-5", got)
	}
	// The downstream handler must still read the identical body.
	rest, err := io.ReadAll(c.Request.Body)
	if err != nil {
		t.Fatalf("re-read body err: %v", err)
	}
	if string(rest) != payload {
		t.Fatalf("body not restored: %q", string(rest))
	}
}

func TestPeekModelFromJSONBody_RestoresCompressedBody(t *testing.T) {
	const payload = `{"model":"grok-4","messages":[{"role":"user","content":"hi"}]}`
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write([]byte(payload))
	_ = w.Close()
	compressed := gz.Bytes()

	c, _ := newTestCtx(http.MethodPost, "/v1/chat/completions", "")
	c.Request.Body = io.NopCloser(bytes.NewReader(compressed))
	c.Request.Header.Set("Content-Encoding", "gzip")

	// Peek decodes a COPY to read the model, but must restore the ORIGINAL compressed bytes
	// + headers untouched, so the downstream handler decodes identically.
	if got := peekModelFromJSONBody(c); got != "grok-4" {
		t.Fatalf("peek model from gzip body = %q want grok-4", got)
	}
	rest, _ := io.ReadAll(c.Request.Body)
	if !bytes.Equal(rest, compressed) {
		t.Fatalf("compressed body not restored byte-for-byte")
	}
	if c.Request.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding header must be left intact for the handler")
	}
}

func TestGeminiModelFromAction(t *testing.T) {
	cases := map[string]string{
		"gemini-3-pro:generateContent":        "gemini-3-pro",
		"/gemini-3-pro:streamGenerateContent": "gemini-3-pro",
		"gemini-2.5-flash":                    "gemini-2.5-flash",
	}
	for in, want := range cases {
		if got := geminiModelFromAction(in); got != want {
			t.Errorf("geminiModelFromAction(%q)=%q want %q", in, got, want)
		}
	}
}

func TestMaybeResolveUniversal_DirectKeyUnchanged(t *testing.T) {
	c, _ := newTestCtx(http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5"}`)
	resolver := service.NewUniversalRoutingResolver(&stubSpanLister{groups: []service.Group{activeGroup(20, service.PlatformOpenAI)}})
	apiKey := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeDirect}

	if handled := MaybeResolveUniversal(c, apiKey, resolver); handled {
		t.Fatalf("direct key should not be handled by universal resolver")
	}
	if apiKey.GroupID != nil || apiKey.Group != nil {
		t.Fatalf("direct key must be left untouched")
	}
}

func TestMaybeResolveUniversal_SwapsBackingGroup(t *testing.T) {
	c, _ := newTestCtx(http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5"}`)
	resolver := service.NewUniversalRoutingResolver(&stubSpanLister{groups: []service.Group{
		activeGroup(10, service.PlatformAnthropic),
		activeGroup(20, service.PlatformOpenAI),
	}})
	apiKey := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeUniversal}

	if handled := MaybeResolveUniversal(c, apiKey, resolver); handled {
		t.Fatalf("successful resolve should return handled=false (continue auth)")
	}
	if apiKey.GroupID == nil || *apiKey.GroupID != 20 || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformOpenAI {
		t.Fatalf("expected swap to openai group 20, got groupID=%v group=%v", apiKey.GroupID, apiKey.Group)
	}
	// Body must still be readable by the handler.
	rest, _ := io.ReadAll(c.Request.Body)
	if !strings.Contains(string(rest), `"model":"gpt-5"`) {
		t.Fatalf("body not restored after swap: %q", string(rest))
	}
}

// Regression guard for the correctness rule (design §2): when a universal key
// resolves to a SUBSCRIPTION backing group, the swapped apiKey.Group must report
// IsSubscriptionType()==true — that is exactly what the downstream
// `isSubscriptionType := apiKey.Group != nil && apiKey.Group.IsSubscriptionType()`
// billing decision reads, so subscription customers are billed under their plan
// (not silently in balance mode). The resolver runs BEFORE that decision (pinned
// by the api_key_auth.go sentinel).
func TestMaybeResolveUniversal_SwapsToSubscriptionGroupForBilling(t *testing.T) {
	c, _ := newTestCtx(http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5"}`)
	subGroup := activeGroup(20, service.PlatformOpenAI)
	subGroup.SubscriptionType = service.SubscriptionTypeSubscription
	resolver := service.NewUniversalRoutingResolver(&stubSpanLister{groups: []service.Group{subGroup}})
	apiKey := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeUniversal}

	if handled := MaybeResolveUniversal(c, apiKey, resolver); handled {
		t.Fatalf("resolve should succeed")
	}
	if apiKey.Group == nil || !apiKey.Group.IsSubscriptionType() {
		t.Fatalf("swapped group must be subscription-type so downstream bills under the plan; got %+v", apiKey.Group)
	}
}

// R-002 regression: a span-load/internal failure must surface as 500 (retryable),
// NOT a 403 "no platform in your plan" (which would mislabel a server error as an
// entitlement problem).
func TestMaybeResolveUniversal_InternalErrorIs500Not403(t *testing.T) {
	c, w := newTestCtx(http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5"}`)
	resolver := service.NewUniversalRoutingResolver(&stubSpanLister{err: errors.New("database unavailable")})
	apiKey := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeUniversal}

	if handled := MaybeResolveUniversal(c, apiKey, resolver); !handled {
		t.Fatalf("internal error should be handled (aborted)")
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("span-load failure should be 500, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "universal_no_entitled_group") {
		t.Fatalf("internal error must not be mislabeled as no-entitled-group: %s", w.Body.String())
	}
}

func TestMaybeResolveUniversal_SkipEndpointNoSwap(t *testing.T) {
	c, _ := newTestCtx(http.MethodGet, "/v1/models", "")
	resolver := service.NewUniversalRoutingResolver(&stubSpanLister{groups: []service.Group{activeGroup(20, service.PlatformOpenAI)}})
	apiKey := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeUniversal}

	if handled := MaybeResolveUniversal(c, apiKey, resolver); handled {
		t.Fatalf("metadata endpoint should not be handled")
	}
	if apiKey.GroupID != nil {
		t.Fatalf("metadata endpoint must not swap a group")
	}
}

func TestMaybeResolveUniversal_NoEntitledGroupAborts(t *testing.T) {
	c, w := newTestCtx(http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5"}`)
	// user only entitled to anthropic → an openai chat request cannot be served.
	resolver := service.NewUniversalRoutingResolver(&stubSpanLister{groups: []service.Group{activeGroup(10, service.PlatformAnthropic)}})
	apiKey := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeUniversal}

	if handled := MaybeResolveUniversal(c, apiKey, resolver); !handled {
		t.Fatalf("unentitled request should be handled (aborted)")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid_request_error") {
		t.Fatalf("expected openai-shaped error, got %s", w.Body.String())
	}
}
