//go:build unit

package qa

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCaptureInternalThinkingBlocks_KiroIndependentOfClientWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("ops_kiro_internal_thinking_blocks", []string{
		`{"type":"thinking","thinking":"qa-only reasoning"}`,
	})

	got := captureInternalThinkingBlocks(c)
	require.Len(t, got, 1)
	require.Contains(t, got[0], `"type":"thinking"`)
	require.Contains(t, got[0], "qa-only reasoning")
}
