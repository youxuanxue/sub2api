//go:build unit

package middleware

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type machineAuthSettings struct{ bmSettingRepo }

func (r *machineAuthSettings) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}
func (r *machineAuthSettings) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

type machineAuthUsers struct {
	stubUserRepo
	admin *service.User
}

func (r *machineAuthUsers) GetFirstAdmin(context.Context) (*service.User, error) { return r.admin, nil }

func TestUS054_MachineAuthScopeAndAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	settingsRepo := &machineAuthSettings{bmSettingRepo{values: map[string]string{service.SettingKeyAdminAPIKey: "legacy-test-key"}}}
	settings := service.NewSettingService(settingsRepo, &config.Config{})
	owner := &service.User{ID: 17, Role: service.RoleAdmin, Status: service.StatusActive, Email: "owner@example.test"}
	usersRepo := &machineAuthUsers{admin: owner, stubUserRepo: stubUserRepo{getByID: func(_ context.Context, id int64) (*service.User, error) {
		require.Equal(t, owner.ID, id)
		return owner, nil
	}}}
	users := service.NewUserService(usersRepo, nil, nil, nil)
	meta, key, err := settings.CreateMachineAdminKey(ctx, owner.ID, "inventory", []string{"accounts:read"}, 1)
	require.NoError(t, err)
	auditRepo := &auditCaptureRepository{}
	audit := service.NewAuditLogService(auditRepo, nil)
	audit.Start()
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAdminAuthMiddleware(nil, users, settings, audit)))
	router.Use(gin.HandlerFunc(NewAuditLogMiddleware(audit)))
	endpoint := func(c *gin.Context) { c.JSON(200, gin.H{"ok": true, "method": c.GetString("auth_method")}) }
	router.GET("/api/v1/admin/accounts", endpoint)
	router.GET("/api/v1/admin/accounts/data", endpoint)
	router.POST("/api/v1/admin/accounts", endpoint)
	router.POST("/api/v1/admin/settings/machine-admin-keys", endpoint)
	router.PUT("/api/v1/admin/payment/config", endpoint)
	router.GET("/api/v1/admin/new-unreviewed-route", endpoint)
	request := func(method, path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(`{"api_key":"body-canary"}`))
		req.Header.Set("x-api-key", token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	w := request("GET", "/api/v1/admin/accounts", key)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), service.AuditAuthMethodMachineAdminKey)
	for _, route := range []string{"GET /api/v1/admin/accounts/data", "POST /api/v1/admin/accounts", "POST /api/v1/admin/settings/machine-admin-keys", "PUT /api/v1/admin/payment/config", "GET /api/v1/admin/new-unreviewed-route"} {
		method, path, _ := strings.Cut(route, " ")
		w := request(method, path, key)
		require.Equal(t, 403, w.Code, route)
		require.Contains(t, w.Body.String(), "MACHINE_ADMIN_SCOPE_REQUIRED")
	}
	audit.Stop()
	auditRepo.mu.Lock()
	logs := append([]*service.AuditLog(nil), auditRepo.logs...)
	auditRepo.mu.Unlock()
	require.Len(t, logs, 6)
	for _, entry := range logs {
		require.Equal(t, meta.ID, entry.Extra["machine_key_id"])
		require.Equal(t, owner.ID, *entry.ActorUserID)
		require.Equal(t, service.AuditAuthMethodMachineAdminKey, entry.AuthMethod)
		encoded, err := json.Marshal(entry)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), key)
		require.NotContains(t, string(encoded), "body-canary")
	}
	// Legacy key and its behavior are unchanged during the migration.
	require.Equal(t, 200, request("GET", "/api/v1/admin/accounts", "legacy-test-key").Code)
	owner.Status = "disabled"
	require.Equal(t, 401, request("GET", "/api/v1/admin/accounts", key).Code)
	owner.Status = service.StatusActive
	owner.Role = service.RoleUser
	require.Equal(t, 401, request("GET", "/api/v1/admin/accounts", key).Code)
	owner.Role = service.RoleAdmin
	require.NoError(t, settings.RevokeMachineAdminKey(ctx, meta.ID))
	require.Equal(t, 401, request("GET", "/api/v1/admin/accounts", key).Code)
	// A malformed machine-prefixed key never falls back to legacy auth.
	settingsRepo.values[service.SettingKeyAdminAPIKey] = key
	require.Equal(t, 401, request("GET", "/api/v1/admin/accounts", key).Code)
}

func TestUS054_MachineStepUpIsExportOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, enabled := range []bool{false, true} {
		for _, tc := range []struct {
			path   string
			scopes []string
			want   int
		}{
			{"/api/v1/admin/accounts/data", []string{"accounts:export", "proxies:export"}, 200},
			{"/api/v1/admin/accounts/data", []string{"accounts:export"}, 403},
			{"/api/v1/admin/accounts/data", []string{"accounts:read", "proxies:read"}, 403},
			{"/api/v1/admin/proxies/data", []string{"proxies:export"}, 200},
			{"/api/v1/admin/settings", []string{"accounts:export", "proxies:export"}, 403},
		} {
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set("auth_method", service.AuditAuthMethodMachineAdminKey)
				c.Set(machineAdminContextKey, &service.MachineAdminKey{Scopes: tc.scopes})
			})
			router.GET(tc.path, stepUpAuth(nil, nil, stubStepUpSettingReader{enabled: enabled}), func(c *gin.Context) { c.Status(200) })
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			require.Equal(t, tc.want, w.Code, "enabled=%v path=%s", enabled, tc.path)
		}
	}
	// Even a legacy key with all implicit rights cannot mint scoped credentials.
	for _, method := range []string{service.AuditAuthMethodAdminAPIKey, service.AuditAuthMethodMachineAdminKey, service.AuditAuthMethodJWT, ""} {
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set("auth_method", method)
			c.Set(string(ContextKeyUserRole), service.RoleAdmin)
		})
		router.POST("/keys", RequireHumanAdmin(), func(c *gin.Context) { c.Status(200) })
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/keys", nil))
		want := 403
		if method == service.AuditAuthMethodJWT {
			want = 200
		}
		require.Equal(t, want, w.Code)
	}
}
