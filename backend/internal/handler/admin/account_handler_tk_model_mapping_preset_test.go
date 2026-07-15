package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func setupModelMappingPresetsRouter(adminSvc service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewAccountHandler(
		adminSvc,
		nil, nil, nil, nil,
		nil, nil, nil, nil, nil,
		nil, nil, nil, nil,
	)
	router.GET("/api/v1/admin/accounts/model-mapping-presets", handler.GetModelMappingPresets)
	return router
}

func TestGetModelMappingPresets_Grok(t *testing.T) {
	router := setupModelMappingPresetsRouter(newStubAdminService())

	ids := requestModelMappingPresetIDs(t, router, "/api/v1/admin/accounts/model-mapping-presets?platform=grok")
	require.ElementsMatch(t,
		service.AccountModelMappingPresetIDs(context.Background(), service.PlatformGrok, 0, nil),
		ids,
	)
}

func TestGetModelMappingPresets_NewAPIVertex(t *testing.T) {
	router := setupModelMappingPresetsRouter(newStubAdminService())

	ids := requestModelMappingPresetIDs(t, router,
		"/api/v1/admin/accounts/model-mapping-presets?platform=newapi&channel_type="+strconv.Itoa(newapiconstant.ChannelTypeVertexAi),
	)
	require.ElementsMatch(t,
		service.AccountModelMappingPresetIDs(context.Background(), service.PlatformNewAPI, newapiconstant.ChannelTypeVertexAi, nil),
		ids,
	)
}

func TestGetModelMappingPresets_NewAPIDeepSeek(t *testing.T) {
	router := setupModelMappingPresetsRouter(newStubAdminService())

	ids := requestModelMappingPresetIDs(t, router,
		"/api/v1/admin/accounts/model-mapping-presets?platform=newapi&channel_type="+strconv.Itoa(newapiconstant.ChannelTypeDeepSeek),
	)
	require.ElementsMatch(t,
		service.AccountModelMappingPresetIDs(context.Background(), service.PlatformNewAPI, newapiconstant.ChannelTypeDeepSeek, nil),
		ids,
	)
}

func requestModelMappingPresetIDs(t *testing.T, router *gin.Engine, target string) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		target,
		nil,
	)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Data struct {
			ModelIDs []string `json:"model_ids"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp.Data.ModelIDs
}

func TestGetModelMappingPresets_InvalidPlatform(t *testing.T) {
	router := setupModelMappingPresetsRouter(newStubAdminService())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/model-mapping-presets?platform=unknown", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}
