//go:build unit

package handler

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeminiImagesWriterBuffersAndConverts(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w := &geminiImagesWriter{ResponseWriter: c.Writer, headers: make(http.Header), status: 200}
	w.WriteHeader(200)
	_, err := w.WriteString(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"inlineData":{"mimeType":"image/jpeg","data":"aW1hZ2U="}}]}}]}`)
	require.NoError(t, err)
	w.Flush()
	require.Empty(t, rec.Body.String(), "native response must not escape during Flush")
	writeGeminiImagesResponse(c, w)
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "aW1hZ2U=", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
	require.NotContains(t, rec.Body.String(), "candidates")
}
func TestGeminiImagesErrorsPreserveStatusAndRetryAfter(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w := &geminiImagesWriter{ResponseWriter: c.Writer, headers: make(http.Header), status: 429}
	w.Header().Set("Retry-After", "10")
	_, err := w.WriteString(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"rate limited"}}`)
	require.NoError(t, err)
	writeGeminiImagesResponse(c, w)
	require.Equal(t, 429, rec.Code)
	require.Equal(t, "10", rec.Header().Get("Retry-After"))
	require.Equal(t, "rate_limit_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Equal(t, "rate limited", gjson.Get(rec.Body.String(), "error.message").String())
}
func TestGeminiImagesRejectsInvalidBeforeNativeHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(`{"model":"nano-2","prompt":"cup","n":2}`))
	(&GatewayHandler{}).GeminiImageGenerations(c)
	require.Equal(t, 400, rec.Code)
	require.Contains(t, rec.Body.String(), "n=1")
}
func TestGeminiImagesResponseFailure(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		w := &geminiImagesWriter{ResponseWriter: c.Writer, headers: make(http.Header), status: 200, overflow: overflow}
		_, err := w.WriteString(`{"candidates":[]}`)
		require.NoError(t, err)
		writeGeminiImagesResponse(c, w)
		require.Equal(t, 502, rec.Code)
	}
}

func TestGeminiImagesWriterEnforcesBufferLimit(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w := &geminiImagesWriter{ResponseWriter: c.Writer, headers: make(http.Header), status: 200}
	n, err := w.Write(bytes.Repeat([]byte("x"), maxGeminiImagesResponseBytes+1))
	require.ErrorIs(t, err, io.ErrShortBuffer)
	require.Zero(t, n)
	require.Zero(t, w.body.Len())
	require.Empty(t, rec.Body.String())
	writeGeminiImagesResponse(c, w)
	require.Equal(t, 502, rec.Code)
}
