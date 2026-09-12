package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAntigravityGeminiCollectPreservesEarlyToolsAndThinking(t *testing.T) {
	chunks := []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"echo","args":{"value":"OK"}},"thoughtSignature":"tool-signature"},{"text":"private thought","thought":true}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Done","thoughtSignature":"text-signature"},{"inlineData":{"mimeType":"image/png","data":"first"}}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"."},{"inlineData":{"mimeType":"image/png","data":"second"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7}}`,
	}
	var wire strings.Builder
	for _, chunk := range chunks {
		_, err := wire.WriteString("data: {\"response\":" + chunk + "}\n\n")
		require.NoError(t, err)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/test", nil)
	result, err := newAntigravityTestService(&config.Config{}).handleGeminiStreamToNonStreaming(c, &http.Response{Body: io.NopCloser(strings.NewReader(wire.String()))}, time.Now())
	require.NoError(t, err)
	require.Equal(t, 11, result.usage.InputTokens)
	require.Equal(t, "STOP", gjson.GetBytes(rec.Body.Bytes(), "candidates.0.finishReason").String())
	var output map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &output))
	parts := extractGeminiParts(output)
	require.Len(t, parts, 6)
	require.Equal(t, "tool-signature", parts[0]["thoughtSignature"])
	require.Equal(t, map[string]any{"name": "echo", "args": map[string]any{"value": "OK"}}, parts[0]["functionCall"])
	require.Equal(t, true, parts[1]["thought"])
	require.Equal(t, "private thought", parts[1]["text"])
	require.Equal(t, "Done", parts[2]["text"])
	require.Equal(t, "text-signature", parts[2]["thoughtSignature"])
	require.Equal(t, "first", gjson.GetBytes(rec.Body.Bytes(), "candidates.0.content.parts.3.inlineData.data").String())
	require.Equal(t, ".", parts[4]["text"])
	require.Equal(t, "second", gjson.GetBytes(rec.Body.Bytes(), "candidates.0.content.parts.5.inlineData.data").String())
}
