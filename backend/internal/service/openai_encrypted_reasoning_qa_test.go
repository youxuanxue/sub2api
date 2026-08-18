package service

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestStashOpenAIEncryptedReasoningFromSSE_OutputItemDone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	stashOpenAIEncryptedReasoningFromSSE(c, []byte(`{"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","encrypted_content":"gAAAA-ITEM"}}`))
	stashOpenAIEncryptedReasoningFromSSE(c, []byte(`{"type":"response.output_text.delta","delta":"hi"}`))
	stashOpenAIEncryptedReasoningFromSSE(c, []byte(`{"type":"response.completed","response":{"output":[{"id":"rs_1","type":"reasoning","encrypted_content":"gAAAA-ITEM"},{"id":"rs_2","type":"reasoning","encrypted_content":"gAAAA-DONE"}]}}`))

	got, ok := c.Get(openaiEncryptedReasoningGinKey)
	require.True(t, ok)
	blocks, ok := got.([]string)
	require.True(t, ok)
	require.Len(t, blocks, 2)
	require.Contains(t, blocks[0], `"item_id":"rs_1"`)
	require.Contains(t, blocks[0], "gAAAA-ITEM")
	require.Contains(t, blocks[1], `"item_id":"rs_2"`)
	require.Contains(t, blocks[1], "gAAAA-DONE")
}

func TestStashOpenAIEncryptedReasoningFromSSE_IgnoresEmptyAndNonReasoning(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	stashOpenAIEncryptedReasoningFromSSE(c, []byte(`{"type":"response.output_item.done","item":{"id":"msg_1","type":"message","encrypted_content":""}}`))
	stashOpenAIEncryptedReasoningFromSSE(c, nil)
	_, ok := c.Get(openaiEncryptedReasoningGinKey)
	require.False(t, ok)
}
