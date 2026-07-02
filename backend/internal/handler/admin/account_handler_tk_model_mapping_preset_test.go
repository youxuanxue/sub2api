package admin

import (
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

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/model-mapping-presets?platform=grok", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Data struct {
			ModelIDs []string `json:"model_ids"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Data.ModelIDs)
	require.Contains(t, resp.Data.ModelIDs, service.GrokDefaultTestModelID)
}

func TestGetModelMappingPresets_NewAPIVertex(t *testing.T) {
	router := setupModelMappingPresetsRouter(newStubAdminService())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/accounts/model-mapping-presets?platform=newapi&channel_type="+strconv.Itoa(newapiconstant.ChannelTypeVertexAi),
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
	require.Contains(t, resp.Data.ModelIDs, "gemini-2.5-flash")
}

func TestGetModelMappingPresets_InvalidPlatform(t *testing.T) {
	router := setupModelMappingPresetsRouter(newStubAdminService())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/model-mapping-presets?platform=unknown", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}
