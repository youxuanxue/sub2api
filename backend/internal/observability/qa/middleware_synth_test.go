//go:build unit

package qa

// Issue #59 Gap 2: middleware-level capture of X-Synth-* headers.
// This test exercises the lightweight `captureSynthHeaders` extractor
// directly because the full Middleware path requires a wired APIKey
// auth subject (covered by the integration tests). Pinning the
// extractor here catches accidental rename/typo of the header names
// (the M0 client writes these literal names per
// docs/projects/auto-traj-from-supply-demand.md §6.1).

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUS059_CaptureSynthHeaders_AllPresent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	c.Request.Header.Set("X-Synth-Pipeline", "dual-cc-supply-demand")
	c.Request.Header.Set("X-Synth-Role", "user-simulator")
	c.Request.Header.Set("X-Synth-Session", "m0-1777017345-eedcaa")
	c.Request.Header.Set("X-Synth-Engineer-Level", "P6")

	session, role, level, dialogSynth := captureSynthHeaders(c)

	require.Equal(t, "m0-1777017345-eedcaa", session)
	require.Equal(t, "user-simulator", role)
	require.Equal(t, "P6", level)
	require.True(t, dialogSynth, "session id present ⇒ dialogSynth must be true")
}

func TestUS059_CaptureSynthHeaders_PipelineAloneFlipsDialogSynth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	c.Request.Header.Set("X-Synth-Pipeline", "dual-cc-supply-demand")

	session, _, _, dialogSynth := captureSynthHeaders(c)
	require.Empty(t, session)
	require.True(t, dialogSynth,
		"pipeline header alone (no session) is enough to mark the row as synth dialog — "+
			"matches the issue #59 contract that DialogSynth = (session OR pipeline)")
}

func TestUS059_CaptureSynthHeaders_AbsentReturnsEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)

	session, role, level, dialogSynth := captureSynthHeaders(c)

	require.Empty(t, session)
	require.Empty(t, role)
	require.Empty(t, level)
	require.False(t, dialogSynth, "no synth headers ⇒ row stays normal-traffic")
}

// Defense: cap header length to 256 to prevent an attacker from writing
// a 1MB synth_session_id row.
func TestUS059_CaptureSynthHeaders_BoundedLength(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	c.Request.Header.Set("X-Synth-Session", strings.Repeat("a", 1024))

	session, _, _, _ := captureSynthHeaders(c)
	require.Len(t, session, 256)
}

func TestUS070_MiddlewarePersistsUpstreamModelFromOpsContext(t *testing.T) {
	svc, client, _ := newQAExportTestService(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
			ID:     5,
			UserID: 7,
			User:   &service.User{ID: 7},
			Group:  &service.Group{Platform: service.PlatformAnthropic},
		})
		c.Set("ops_upstream_model", "claude-sonnet-4-5")
		c.Set("ops_input_tokens", 123)
		c.Set("ops_output_tokens", 45)
		c.Set("ops_cached_tokens", 6)
		c.Next()
	})
	r.Use(svc.Middleware())
	r.POST("/v1/messages", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "us070-upstream-model")
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	require.Eventually(t, func() bool {
		record, err := client.QARecord.Query().Only(req.Context())
		if err != nil {
			return false
		}
		return record.UpstreamModel != nil &&
			*record.UpstreamModel == "claude-sonnet-4-5" &&
			record.InputTokens == 123 &&
			record.OutputTokens == 45 &&
			record.CachedTokens == 6 &&
			record.Platform == "anthropic"
	}, 2*time.Second, 10*time.Millisecond)
}
