package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUS054_MachineKeyManagementAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newTestSettingRepo()
	settings := service.NewSettingService(repo, &config.Config{})
	h := NewSettingHandler(settings, nil, nil, nil, nil, nil, nil)
	method := service.AuditAuthMethodJWT
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("auth_method", method)
		c.Set(string(middleware.ContextKeyUserRole), service.RoleAdmin)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 19})
	})
	keys := router.Group("/keys", middleware.RequireHumanAdmin())
	keys.GET("", h.ListMachineAdminKeys)
	keys.POST("", gin.HandlerFunc(middleware.NewStepUpAuthMiddleware(nil, nil, settings)), h.CreateMachineAdminKey)
	keys.DELETE("/:id", h.RevokeMachineAdminKey)
	request := func(verb, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(verb, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	body := `{"name":"inventory","scopes":["accounts:read"],"ttl_hours":24,"owner_user_id":1}`
	w := request("POST", "/keys", body)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var created struct {
		Data struct {
			Credential service.MachineAdminKey `json:"credential"`
			Key        string                  `json:"key"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.Equal(t, int64(19), created.Data.Credential.OwnerUserID, "caller cannot select a different issuing admin")
	_, err := settings.AuthenticateMachineAdminKey(context.Background(), created.Data.Key)
	require.NoError(t, err)
	w = request("GET", "/keys", "")
	require.Equal(t, 200, w.Code)
	require.NotContains(t, w.Body.String(), created.Data.Key)
	require.NotContains(t, w.Body.String(), "digest")
	require.Contains(t, w.Body.String(), "permissions")
	for _, auth := range []string{service.AuditAuthMethodAdminAPIKey, service.AuditAuthMethodMachineAdminKey} {
		method = auth
		require.Equal(t, 403, request("GET", "/keys", "").Code)
		require.Equal(t, 403, request("POST", "/keys", body).Code)
		require.Equal(t, 403, request("DELETE", "/keys/"+created.Data.Credential.ID, "").Code)
	}
	method = service.AuditAuthMethodJWT
	require.Equal(t, 400, request("POST", "/keys", `{"name":"invalid","scopes":["*"],"ttl_hours":1}`).Code)
	require.Equal(t, 200, request("DELETE", "/keys/"+created.Data.Credential.ID, "").Code)
	_, err = settings.AuthenticateMachineAdminKey(context.Background(), created.Data.Key)
	require.ErrorIs(t, err, service.ErrInvalidMachineAdminKey)
}
