//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type apiKeyLookupStub struct {
	apiKey *service.APIKey
	err    error
}

func (s apiKeyLookupStub) GetByKey(_ context.Context, _ string) (*service.APIKey, error) {
	return s.apiKey, s.err
}

type userLookupStub struct {
	user *service.User
	err  error
}

func (s userLookupStub) GetByID(_ context.Context, _ int64) (*service.User, error) {
	return s.user, s.err
}

type sessionMinterStub struct {
	token string
	err   error
}

func (s sessionMinterStub) GenerateEdgeAdminSessionToken(_ *service.User, _ time.Duration) (string, error) {
	return s.token, s.err
}

func performAdminSessionRequest(t *testing.T, h *EdgeAdminSessionHandler, apiKey string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/edge/admin-session", nil)
	if apiKey != "" {
		c.Request.Header.Set("x-api-key", apiKey)
	}
	h.Mint(c)
	return w
}

func adminUser() *service.User {
	return &service.User{ID: 1, Email: "admin@edge", Role: service.RoleAdmin, Status: service.StatusActive}
}

func TestEdgeAdminSession_AdminKeyMintsToken(t *testing.T) {
	h := NewEdgeAdminSessionHandler(
		apiKeyLookupStub{apiKey: &service.APIKey{UserID: 1}},
		userLookupStub{user: adminUser()},
		sessionMinterStub{token: "minted.jwt.value"},
	)
	w := performAdminSessionRequest(t, h, "admin-key")
	require.Equal(t, http.StatusOK, w.Code)

	var env struct {
		Data edgeAdminSessionResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, "minted.jwt.value", env.Data.Token)
	require.Greater(t, env.Data.ExpiresIn, 0)
}

func TestEdgeAdminSession_NonAdminForbidden(t *testing.T) {
	nonAdmin := &service.User{ID: 2, Role: service.RoleUser, Status: service.StatusActive}
	h := NewEdgeAdminSessionHandler(
		apiKeyLookupStub{apiKey: &service.APIKey{UserID: 2}},
		userLookupStub{user: nonAdmin},
		sessionMinterStub{token: "should-not-be-minted"},
	)
	w := performAdminSessionRequest(t, h, "user-key")
	require.Equal(t, http.StatusForbidden, w.Code)
	require.NotContains(t, w.Body.String(), "should-not-be-minted")
}

func TestEdgeAdminSession_DisabledAdminForbidden(t *testing.T) {
	disabled := &service.User{ID: 1, Role: service.RoleAdmin, Status: service.StatusDisabled}
	h := NewEdgeAdminSessionHandler(
		apiKeyLookupStub{apiKey: &service.APIKey{UserID: 1}},
		userLookupStub{user: disabled},
		sessionMinterStub{token: "x"},
	)
	w := performAdminSessionRequest(t, h, "k")
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestEdgeAdminSession_MissingKeyUnauthorized(t *testing.T) {
	h := NewEdgeAdminSessionHandler(apiKeyLookupStub{}, userLookupStub{}, sessionMinterStub{})
	w := performAdminSessionRequest(t, h, "")
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestEdgeAdminSession_InvalidKeyUnauthorized(t *testing.T) {
	h := NewEdgeAdminSessionHandler(
		apiKeyLookupStub{err: errors.New("not found")},
		userLookupStub{user: adminUser()},
		sessionMinterStub{token: "x"},
	)
	w := performAdminSessionRequest(t, h, "bad")
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestEdgeAdminSession_MinterErrorIsInternal(t *testing.T) {
	h := NewEdgeAdminSessionHandler(
		apiKeyLookupStub{apiKey: &service.APIKey{UserID: 1}},
		userLookupStub{user: adminUser()},
		sessionMinterStub{err: errors.New("sign failed")},
	)
	w := performAdminSessionRequest(t, h, "k")
	require.Equal(t, http.StatusInternalServerError, w.Code)
}
