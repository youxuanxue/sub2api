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

// US-028 — Bug B-11 verification.
//
// TkTryWriteNewAPIRelayErrorJSON used to write c.JSON(nre.Err.StatusCode, ...)
// without validating StatusCode. Bridge errors built via certain ErrOption
// combos can carry StatusCode == 0, which would emit HTTP 200 + a JSON error
// body — clients then mis-interpret the failure as success. Fix: coerce
// out-of-range status to 502.
//
// See docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md § B-11.

func us028NewTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	// gin.CreateTestContext does not set c.Request; the renderer (c.JSON)
	// happily writes without it, but we still need a non-nil Request to
	// avoid surprises if the helper later reads Method/Header.
	c.Request, _ = http.NewRequest(http.MethodPost, "/test", nil)
	return c, w
}

func TestUS028_TkTryWriteNewAPIRelayErrorJSON_ZeroStatusCoercedTo502(t *testing.T) {
	c, w := us028NewTestContext()

	// Construct a NewAPIError with StatusCode=0 (regression case).
	apiErr := newapitypes.NewError(errors.New("bridge transient failure"), newapitypes.ErrorCodeBadResponse)
	apiErr.StatusCode = 0
	wrapped := &service.NewAPIRelayError{Err: apiErr}

	// gin.ResponseWriter returns Size() == -1 before any write. The handler
	// captures this as writerSizeBeforeForward; pass the same value here.
	got := TkTryWriteNewAPIRelayErrorJSON(c, wrapped, false, c.Writer.Size())

	require.True(t, got, "must return true so caller stops")
	require.Equal(t, 502, w.Code, "status 0 must be coerced to 502")
}

func TestUS028_TkTryWriteNewAPIRelayErrorJSON_PreservesValid4xx(t *testing.T) {
	c, w := us028NewTestContext()

	apiErr := newapitypes.NewErrorWithStatusCode(
		errors.New("upstream rejected"),
		newapitypes.ErrorCodeAccessDenied,
		401,
	)
	wrapped := &service.NewAPIRelayError{Err: apiErr}

	got := TkTryWriteNewAPIRelayErrorJSON(c, wrapped, false, c.Writer.Size())

	require.True(t, got)
	require.Equal(t, 401, w.Code, "valid 4xx must be preserved verbatim")
}

func TestUS028_TkTryWriteNewAPIRelayErrorJSON_PreservesValid5xx(t *testing.T) {
	c, w := us028NewTestContext()

	apiErr := newapitypes.NewErrorWithStatusCode(
		errors.New("upstream 500"),
		newapitypes.ErrorCodeBadResponse,
		503,
	)
	wrapped := &service.NewAPIRelayError{Err: apiErr}

	got := TkTryWriteNewAPIRelayErrorJSON(c, wrapped, false, c.Writer.Size())

	require.True(t, got)
	require.Equal(t, 503, w.Code, "valid 5xx must be preserved verbatim")
}

func TestUS028_TkTryWriteNewAPIRelayErrorJSON_StreamStartedReturnsTrueWithoutWrite(t *testing.T) {
	c, w := us028NewTestContext()

	apiErr := newapitypes.NewErrorWithStatusCode(
		errors.New("stream failed midway"),
		newapitypes.ErrorCodeBadResponse,
		500,
	)
	wrapped := &service.NewAPIRelayError{Err: apiErr}

	// streamStarted=true means the response body has already been streamed,
	// so the helper must not write a second JSON response. It still returns
	// true so the caller knows the error was a NewAPIRelayError.
	got := TkTryWriteNewAPIRelayErrorJSON(c, wrapped, true, c.Writer.Size())

	require.True(t, got, "must still report it was a relay error so caller stops")
	require.Equal(t, 200, w.Code, "stream-started case must NOT write a body (default 200 from recorder)")
	require.Empty(t, w.Body.String())
}

func TestUS028_TkTryWriteNewAPIRelayErrorJSON_NonRelayErrorReturnsFalse(t *testing.T) {
	c, w := us028NewTestContext()

	got := TkTryWriteNewAPIRelayErrorJSON(c, errors.New("not a relay error"), false, c.Writer.Size())

	require.False(t, got)
	require.Equal(t, 200, w.Code, "non-relay error must not write anything")
	require.Empty(t, w.Body.String())
}

func TestUS028_TkTryWriteNewAPIRelayErrorJSON_TooLargeStatusCoercedTo502(t *testing.T) {
	c, w := us028NewTestContext()

	apiErr := newapitypes.NewErrorWithStatusCode(
		errors.New("garbled status"),
		newapitypes.ErrorCodeBadResponse,
		999,
	)
	wrapped := &service.NewAPIRelayError{Err: apiErr}

	got := TkTryWriteNewAPIRelayErrorJSON(c, wrapped, false, c.Writer.Size())

	require.True(t, got)
	require.Equal(t, 502, w.Code, "out-of-range status must be coerced to 502")
}
