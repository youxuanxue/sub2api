package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
func (s *geminiWebStoreStub) ListByPlatform(context.Context, string) ([]service.Account, error) {
	return []service.Account{*s.account}, nil
}
func (s *geminiWebStoreStub) CompareAndSwapGeminiWebRuntime(_ context.Context, _ int64, expected int64, _ map[string]any) (bool, error) {
	s.expected = expected
	return s.updated, nil
}

func geminiWebHandlerFixture(updated bool) (*GeminiWebSessionHandler, *geminiWebStoreStub) {
	store := &geminiWebStoreStub{updated: updated, account: &service.Account{ID: 28, Platform: service.PlatformGemini,
		Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{
			"gemini_web": map[string]any{"runtime": map[string]any{"version": int64(7), "user_agent": "ua", "cookies": []any{}}},
		}}}
	return &GeminiWebSessionHandler{store: store, token: "a-very-long-worker-control-token-123"}, store
}

func TestGeminiWebSessionHandlerRuntimeIsScopedAndVersioned(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, store := geminiWebHandlerFixture(true)
	r := gin.New()
	r.GET("/internal/gemini-web/accounts/:id/session", h.Get)
	r.PUT("/internal/gemini-web/accounts/:id/runtime", h.PutRuntime)

	unauthorized := httptest.NewRecorder()
	r.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/internal/gemini-web/accounts/28/session", nil))
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)

	get := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/gemini-web/accounts/28/session", nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	r.ServeHTTP(get, req)
	require.Equal(t, http.StatusOK, get.Code)
	require.NotContains(t, get.Body.String(), "source_bundle")

	put := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/internal/gemini-web/accounts/28/runtime", strings.NewReader(`{"expected_version":7,"runtime":{"user_agent":"ua","cookies":[]}}`))
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(put, req)
	require.Equal(t, http.StatusOK, put.Code)
	require.Equal(t, int64(7), store.expected)
}
