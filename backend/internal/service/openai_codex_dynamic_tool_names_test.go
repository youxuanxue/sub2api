//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexDynamicTools_LongNameSharesDeclarationHistoryAndReverseMap(t *testing.T) {
	name := "mcp__" + strings.Repeat("long_server_", 8) + "handback"
	raw, _ := json.Marshal(map[string]any{
		"tools":       []any{map[string]any{"type": "function", "name": "base"}},
		"tool_choice": map[string]any{"type": "function", "name": name},
		"input": []any{
			map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{map[string]any{"type": "function", "name": name}}},
			map[string]any{"type": "function_call", "name": name, "call_id": "call_1", "arguments": "{}"},
		},
	})
	out, reverse, changed, err := aliasOpenAIOAuthReservedToolNamesBody(raw)
	require.NoError(t, err)
	require.True(t, changed)
	short := gjson.GetBytes(out, "input.0.tools.0.name").String()
	require.Len(t, short, 64)
	require.Equal(t, short, gjson.GetBytes(out, "input.1.name").String())
	require.Equal(t, short, gjson.GetBytes(out, "tool_choice.name").String())
	var transformed map[string]any
	require.NoError(t, json.Unmarshal(out, &transformed))
	require.False(t, normalizeCodexToolChoice(transformed))
	require.Equal(t, short, transformed["tool_choice"].(map[string]any)["name"])
	require.Equal(t, name, reverse[short])
	response, _ := json.Marshal(map[string]any{"output": []any{map[string]any{"type": "function_call", "name": short}}})
	restored := restoreCodexToolNamesInJSON(response, reverse)
	require.Equal(t, name, gjson.GetBytes(restored, "output.0.name").String())
	require.Equal(t, "base", gjson.GetBytes(out, "tools.0.name").String())
	require.Equal(t, short, aliasOpenAIOAuthReservedToolName(name))
	require.NotEqual(t, short, aliasOpenAIOAuthReservedToolName(name+"different"))
}

func TestCodexDynamicTools_RejectsShortNameCollisionBeforeMutation(t *testing.T) {
	name := strings.Repeat("long_name", 10)
	short := aliasOpenAIOAuthReservedToolName(name)
	payload := map[string]any{"tools": []any{map[string]any{"type": "function", "name": short}}, "input": []any{map[string]any{"type": "additional_tools", "tools": []any{map[string]any{"type": "function", "name": name}}}}}
	before, _ := json.Marshal(payload)
	_, changed, err := aliasOpenAIOAuthReservedToolNames(payload)
	require.ErrorContains(t, err, "both normalize")
	require.False(t, changed)
	after, _ := json.Marshal(payload)
	require.JSONEq(t, string(before), string(after))
}

func TestCodexDynamicTools_PreservesHistoryAcrossSessionPreparation(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	s := &OpenAIGatewayService{}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"messages_compaction_enabled": true, "messages_compaction_input_tokens_threshold": 1}}
	req := &apicompat.AnthropicRequest{Model: "gpt-5", Messages: []apicompat.AnthropicMessage{{Role: "system", Content: []byte(`[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"late"}}}]`)}}}
	for i := 0; i < 20; i++ {
		req.Messages = append(req.Messages, apicompat.AnthropicMessage{Role: "user", Content: []byte(`"message"`)})
	}
	s.bindOpenAICompatSessionResponseID(context.Background(), c, account, "cache-key", "resp_previous")
	prep := s.tkPrepareOpenAICompatMessagesSession(context.Background(), c, account, nil, req, req, "gpt-5", "cache-key", 0)
	require.Empty(t, prep.previousResponseID)
	require.False(t, prep.compatContinuationEnabled)
	require.False(t, prep.compatReplayTrimmed)
	require.Len(t, req.Messages, 21)
	require.Equal(t, "cache-key", prep.promptCacheKey)
}
