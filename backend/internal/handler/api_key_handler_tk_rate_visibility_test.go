//go:build unit

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newGroupRatesRouter(t *testing.T, svc *service.APIKeyService, role string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewAPIKeyHandler(svc)
	r := gin.New()
	r.GET("/api/v1/groups/rates", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
		if role != "" {
			c.Set(string(middleware.ContextKeyUserRole), role)
		}
		h.GetUserGroupRates(c)
	})
	return r
}

// 非 admin（含 role 缺失）必须拿到空 map，且不触达 service —— 传入 nil
// service 仍 200 即证明短路生效。
func TestGetUserGroupRates_HiddenForNonAdmin(t *testing.T) {
	for _, role := range []string{"user", ""} {
		r := newGroupRatesRouter(t, nil, role)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/groups/rates", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "role=%q", role)

		var resp struct {
			Data map[int64]float64 `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Empty(t, resp.Data, "role=%q", role)
	}
}

// admin 走原路径（零值 service 的 userGroupRateRepo 为 nil → data 为 null），
// 关键断言是没有被非 admin 短路成空 map 之外的行为且不 panic。
func TestGetUserGroupRates_AdminReachesService(t *testing.T) {
	r := newGroupRatesRouter(t, &service.APIKeyService{}, "admin")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/groups/rates", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestTkHideUserRateValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		role string
		set  bool
		hide bool
	}{
		{role: "admin", set: true, hide: false},
		{role: "user", set: true, hide: true},
		{set: false, hide: true},
	}
	for _, tc := range cases {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		if tc.set {
			c.Set(string(middleware.ContextKeyUserRole), tc.role)
		}
		assert.Equal(t, tc.hide, tkHideUserRateValues(c), "role=%q set=%v", tc.role, tc.set)
	}
}
