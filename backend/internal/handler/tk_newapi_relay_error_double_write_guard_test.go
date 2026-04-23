//go:build unit

package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// US-031 — Bug B-2 verification.
//
// Anthropic bridge dispatch (openai_gateway_bridge_dispatch_tk_anthropic.go)
// writes a ClaudeError response body BEFORE returning *NewAPIRelayError to
// the handler. OpenAI / Embeddings / Images bridge dispatch paths, in
// contrast, do NOT write the body — they leave it to the handler's
// TkTryWriteNewAPIRelayErrorJSON call.
//
// The asymmetry is a soft contract: easy to break by reflexively wiring
// TkTryWriteNewAPIRelayErrorJSON into the Anthropic Messages handler
// "for consistency", which would emit a second OpenAI-shape body on top
// of the already-written ClaudeError, corrupting the response and emitting
// a gin write-after-write warning.
//
// Fix: TkTryWriteNewAPIRelayErrorJSON detects "response already written"
// (writer Size grew or stream started) and skips the second write while
// still returning true so the caller stops further forwarding logic.
//
// See docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md § B-2.

func us031NewTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/test", nil)
	return c, w
}

func TestUS031_TkWriteRelayErrorJSON_SkipsWriteWhenServiceLayerAlreadyWrote(t *testing.T) {
	c, w := us031NewTestContext()

	// Capture the size BEFORE the simulated service-layer write.
	sizeBefore := c.Writer.Size()

	// Simulate the Anthropic bridge dispatch having already written a
	// ClaudeError body to the response.
	c.JSON(http.StatusBadGateway, gin.H{
		"type":  "error",
		"error": gin.H{"type": "api_error", "message": "claude-shape error pre-written"},
	})
	require.Equal(t, http.StatusBadGateway, w.Code)
	preWrittenLen := w.Body.Len()
	require.Greater(t, preWrittenLen, 0, "service layer must have written something")

	// Now invoke the handler helper as if the OpenAI Messages handler reflexively
	// wired the same helper "for consistency". Without B-2 guard, this would
	// emit a second OpenAI-shape body on top.
	apiErr := newapitypes.NewErrorWithStatusCode(
		errors.New("relay failed"),
		newapitypes.ErrorCodeAccessDenied,
		http.StatusUnauthorized,
	)
	wrapped := &service.NewAPIRelayError{Err: apiErr}

	got := TkTryWriteNewAPIRelayErrorJSON(c, wrapped, false, sizeBefore)

	require.True(t, got, "must still report it was a relay error so caller stops")
	require.Equal(t, http.StatusBadGateway, w.Code,
		"status must remain at the service-layer write — guard must not allow override")
	require.Equal(t, preWrittenLen, w.Body.Len(),
		"body length must NOT grow — guard must skip the second write")
}

func TestUS031_TkWriteRelayErrorJSON_WritesWhenNothingPreWritten(t *testing.T) {
	// Regression: when service layer did NOT pre-write (current OpenAI /
	// embeddings / images bridge paths), the helper still serialises the
	// JSON error as before.
	c, w := us031NewTestContext()
	sizeBefore := c.Writer.Size()

	apiErr := newapitypes.NewErrorWithStatusCode(
		errors.New("relay failed"),
		newapitypes.ErrorCodeAccessDenied,
		http.StatusUnauthorized,
	)
	wrapped := &service.NewAPIRelayError{Err: apiErr}

	got := TkTryWriteNewAPIRelayErrorJSON(c, wrapped, false, sizeBefore)

	require.True(t, got)
	require.Equal(t, http.StatusUnauthorized, w.Code,
		"with no pre-write, the helper must serialise the error normally")
	require.Greater(t, w.Body.Len(), 0)
}
