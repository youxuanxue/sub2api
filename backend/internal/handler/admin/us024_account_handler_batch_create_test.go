package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func setupAccountBatchCreateRouter(adminSvc *stubAdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	accountHandler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.POST("/api/v1/admin/accounts/batch", accountHandler.BatchCreate)
	return router
}

// US-024 AC-001 (正向): BatchCreate must forward ChannelType + LoadFactor for
// PlatformNewAPI. Before this fix the handler dropped both fields, so the
// downstream service-level validator ("channel_type must be > 0 for newapi
// platform") rejected every newapi row in batch mode — there was no path to
// import multiple newapi accounts via the admin API.
func TestUS024_BatchCreate_NewAPI_ForwardsChannelTypeAndLoadFactor(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountBatchCreateRouter(adminSvc)

	loadFactor := 200
	body, _ := json.Marshal(map[string]any{
		"accounts": []map[string]any{
			{
				"name":         "newapi-batch-1",
				"platform":     "newapi",
				"type":         "apikey",
				"channel_type": 14,
				"load_factor":  loadFactor,
				"credentials": map[string]any{
					"api_key":  "sk-test",
					"base_url": "https://upstream.example.com",
				},
			},
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "response body: %s", rec.Body.String())
	require.Len(t, adminSvc.createdAccounts, 1, "BatchCreate must reach CreateAccount once")

	got := adminSvc.createdAccounts[0]
	require.Equal(t, "newapi", got.Platform)
	require.Equal(t, 14, got.ChannelType, "channel_type must be forwarded into CreateAccountInput; was silently dropped before US-024")
	require.NotNil(t, got.LoadFactor, "load_factor must be forwarded into CreateAccountInput; was silently dropped before US-024")
	require.Equal(t, 200, *got.LoadFactor)
}

// US-024 AC-002 (负向): BatchCreate must reject newapi rows with channel_type==0
// at the handler layer (same UX as single-account Create), instead of letting
// the service layer return the lower-level "channel_type must be > 0" error.
// The error must surface in results[].error, not as a CreateAccount call.
func TestUS024_BatchCreate_NewAPI_RejectsZeroChannelType(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountBatchCreateRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"accounts": []map[string]any{
			{
				"name":     "newapi-batch-bad",
				"platform": "newapi",
				"type":     "apikey",
				"credentials": map[string]any{
					"api_key":  "sk-test",
					"base_url": "https://upstream.example.com",
				},
			},
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "batch endpoint returns 200 with per-row failures")
	require.Empty(t, adminSvc.createdAccounts, "validation must reject before reaching CreateAccount")

	var resp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Equal(t, float64(1), resp.Data["failed"])
	require.Equal(t, float64(0), resp.Data["success"])

	results, ok := resp.Data["results"].([]any)
	require.True(t, ok)
	require.Len(t, results, 1)
	row, ok := results[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, false, row["success"])
	require.Contains(t, row["error"], "channel_type")
}

// US-024 AC-003 (负向): BatchCreate must reject newapi rows with empty
// credentials.base_url at handler validation layer (mirrors single-Create UX).
func TestUS024_BatchCreate_NewAPI_RejectsMissingBaseURL(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountBatchCreateRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"accounts": []map[string]any{
			{
				"name":         "newapi-batch-no-base",
				"platform":     "newapi",
				"type":         "apikey",
				"channel_type": 14,
				"credentials": map[string]any{
					"api_key": "sk-test",
				},
			},
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, adminSvc.createdAccounts)

	var resp struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, float64(1), resp.Data["failed"])
	results, ok := resp.Data["results"].([]any)
	require.True(t, ok)
	row, ok := results[0].(map[string]any)
	require.True(t, ok)
	require.Contains(t, row["error"], "base_url")
}

// US-024 AC-004 (回归保护): non-newapi platforms (anthropic) must still pass
// through BatchCreate without channel_type. This protects the original anthropic
// batch-import flow from being collateral-damaged by the new validator.
func TestUS024_BatchCreate_AnthropicWithoutChannelType_StillPasses(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountBatchCreateRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"accounts": []map[string]any{
			{
				"name":     "anth-batch-1",
				"platform": "anthropic",
				"type":     "oauth",
				"credentials": map[string]any{
					"refresh_token": "rt-1",
				},
			},
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, adminSvc.createdAccounts, 1, "anthropic row must reach CreateAccount unchanged")
	require.Equal(t, "anthropic", adminSvc.createdAccounts[0].Platform)
}
