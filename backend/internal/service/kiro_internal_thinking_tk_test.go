//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestKiroInternalThinkingBlockJSON_OmitsSyntheticSignature(t *testing.T) {
	block := kiroInternalThinkingBlockJSON("plain only", "")
	require.Contains(t, block, `"thinking":"plain only"`)
	require.NotContains(t, block, `"signature"`)
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

func TestPublishKiroInternalThinkingSideChannel_CapturesLocally(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	captureKiroInternalThinking(c, "internal reasoning", "SIG")

	require.Empty(t, rec.Body.String())
	require.Empty(t, rec.Header())

	raw, ok := c.Get(kiroInternalThinkingGinKey)
	require.True(t, ok)
	blocks, ok := raw.([]string)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], "internal reasoning")
	require.Contains(t, blocks[0], "SIG")
}
