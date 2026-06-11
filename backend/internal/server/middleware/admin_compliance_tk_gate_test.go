//go:build unit

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func tkComplianceGateRouter(svc *service.SettingService) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})
		c.Next()
	})
	router.Use(TkAdminComplianceGuardIfEnabled(svc))
	router.GET("/api/v1/admin/users", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	return router
}

func TestTkAdminComplianceGateDisabledByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// No tk_admin_compliance_gate_enabled setting and no acknowledgement:
	// the request must pass through (TK default-off override).
	svc := service.NewSettingService(&complianceGuardRepoStub{}, &config.Config{})
	router := tkComplianceGateRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/users", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "ok", w.Body.String())
}

func TestTkAdminComplianceGateEnabledRunsUpstreamGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &complianceGuardRepoStub{values: map[string]string{
		service.SettingKeyTkAdminComplianceGateEnabled: "true",
	}}
	svc := service.NewSettingService(repo, &config.Config{})
	router := tkComplianceGateRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/users", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusLocked, w.Code)
	require.Contains(t, w.Body.String(), "ADMIN_COMPLIANCE_ACK_REQUIRED")
}
