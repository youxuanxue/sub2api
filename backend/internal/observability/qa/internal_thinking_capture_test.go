//go:build unit

package qa

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func TestCaptureEncryptedReasoning_PreservesCiphertext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("ops_openai_encrypted_reasoning", []string{
		`{"item_id":"rs_1","encrypted_content":"gAAAA-REAL-CIPHER"}`,
	})

	got := captureEncryptedReasoning(c)
	require.Len(t, got, 1)
	require.Contains(t, got[0], `"item_id":"rs_1"`)
	require.Contains(t, got[0], "gAAAA-REAL-CIPHER")
	require.NotContains(t, got[0], "***")
}

func TestBuildBlob_EncryptedReasoningField(t *testing.T) {
	svc := &Service{bodyMaxBytes: 256 * 1024}
	input := CaptureInput{
		RequestID:              "req-enc-1",
		InboundEndpoint:        "/v1/responses",
		StatusCode:             200,
		CreatedAt:              time.Now().UTC(),
		RequestBody:            []byte(`{"model":"gpt-5.4-mini"}`),
		ResponseBody:           []byte(`{"id":"resp_1"}`),
		EncryptedReasoningJSON: []string{`{"item_id":"rs_1","encrypted_content":"gAAAA-QA"}`},
	}

	compressed, _, _, _, err := svc.buildBlob(input)
	require.NoError(t, err)

	dec, err := zstd.NewReader(nil)
	require.NoError(t, err)
	defer dec.Close()
	raw, err := dec.DecodeAll(compressed, nil)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	resp, ok := payload["response"].(map[string]any)
	require.True(t, ok)
	blocks, ok := resp["encrypted_reasoning"].([]any)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], "gAAAA-QA")
	require.Contains(t, blocks[0], `"item_id":"rs_1"`)
}

func TestCaptureInternalThinkingBlocks_KiroIndependentOfClientWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("ops_kiro_internal_thinking_blocks", []string{
		`{"type":"thinking","thinking":"qa-only reasoning","signature":"REAL_SIG_123"}`,
	})

	got := captureInternalThinkingBlocks(c)
	require.Len(t, got, 1)
	require.Contains(t, got[0], `"type":"thinking"`)
	require.Contains(t, got[0], "qa-only reasoning")
	require.Contains(t, got[0], "REAL_SIG_123")
	require.NotContains(t, got[0], `"signature":"***"`)
}
