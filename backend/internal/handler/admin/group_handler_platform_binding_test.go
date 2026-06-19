package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine"
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

// TestCreateGroupRequest_AcceptsGrokPlatform is the regression guard for the
// same bug class as newapi/kiro above, re-introduced by the seventh platform
// (grok, #791): platform="grok" accounts could be created and the scheduler
// (engine.AllSchedulingPlatforms / IsOpenAICompatPoolMember) plus the frontend
// GATEWAY_PLATFORMS already supported grok groups, but `oneof=... newapi kiro`
// rejected `platform: "grok"` group creation, so grok accounts had no
// schedulable group. Only these two DTO enums lagged.
func TestCreateGroupRequest_AcceptsGrokPlatform(t *testing.T) {
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
		"name":     "grok-prod",
		"platform": "grok",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	require.Equalf(t, http.StatusOK, rec.Code, "grok must be a valid platform value; body=%s", rec.Body.String())
	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "grok", resp["platform"])
}

// TestUpdateGroupRequest_AcceptsGrokPlatform mirrors the create-side grok guard
// for the update path; both DTOs had the same gap pre-fix.
func TestUpdateGroupRequest_AcceptsGrokPlatform(t *testing.T) {
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
		"platform": "grok",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/groups/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	require.Equalf(t, http.StatusOK, rec.Code, "grok must be a valid platform value on update; body=%s", rec.Body.String())
}

// TestGroupRequest_PlatformEnumCoversAllSchedulingPlatforms is the drift guard
// that turns this thrice-recurring bug class (newapi, kiro #481, grok #791)
// into a mechanical check, per the "no soft rule without a check" discipline.
// engine.AllSchedulingPlatforms() is the single source of truth for "which
// platforms have a scheduling pool"; every one of them MUST be an accepted
// group Platform value on BOTH the create and update DTOs, otherwise that
// platform's accounts can be created but have no schedulable group. When the
// eighth platform is appended to AllSchedulingPlatforms() this test fails until
// both `oneof` binding tags in group_handler.go are updated — forcing the fix
// instead of silently shipping the gap a fourth time.
func TestGroupRequest_PlatformEnumCoversAllSchedulingPlatforms(t *testing.T) {
	gin.SetMode(gin.TestMode)

	createRouter := gin.New()
	createRouter.POST("/groups", func(c *gin.Context) {
		var req CreateGroupRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"platform": req.Platform})
	})
	updateRouter := gin.New()
	updateRouter.PUT("/groups/:id", func(c *gin.Context) {
		var req UpdateGroupRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"platform": req.Platform})
	})

	for _, platform := range engine.AllSchedulingPlatforms() {
		t.Run(platform, func(t *testing.T) {
			createBody, _ := json.Marshal(map[string]any{"name": platform + "-grp", "platform": platform})
			createRec := httptest.NewRecorder()
			createReq := httptest.NewRequest(http.MethodPost, "/groups", bytes.NewReader(createBody))
			createReq.Header.Set("Content-Type", "application/json")
			createRouter.ServeHTTP(createRec, createReq)
			require.Equalf(t, http.StatusOK, createRec.Code,
				"scheduling platform %q must be accepted by CreateGroupRequest.Platform oneof — add it to the group_handler.go binding tag; body=%s",
				platform, createRec.Body.String())

			updateBody, _ := json.Marshal(map[string]any{"platform": platform})
			updateRec := httptest.NewRecorder()
			updateReq := httptest.NewRequest(http.MethodPut, "/groups/1", bytes.NewReader(updateBody))
			updateReq.Header.Set("Content-Type", "application/json")
			updateRouter.ServeHTTP(updateRec, updateReq)
			require.Equalf(t, http.StatusOK, updateRec.Code,
				"scheduling platform %q must be accepted by UpdateGroupRequest.Platform oneof — add it to the group_handler.go binding tag; body=%s",
				platform, updateRec.Body.String())
		})
	}
}
