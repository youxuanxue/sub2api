package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type geminiWebStoreStub struct {
	account  *service.Account
	updated  bool
	expected int64
}

func (s *geminiWebStoreStub) GetByID(context.Context, int64) (*service.Account, error) {
	return s.account, nil
}
func (s *geminiWebStoreStub) ListDueGeminiWebAccounts(context.Context) ([]int64, error) {
	if s.account.Schedulable {
		return []int64{s.account.ID}, nil
	}
	return []int64{}, nil
}
func (s *geminiWebStoreStub) CompareAndSwapGeminiWebRuntime(_ context.Context, _ int64, expected int64, _ string, _ map[string]any) (bool, error) {
	s.expected = expected
	return s.updated, nil
}

func geminiWebHandlerFixture(updated bool) (*GeminiWebSessionHandler, *geminiWebStoreStub) {
	store := &geminiWebStoreStub{updated: updated, account: &service.Account{ID: 28, Platform: service.PlatformGemini,
		Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{
			"gemini_web": map[string]any{"runtime": map[string]any{"version": int64(7), "user_agent": "ua", "cookies": []any{}}},
		}}}
	return &GeminiWebSessionHandler{store: store}, store
}

func TestGeminiWebSessionHandlerRuntimeIsScopedAndVersioned(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, store := geminiWebHandlerFixture(true)
	r := gin.New()
	auth := &geminiWebAuthStub{role: service.RoleAdmin}
	r.Use(middleware.NewEdgeCapacityAuthMiddleware(auth), middleware.NewEdgeAdminOwnerMiddleware(auth, auth))
	r.GET("/edge/gemini-web/accounts/:id/session", h.Get)
	r.PUT("/edge/gemini-web/accounts/:id/runtime", h.PutRuntime)

	unauthorized := httptest.NewRecorder()
	r.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/edge/gemini-web/accounts/28/session", nil))
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)

	get := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/edge/gemini-web/accounts/28/session", nil)
	req.Header.Set("Authorization", "Bearer edge-key")
	r.ServeHTTP(get, req)
	require.Equal(t, http.StatusOK, get.Code)
	require.NotContains(t, get.Body.String(), "source_bundle")

	put := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/edge/gemini-web/accounts/28/runtime", strings.NewReader(`{"expected_version":7,"runtime":{"user_agent":"ua","cookies":[]}}`))
	req.Header.Set("Authorization", "Bearer edge-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gemini-Web-Lease", strings.Repeat("a", 32))
	r.ServeHTTP(put, req)
	require.Equal(t, http.StatusOK, put.Code)
	require.Equal(t, int64(7), store.expected)
}

func (s *geminiWebStoreStub) AcquireGeminiWebLease(context.Context, int64, string) (bool, error) {
	return s.updated, nil
}
func (s *geminiWebStoreStub) ReleaseGeminiWebLease(context.Context, int64, string) error { return nil }

type geminiWebAuthStub struct{ role string }

func (s *geminiWebAuthStub) GetByKey(_ context.Context, key string) (*service.APIKey, error) {
	if key != "edge-key" {
		return nil, service.ErrAPIKeyNotFound
	}
	return &service.APIKey{UserID: 1, Status: service.StatusActive}, nil
}
func (s *geminiWebAuthStub) GetByID(context.Context, int64) (*service.User, error) {
	return &service.User{ID: 1, Role: s.role, Status: service.StatusActive}, nil
}

func TestGeminiWebControlRejectsInvalidAccessAndConflicts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, store := geminiWebHandlerFixture(true)
	auth := &geminiWebAuthStub{role: service.RoleAdmin}
	r := gin.New()
	r.Use(middleware.NewEdgeCapacityAuthMiddleware(auth), middleware.NewEdgeAdminOwnerMiddleware(auth, auth))
	r.GET("/accounts/:id/session", h.Get)
	r.PUT("/accounts/:id/runtime", h.PutRuntime)
	r.POST("/accounts/:id/lease", h.Lease)
	r.GET("/warm-accounts", h.WarmAccounts)
	request := func(method, path, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Gemini-Web-Lease", strings.Repeat("a", 32))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for _, key := range []string{"", "invalid"} {
		require.Equal(t, 401, request(http.MethodGet, "/accounts/28/session", key, "").Code)
	}
	auth.role = service.RoleUser
	require.Equal(t, 403, request(http.MethodGet, "/accounts/28/session", "edge-key", "").Code)
	auth.role = service.RoleAdmin
	require.JSONEq(t, `{"accounts":[28],"protocol_version":1}`, request(http.MethodGet, "/warm-accounts", "edge-key", "").Body.String())
	store.updated = false
	body := `{"expected_version":7,"runtime":{"user_agent":"ua","cookies":[]}}`
	require.Equal(t, 409, request(http.MethodPut, "/accounts/28/runtime", "edge-key", body).Code)
	require.Equal(t, 409, request(http.MethodPost, "/accounts/28/lease", "edge-key", "").Code)
	require.Equal(t, 400, request(http.MethodGet, "/accounts/no/session", "edge-key", "").Code)
	store.account.Schedulable = false
	require.Equal(t, 404, request(http.MethodGet, "/accounts/28/session", "edge-key", "").Code)
	require.JSONEq(t, `{"accounts":[],"protocol_version":1}`, request(http.MethodGet, "/warm-accounts", "edge-key", "").Body.String())
	store.account.Schedulable = true
	delete(store.account.Credentials, "gemini_web")
	require.Equal(t, 404, request(http.MethodGet, "/accounts/28/session", "edge-key", "").Code)
}
