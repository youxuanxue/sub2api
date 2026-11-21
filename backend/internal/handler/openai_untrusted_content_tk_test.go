package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

func TestCodexUntrustedContentAsUserText(t *testing.T) {
	for _, callID := range []any{nil, "", "  "} {
		item := pageContextItem("untrusted_content", "external context")
		if callID != nil {
			item["call_id"] = callID
		}
		body := pageContextRequest(t, item, map[string]any{"role": "user", "content": "continue"})
		got, changed := normalizeCodexUntrustedContext(body)
		require.True(t, changed)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		require.True(t, (&OpenAIGatewayHandler{}).validateFunctionCallOutputRequest(c, got, zap.NewNop()))
		require.Equal(t, "user", gjson.GetBytes(got, "input.0.role").String())
		require.Equal(t, "<external_untrusted_content>external context</external_untrusted_content>", gjson.GetBytes(got, "input.0.content.0.text").String())
		require.Equal(t, "continue", gjson.GetBytes(got, "input.1.content").String())
	}
}

func TestCodexUntrustedContentPreservesToolAndMalformedItems(t *testing.T) {
	for _, override := range []map[string]any{
		{"call_id": "call_real"}, {"call_id": nil}, {"call_id": 42},
		{"namespace": "other"}, {"output": []string{"context"}}, {"output": nil}, {"output": " "},
		{"name": "UNTRUSTED_CONTENT", "output": " "}, {"name": " untrusted_content ", "output": " "},
	} {
		item := pageContextItem("untrusted_content", "context")
		for k, v := range override {
			item[k] = v
		}
		body := pageContextRequest(t, item)
		got, changed := normalizeCodexUntrustedContext(body)
		require.False(t, changed, "override=%v", override)
		require.Equal(t, body, got)
	}
	body := []byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"untrusted_content","call_id":"real","call_id":"","output":"text"}]}`)
	got, changed := normalizeCodexUntrustedContext(body)
	require.False(t, changed)
	require.Equal(t, body, got)
}
