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
	blocks := kiroInternalThinkingBlocksFromCapture("reason step one", "UPSTREAM_SIG_abc")
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], `"type":"thinking"`)
	require.Contains(t, blocks[0], `"signature":"UPSTREAM_SIG_abc"`)
	require.Contains(t, blocks[0], "reason step one")

	payload := encodeKiroInternalThinkingPayload(blocks)
	got := decodeKiroInternalThinkingPayload(payload)
	require.Equal(t, blocks, got)
}

func TestKiroInternalThinkingBlockJSON_OmitsSyntheticSignature(t *testing.T) {
	block := kiroInternalThinkingBlockJSON("plain only", "")
	require.Contains(t, block, `"thinking":"plain only"`)
	require.NotContains(t, block, `"signature"`)
}

func TestParseKiroInternalThinkingSSECommentLine(t *testing.T) {
	thinking := "streamed reasoning"
	payload := encodeKiroInternalThinkingPayload(kiroInternalThinkingBlocksFromCapture(thinking, "SIG_LIVE"))
	line := kiroInternalThinkingSSECommentPfx + payload

	blocks, ok := parseKiroInternalThinkingSSECommentLine(line)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], thinking)
	require.Contains(t, blocks[0], "SIG_LIVE")

	_, bad := parseKiroInternalThinkingSSECommentLine(": unrelated comment")
	require.False(t, bad)
}

func TestStashKiroInternalThinkingBlocks_GinContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	stashKiroInternalThinkingBlocks(c, "first", "SIG1")
	stashKiroInternalThinkingBlocks(c, "second", "")

	raw, ok := c.Get(kiroInternalThinkingGinKey)
	require.True(t, ok)
	blocks, ok := raw.([]string)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], "first")
	require.Contains(t, blocks[0], "second")
	require.Contains(t, blocks[0], "SIG1")
}

func TestConsolidateKiroInternalThinkingBlocks_MergesSplitFrames(t *testing.T) {
	blocks := consolidateKiroInternalThinkingBlocks([]string{
		`{"type":"thinking","thinking":"long chain"}`,
		`{"type":"thinking","signature":"UPSTREAM_SIG"}`,
	})
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], "long chain")
	require.Contains(t, blocks[0], "UPSTREAM_SIG")
}

func TestWriteKiroInternalThinkingResponseHeader(t *testing.T) {
	hdr := httptest.NewRecorder().Header()
	writeKiroInternalThinkingResponseHeader(hdr, "non-stream reasoning", "SIG_HDR")
	require.NotEmpty(t, hdr.Get(kiroInternalThinkingResponseHeader))

	got := kiroInternalThinkingBlocksFromUpstream(hdr)
	require.Len(t, got, 1)
	require.Contains(t, got[0], "non-stream reasoning")
	require.Contains(t, got[0], "SIG_HDR")
}

func TestPublishKiroInternalThinkingSideChannel_DirectEdgeOmitsWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	publishKiroInternalThinkingSideChannel(c, rec, rec.Header(), "edge-only reasoning", "SIG_EDGE")

	require.NotContains(t, rec.Body.String(), kiroInternalThinkingSSECommentPfx)
	require.Empty(t, rec.Header().Get(kiroInternalThinkingResponseHeader))

	raw, ok := c.Get(kiroInternalThinkingGinKey)
	require.True(t, ok)
	blocks, ok := raw.([]string)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], "edge-only reasoning")
	require.Contains(t, blocks[0], "SIG_EDGE")
}

func TestPublishKiroInternalThinkingSideChannel_MirrorHopEmitsWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set(kiroInternalThinkingMirrorHopRequestHeader, "1")

	publishKiroInternalThinkingSideChannel(c, rec, rec.Header(), "mirror hop reasoning", "SIG_MIRROR")

	require.Contains(t, rec.Body.String(), kiroInternalThinkingSSECommentPfx)
	require.NotEmpty(t, rec.Header().Get(kiroInternalThinkingResponseHeader))
}

func TestSetKiroInternalThinkingMirrorHopHeaderForAccount_OAuthIgnored(t *testing.T) {
	hdr := http.Header{}
	account := &Account{
		Type: AccountTypeOAuth,
		Credentials: map[string]any{
			"base_url": "https://api-us6.tokenkey.dev",
		},
	}
	setKiroInternalThinkingMirrorHopHeaderForAccount(hdr, account)
	require.Empty(t, hdr.Get(kiroInternalThinkingMirrorHopRequestHeader))
}
