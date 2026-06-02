//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type edgeAccountsReaderStub struct {
	accounts     []service.Account
	err          error
	lastPlatform string
}

func (s *edgeAccountsReaderStub) ListByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	s.lastPlatform = platform
	return s.accounts, s.err
}

func performEdgeAccountsRequest(t *testing.T, h *EdgeAccountsHandler, query string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/edge/accounts"+query, nil)
	h.ListAccounts(c)
	return w
}

// richAccount returns an account populated with BOTH sensitive credentials and
// the non-sensitive fields the DTO is allowed to expose, so the leak assertion
// is meaningful.
func richAccount() service.Account {
	mult := 1.5
	tierID := int64(7)
	expires := time.Now().Add(24 * time.Hour)
	return service.Account{
		ID:       42,
		Name:     "edge-acct-1",
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeAPIKey,
		Status:   service.StatusActive,
		Credentials: map[string]any{
			"api_key":       "sk-super-secret-key",
			"access_token":  "at-secret",
			"refresh_token": "rt-secret",
			"base_url":      "https://api-us1.tokenkey.dev",
		},
		Extra: map[string]any{
			"window_cost_limit": 50.0,
			"max_sessions":      30,
			"base_rpm":          28,
		},
		Concurrency:    8,
		Priority:       3,
		RateMultiplier: &mult,
		Schedulable:    true,
		ExpiresAt:      &expires,
		CreatedAt:      time.Now(),
		TierID:         &tierID,
		Groups:         []*service.Group{{ID: 1, Name: "default"}, {ID: 2, Name: "vip"}},
	}
}

func TestEdgeAccountsHandler_ReturnsSanitizedAccounts(t *testing.T) {
	stub := &edgeAccountsReaderStub{accounts: []service.Account{richAccount()}}
	h := NewEdgeAccountsHandler(stub)
	w := performEdgeAccountsRequest(t, h, "?platform=anthropic")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, service.PlatformAnthropic, stub.lastPlatform)

	var env struct {
		Data edgeAccountsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, "anthropic", env.Data.Platform)
	require.NotZero(t, env.Data.TS)
	require.Len(t, env.Data.Accounts, 1)

	got := env.Data.Accounts[0]
	require.Equal(t, int64(42), got.ID)
	require.Equal(t, "edge-acct-1", got.Name)
	require.Equal(t, 8, got.Concurrency)
	require.Equal(t, 1.5, got.RateMultiplier)
	require.Equal(t, 50.0, got.WindowCostLimit)
	require.Equal(t, 30, got.MaxSessions)
	require.Equal(t, 28, got.BaseRPM)
	require.Equal(t, []string{"default", "vip"}, got.Groups)
	require.NotNil(t, got.TierID)
	require.Equal(t, int64(7), *got.TierID)
}

// TestEdgeAccountsHandler_NeverLeaksCredentials is the load-bearing security
// assertion: the raw response bytes must not contain ANY credential value or key.
func TestEdgeAccountsHandler_NeverLeaksCredentials(t *testing.T) {
	stub := &edgeAccountsReaderStub{accounts: []service.Account{richAccount()}}
	h := NewEdgeAccountsHandler(stub)
	w := performEdgeAccountsRequest(t, h, "?platform=anthropic")
	require.Equal(t, http.StatusOK, w.Code)

	body := w.Body.String()
	for _, forbidden := range []string{
		"sk-super-secret-key", "at-secret", "rt-secret", // credential values
		"api_key", "access_token", "refresh_token", "base_url", // credential keys
		"credentials", // the map itself must not appear
	} {
		require.NotContainsf(t, body, forbidden,
			"response leaked credential-related token %q: %s", forbidden, body)
	}
}

func TestEdgeAccountsHandler_RejectsUnknownPlatform(t *testing.T) {
	h := NewEdgeAccountsHandler(&edgeAccountsReaderStub{})
	w := performEdgeAccountsRequest(t, h, "?platform=bogus")
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestEdgeAccountsHandler_DefaultsToAnthropic(t *testing.T) {
	stub := &edgeAccountsReaderStub{}
	h := NewEdgeAccountsHandler(stub)
	w := performEdgeAccountsRequest(t, h, "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, service.PlatformAnthropic, stub.lastPlatform)
}

func TestEdgeAccountsHandler_ListError(t *testing.T) {
	h := NewEdgeAccountsHandler(&edgeAccountsReaderStub{err: errors.New("db down")})
	w := performEdgeAccountsRequest(t, h, "?platform=anthropic")
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestEdgeAccountsHandler_NilReader(t *testing.T) {
	h := NewEdgeAccountsHandler(nil)
	w := performEdgeAccountsRequest(t, h, "?platform=anthropic")
	require.Equal(t, http.StatusInternalServerError, w.Code)
	// sanity: the body should not look like a normal success envelope
	require.False(t, strings.Contains(w.Body.String(), `"accounts"`))
}
