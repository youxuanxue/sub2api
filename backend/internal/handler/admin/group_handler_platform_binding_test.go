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

// TestCreateGroupRequest_AcceptsNewAPIPlatform is the regression guard for the
// audit P0 finding: before fix, the Gin binding tag
// `oneof=anthropic openai gemini antigravity` rejected `platform: "newapi"`,
// so the admin HTTP API silently could not create a fifth-platform group even
// though the service layer would have accepted it. This bound newapi groups
// to UI-only creation paths and broke any operator/script using the API.
func TestCreateGroupRequest_AcceptsNewAPIPlatform(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/groups", func(c *gin.Context) {
		var req CreateGroupRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"platform": req.Platform})
	})

	body, _ := json.Marshal(map[string]any{
		"name":     "newapi-prod",
		"platform": "newapi",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	require.Equalf(t, http.StatusOK, rec.Code, "newapi must be a valid platform value; body=%s", rec.Body.String())
	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "newapi", resp["platform"])
}

// TestCreateGroupRequest_RejectsUnknownPlatform is the negative guard: the
// `oneof` enum must still reject typos / unknown platforms (otherwise we'd
// have widened to no-op validation).
func TestCreateGroupRequest_RejectsUnknownPlatform(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/groups", func(c *gin.Context) {
		var req CreateGroupRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, nil)
	})

	body, _ := json.Marshal(map[string]any{
		"name":     "broken",
		"platform": "newpi", // typo
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestCreateGroupRequest_AcceptsKiroPlatform is the regression guard for the
// same bug class as newapi above, re-introduced by the sixth platform (#481):
// `platform: "kiro"` accounts could be created but `oneof=... newapi` rejected
// `platform: "kiro"` group creation, so kiro accounts had no schedulable group
// (anthropic groups only schedule {anthropic, antigravity}). The scheduler and
// frontend GATEWAY_PLATFORMS already supported kiro; only these two DTO enums
// lagged.
func TestCreateGroupRequest_AcceptsKiroPlatform(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/groups", func(c *gin.Context) {
		var req CreateGroupRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"platform": req.Platform})
	})

	body, _ := json.Marshal(map[string]any{
		"name":     "kiro-prod",
		"platform": "kiro",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	require.Equalf(t, http.StatusOK, rec.Code, "kiro must be a valid platform value; body=%s", rec.Body.String())
	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "kiro", resp["platform"])
}

// TestUpdateGroupRequest_AcceptsKiroPlatform mirrors the create-side kiro guard
// for the update path; both DTOs had the same gap pre-fix.
func TestUpdateGroupRequest_AcceptsKiroPlatform(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/groups/:id", func(c *gin.Context) {
		var req UpdateGroupRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"platform": req.Platform})
	})

	body, _ := json.Marshal(map[string]any{
		"platform": "kiro",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/groups/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	require.Equalf(t, http.StatusOK, rec.Code, "kiro must be a valid platform value on update; body=%s", rec.Body.String())
}

// TestUpdateGroupRequest_AcceptsNewAPIPlatform mirrors the create-side guard
// for the update path; both DTOs had the same bug pre-fix.
func TestUpdateGroupRequest_AcceptsNewAPIPlatform(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/groups/:id", func(c *gin.Context) {
		var req UpdateGroupRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"platform": req.Platform})
	})

	body, _ := json.Marshal(map[string]any{
		"platform": "newapi",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/groups/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	require.Equalf(t, http.StatusOK, rec.Code, "newapi must be a valid platform value on update; body=%s", rec.Body.String())
}
