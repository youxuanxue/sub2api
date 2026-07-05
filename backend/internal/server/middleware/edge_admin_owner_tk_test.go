//go:build unit

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type edgeOwnerKeyStub struct {
	apiKey *service.APIKey
	err    error
}

func (s edgeOwnerKeyStub) GetByKey(_ context.Context, _ string) (*service.APIKey, error) {
	return s.apiKey, s.err
}

type edgeOwnerUserStub struct {
	user *service.User
	err  error
}

func (s edgeOwnerUserStub) GetByID(_ context.Context, _ int64) (*service.User, error) {
	return s.user, s.err
}

// runEdgeAdminOwner mounts the middleware on a throwaway gin engine with a terminal
// 200 handler and replays one request, returning the recorder. A 200 means the gate
// passed (c.Next reached the handler); any other code is the gate's abort.
func runEdgeAdminOwner(t *testing.T, keys edgeAdminOwnerKeyLookup, users edgeAdminOwnerUserLookup, apiKey string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(NewEdgeAdminOwnerMiddleware(keys, users))
	r.POST("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func edgeAdmin() *service.User {
	return &service.User{ID: 1, Role: service.RoleAdmin, Status: service.StatusActive}
}
func edgeNonAdmin() *service.User {
	return &service.User{ID: 2, Role: service.RoleUser, Status: service.StatusActive}
}

func TestEdgeAdminOwner_AdminKeyPasses(t *testing.T) {
	w := runEdgeAdminOwner(t,
		edgeOwnerKeyStub{apiKey: &service.APIKey{UserID: 1}},
		edgeOwnerUserStub{user: edgeAdmin()},
		"admin-key")
	require.Equal(t, http.StatusOK, w.Code)
}

func TestEdgeAdminOwner_NonAdminForbidden(t *testing.T) {
	w := runEdgeAdminOwner(t,
		edgeOwnerKeyStub{apiKey: &service.APIKey{UserID: 2}},
		edgeOwnerUserStub{user: edgeNonAdmin()},
		"user-key")
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestEdgeAdminOwner_DisabledAdminForbidden(t *testing.T) {
	disabled := &service.User{ID: 1, Role: service.RoleAdmin, Status: service.StatusDisabled}
	w := runEdgeAdminOwner(t,
		edgeOwnerKeyStub{apiKey: &service.APIKey{UserID: 1}},
		edgeOwnerUserStub{user: disabled},
		"k")
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestEdgeAdminOwner_MissingKeyUnauthorized(t *testing.T) {
	w := runEdgeAdminOwner(t, edgeOwnerKeyStub{}, edgeOwnerUserStub{}, "")
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestEdgeAdminOwner_InvalidKeyUnauthorized(t *testing.T) {
	w := runEdgeAdminOwner(t,
		edgeOwnerKeyStub{err: errors.New("not found")},
		edgeOwnerUserStub{user: edgeAdmin()},
		"bad")
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestEdgeAdminOwner_OwnerLookupFailureUnauthorized(t *testing.T) {
	w := runEdgeAdminOwner(t,
		edgeOwnerKeyStub{apiKey: &service.APIKey{UserID: 9}},
		edgeOwnerUserStub{err: errors.New("db down")},
		"k")
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestEdgeAdminOwner_NilDepsInternalError(t *testing.T) {
	w := runEdgeAdminOwner(t, nil, nil, "k")
	require.Equal(t, http.StatusInternalServerError, w.Code)
}
