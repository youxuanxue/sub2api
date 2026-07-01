//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestKiroInternalThinking_EncodeDecodeRoundTrip(t *testing.T) {
	blocks := kiroInternalThinkingBlocksFromPlaintext("reason step one")
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], `"type":"thinking"`)
	require.Contains(t, blocks[0], "reason step one")

	payload := encodeKiroInternalThinkingPayload(blocks)
	got := decodeKiroInternalThinkingPayload(payload)
	require.Equal(t, blocks, got)
}

func TestParseKiroInternalThinkingSSECommentLine(t *testing.T) {
	thinking := "streamed reasoning"
	payload := encodeKiroInternalThinkingPayload(kiroInternalThinkingBlocksFromPlaintext(thinking))
	line := kiroInternalThinkingSSECommentPfx + payload

	blocks, ok := parseKiroInternalThinkingSSECommentLine(line)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], thinking)

	_, bad := parseKiroInternalThinkingSSECommentLine(": unrelated comment")
	require.False(t, bad)
}

func TestStashKiroInternalThinkingBlocks_GinContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	stashKiroInternalThinkingBlocks(c, "first")
	stashKiroInternalThinkingBlocks(c, "second")

	raw, ok := c.Get(kiroInternalThinkingGinKey)
	require.True(t, ok)
	blocks, ok := raw.([]string)
	require.True(t, ok)
	require.Len(t, blocks, 2)
}

func TestWriteKiroInternalThinkingResponseHeader(t *testing.T) {
	hdr := httptest.NewRecorder().Header()
	writeKiroInternalThinkingResponseHeader(hdr, "non-stream reasoning")
	require.NotEmpty(t, hdr.Get(kiroInternalThinkingResponseHeader))

	got := kiroInternalThinkingBlocksFromUpstream(hdr)
	require.Len(t, got, 1)
	require.Contains(t, got[0], "non-stream reasoning")
}

func TestPublishKiroInternalThinkingSideChannel_DirectEdgeOmitsWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	publishKiroInternalThinkingSideChannel(c, rec, rec.Header(), "edge-only reasoning")

	require.NotContains(t, rec.Body.String(), kiroInternalThinkingSSECommentPfx)
	require.Empty(t, rec.Header().Get(kiroInternalThinkingResponseHeader))

	raw, ok := c.Get(kiroInternalThinkingGinKey)
	require.True(t, ok)
	blocks, ok := raw.([]string)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], "edge-only reasoning")
}

func TestPublishKiroInternalThinkingSideChannel_MirrorHopEmitsWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set(kiroInternalThinkingMirrorHopRequestHeader, "1")

	publishKiroInternalThinkingSideChannel(c, rec, rec.Header(), "mirror hop reasoning")

	require.Contains(t, rec.Body.String(), kiroInternalThinkingSSECommentPfx)
	require.NotEmpty(t, rec.Header().Get(kiroInternalThinkingResponseHeader))
}
