package protocolrouter

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatToMessagesToolsAndCachePlan(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5","messages":[{"role":"system","content":[{"type":"text","text":"instructions","cache_control":{"type":"ephemeral","ttl":"1h"}}]},{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"auto"}`)
	req, err := ParseCanonicalRequest(ProtocolChatCompletions, ResponsesPathNone, "claude-fable-5", true, body)
	require.NoError(t, err)
	plan, err := New(allTestAdapters()).Plan(req, testAccount(t, ProtocolMessages))
	require.NoError(t, err)
	require.Equal(t, AdapterChatToMessages, plan.AdapterID())
	require.Equal(t, ProtocolMessages, plan.TargetProtocol())
}

func TestChatToMessagesRejectsUnsupportedSemantics(t *testing.T) {
	for name, extra := range map[string]string{
		"strict tool":       `"tools":[{"type":"function","function":{"name":"lookup","strict":true}}]`,
		"hosted tool":       `"tools":[{"type":"web_search"}]`,
		"named undeclared":  `"tool_choice":{"type":"function","function":{"name":"missing"}}`,
		"structured output": `"response_format":{"type":"json_schema"}`,
		"reasoning":         `"reasoning_effort":"high"`,
		"prompt cache key":  `"prompt_cache_key":"session"`,
		"continuation":      `"previous_response_id":"resp_1"`,
		"legacy function":   `"functions":[{"name":"lookup"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"model":"claude-fable-5","messages":[{"role":"user","content":"hello"}],` + extra + `}`)
			req, err := ParseCanonicalRequest(ProtocolChatCompletions, ResponsesPathNone, "claude-fable-5", true, body)
			require.NoError(t, err)
			_, err = New(allTestAdapters()).Plan(req, testAccount(t, ProtocolMessages))
			require.ErrorIs(t, err, ErrNoLegalRoute)
		})
	}
	for name, messages := range map[string]string{
		"orphan result":     `[{"role":"tool","tool_call_id":"call_missing","content":"result"}]`,
		"invalid arguments": `[{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"lookup","arguments":"invalid"}}]}]`,
		"missing result":    `[{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"lookup","arguments":"{}"}}]}]`,
		"image":             `[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}]`,
		"invalid cache":     `[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral","ttl":"2h"}}]}]`,
		"conflicting cache": `[{"role":"user","cache_control":{"type":"ephemeral","ttl":"5m"},"content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"model":"claude-fable-5","tools":[{"type":"function","function":{"name":"lookup"}}],"messages":` + messages + `}`)
			req, err := ParseCanonicalRequest(ProtocolChatCompletions, ResponsesPathNone, "claude-fable-5", true, body)
			require.NoError(t, err)
			_, err = New(allTestAdapters()).Plan(req, testAccount(t, ProtocolMessages))
			require.ErrorIs(t, err, ErrNoLegalRoute)
		})
	}
	parts := make([]map[string]any, 5)
	for i := range parts {
		parts[i] = map[string]any{"type": "text", "text": "cached", "cache_control": map[string]any{"type": "ephemeral"}}
	}
	body, err := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "user", "content": parts}}})
	require.NoError(t, err)
	req, err := ParseCanonicalRequest(ProtocolChatCompletions, ResponsesPathNone, "claude-fable-5", true, body)
	require.NoError(t, err)
	_, err = New(allTestAdapters()).Plan(req, testAccount(t, ProtocolMessages))
	require.ErrorIs(t, err, ErrNoLegalRoute, "must not admit cache breakpoints the gateway would trim")
}
