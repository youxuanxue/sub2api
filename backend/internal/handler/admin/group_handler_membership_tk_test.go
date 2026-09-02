//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type membershipAdminServiceStub struct {
	stubAdminService
	bindFn   func(groupID int64, accountIDs []int64, skipMixed bool) error
	unbindFn func(groupID int64, accountIDs []int64) error
	listFn   func(groupID int64, page, pageSize int, status, search string, channelType int) ([]service.Account, int64, error)
}

func (s *membershipAdminServiceStub) BindGroupAccounts(_ context.Context, groupID int64, accountIDs []int64, skipMixed bool) error {
	if s.bindFn != nil {
		return s.bindFn(groupID, accountIDs, skipMixed)
	}
	return nil
}

func (s *membershipAdminServiceStub) UnbindGroupAccounts(_ context.Context, groupID int64, accountIDs []int64) error {
	if s.unbindFn != nil {
		return s.unbindFn(groupID, accountIDs)
	}
	return nil
}

func (s *membershipAdminServiceStub) ListGroupAccounts(_ context.Context, groupID int64, page, pageSize int, status, search string, channelType int) ([]service.Account, int64, error) {
	if s.listFn != nil {
		return s.listFn(groupID, page, pageSize, status, search, channelType)
	}
	return nil, 0, nil
}

func TestGroupHandler_BindAccounts_RejectsPlatformMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &membershipAdminServiceStub{
		bindFn: func(groupID int64, accountIDs []int64, skip bool) error {
			require.Equal(t, int64(7), groupID)
			require.Equal(t, []int64{3}, accountIDs)
			require.False(t, skip)
			return infraerrors.BadRequest("GROUP_ACCOUNT_PLATFORM_MISMATCH", "account platform mismatch")
		},
	}
	h := NewGroupHandler(svc, nil, nil)
	r := gin.New()
	r.POST("/groups/:id/accounts", h.BindAccounts)

	body, _ := json.Marshal(map[string]any{"account_ids": []int64{3}})
	req := httptest.NewRequest(http.MethodPost, "/groups/7/accounts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGroupHandler_UnbindAccounts_OK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	called := false
	svc := &membershipAdminServiceStub{
		unbindFn: func(groupID int64, accountIDs []int64) error {
			called = true
			require.Equal(t, int64(7), groupID)
			require.Equal(t, []int64{3, 4}, accountIDs)
			return nil
		},
	}
	h := NewGroupHandler(svc, nil, nil)
	r := gin.New()
	r.DELETE("/groups/:id/accounts", h.UnbindAccounts)

	body, _ := json.Marshal(map[string]any{"account_ids": []int64{3, 4}})
	req := httptest.NewRequest(http.MethodDelete, "/groups/7/accounts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, called)
}

func TestGroupHandler_ListAccounts_OK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &membershipAdminServiceStub{
		listFn: func(groupID int64, page, pageSize int, status, search string, channelType int) ([]service.Account, int64, error) {
			require.Equal(t, int64(7), groupID)
			require.Equal(t, 1, page)
			return []service.Account{{ID: 3, Name: "a", Platform: service.PlatformOpenAI, Status: service.StatusActive}}, 1, nil
		},
	}
	h := NewGroupHandler(svc, nil, nil)
	r := gin.New()
	r.GET("/groups/:id/accounts", h.ListAccounts)

	req := httptest.NewRequest(http.MethodGet, "/groups/7/accounts?page=1&page_size=20", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"id":3`)
}

func TestGroupHandler_ListAccounts_RedactsManagedExtra(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &membershipAdminServiceStub{
		listFn: func(groupID int64, page, pageSize int, status, search string, channelType int) ([]service.Account, int64, error) {
			return []service.Account{{
				ID:       3,
				Name:     "ollama",
				Platform: service.PlatformOpenAI,
				Status:   service.StatusActive,
				Extra: map[string]any{
					service.OllamaCloudUsageSessionExtraKey: "ciphertext-secret",
					"ordinary":                              "kept",
					"mixed_scheduling":                      true,
				},
			}}, 1, nil
		},
	}
	h := NewGroupHandler(svc, nil, nil)
	r := gin.New()
	r.GET("/groups/:id/accounts", h.ListAccounts)

	req := httptest.NewRequest(http.MethodGet, "/groups/7/accounts", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	require.NotContains(t, body, "ciphertext-secret")
	require.NotContains(t, body, service.OllamaCloudUsageSessionExtraKey)
	require.Contains(t, body, `"ordinary":"kept"`)
	require.Contains(t, body, `"mixed_scheduling":true`)
}
