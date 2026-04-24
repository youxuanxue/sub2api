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
	"net/http/httptest"
	"strings"
	"testing"

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
