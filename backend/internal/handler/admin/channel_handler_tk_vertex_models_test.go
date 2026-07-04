//go:build unit

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestListChannelTypeModels_VertexAIUsesTokenKeyServablePreset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewTKChannelAdminHandler(nil, nil, nil, nil)
	router := gin.New()
	router.GET("/channel-type-models", h.ListChannelTypeModels)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/channel-type-models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data map[string][]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	require.NotEmpty(t, resp.Data["41"])
	require.ElementsMatch(t, resp.Data["41"], service.VertexNewAPIChannelServableModelIDs())
	require.Contains(t, resp.Data["41"], "gemini-2.5-flash")
	require.Contains(t, resp.Data["41"], "imagen-4.0-fast-generate-001")
	require.Contains(t, resp.Data["41"], "veo-3.1-generate-001")
}

func TestListChannelTypeModels_ManifestChannelsUseTokenKeyPresets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewTKChannelAdminHandler(nil, newStubAdminService(), nil, nil)
	router := gin.New()
	router.GET("/channel-type-models", h.ListChannelTypeModels)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/channel-type-models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data map[string][]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	require.Contains(t, resp.Data["43"], "deepseek-chat")
	require.Contains(t, resp.Data["17"], "qwen3.7-max")
	require.Contains(t, resp.Data["26"], "glm-5-turbo")
	require.Contains(t, resp.Data["45"], "doubao-seed-2-0-pro-260215")
}

func TestFetchUpstreamModels_VertexAIDoesNotRequireAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewTKChannelAdminHandler(nil, nil, nil, nil)
	router := gin.New()
	router.POST("/fetch-upstream-models", h.FetchUpstreamModels)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/fetch-upstream-models",
		bytes.NewBufferString(`{"channel_type":41}`),
	)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data struct {
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Data.Models)
	ids := make(map[string]bool, len(resp.Data.Models))
	for _, m := range resp.Data.Models {
		ids[m.ID] = true
	}
	require.True(t, ids["gemini-2.5-flash"])
	require.True(t, ids["veo-3.1-generate-001"],
		"Vertex video joins the admin preset after the paid gate proves it is provisioned")
}
