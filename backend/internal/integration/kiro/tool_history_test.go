//go:build unit

package kiro

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The real Kiro CLI 2.21.1 v1/v2 five-request capture retained every earlier
// tool pair: one failed read followed by three successful reads.
func TestClaudeToKiro_CLIToolHistory(t *testing.T) {
	req := &ClaudeRequest{Model: "claude-opus-5", Messages: []ClaudeMessage{{Role: "user", Content: "Read the fixtures"}}}
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("fixture-%d", i)
		req.Messages = append(req.Messages,
			ClaudeMessage{Role: "assistant", Content: []any{map[string]any{"type": "tool_use", "id": id, "name": "Read", "input": map[string]any{"file_path": id}}}},
			ClaudeMessage{Role: "user", Content: []any{map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": i == 0, "content": "result " + id}}},
		)
		payload := wireKiroPayload(t, ClaudeToKiro(req, false))
		assertKiroToolPairs(t, payload)
		var calls []KiroToolUse
		var results []KiroToolResult
		for _, msg := range payload.ConversationState.History {
			if msg.AssistantResponseMessage != nil {
				calls = append(calls, msg.AssistantResponseMessage.ToolUses...)
			}
			if msg.UserInputMessage != nil && msg.UserInputMessage.UserInputMessageContext != nil {
				results = append(results, msg.UserInputMessage.UserInputMessageContext.ToolResults...)
			}
		}
		results = append(results, payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.ToolResults...)
		require.Len(t, calls, i+1)
		require.Len(t, results, i+1)
		for j := range results {
			require.Equal(t, fmt.Sprintf("fixture-%d", j), calls[j].ToolUseID)
			require.Equal(t, calls[j].ToolUseID, results[j].ToolUseID)
			require.Equal(t, calls[j].ToolUseID, calls[j].Input["file_path"])
			want := "success"
			if j == 0 {
				want = "error"
			}
			require.Equal(t, want, results[j].Status)
			wantText := "result " + calls[j].ToolUseID
			if j == 0 {
				wantText = "[Tool execution failed]\n" + wantText
			}
			require.Equal(t, wantText, results[j].Content[0].Text)
		}
	}
	payload := ClaudeToKiro(req, false)
	payload.ClaudeCodeCompletionProtocol = true
	PrepareClaudeCodeCompletionContinuation(payload, "I received the results.")
	assertKiroToolPairs(t, wireKiroPayload(t, payload))
	var count int
	for _, msg := range payload.ConversationState.History {
		if msg.AssistantResponseMessage != nil {
			count += len(msg.AssistantResponseMessage.ToolUses)
		}
	}
	require.Equal(t, 4, count)
}

func TestClaudeToKiro_RepairsOnlyUnpairedResults(t *testing.T) {
	var req ClaudeRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"claude-opus-5","messages":[
		{"role":"user","content":"Inspect"},
		{"role":"assistant","content":[
			{"type":"tool_use","id":"ok","name":"Read","input":{}},
			{"type":"tool_use","id":"missing","name":"Read","input":{}}]},
		{"role":"user","content":[
			{"type":"text","text":"Keep this instruction"},
			{"type":"tool_result","tool_use_id":"ok","content":"actual result","is_error":true},
			{"type":"tool_result","tool_use_id":"orphan","content":"orphan result","is_error":true}]}]}`), &req))
	payload := wireKiroPayload(t, ClaudeToKiro(&req, false))
	assertKiroToolPairs(t, payload)
	current := payload.ConversationState.CurrentMessage.UserInputMessage
	require.Contains(t, current.Content, "Keep this instruction")
	require.Contains(t, current.Content, "orphan result")
	require.Contains(t, current.Content, "[Tool execution failed]")
	require.NotContains(t, current.Content, "actual result")
	results := current.UserInputMessageContext.ToolResults
	require.Len(t, results, 2)
	require.Equal(t, "error", results[0].Status)
	require.Equal(t, "[Tool execution failed]\nactual result", results[0].Content[0].Text)
	require.Equal(t, "missing", results[1].ToolUseID)
	require.Equal(t, "error", results[1].Status)
	require.Contains(t, results[1].Content[0].Text, "not confirmed")
}

func TestOpenAIToKiro_ParallelToolHistory(t *testing.T) {
	var req OpenAIRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"claude-opus-5","messages":[
		{"role":"user","content":"Inspect"},
		{"role":"assistant","tool_calls":[
			{"id":"a","type":"function","function":{"name":"Read","arguments":"{\"path\":\"a\"}"}},
			{"id":"b","type":"function","function":{"name":"Read","arguments":"{\"path\":\"b\"}"}}]},
		{"role":"tool","tool_call_id":"a","content":"same output"},
		{"role":"tool","tool_call_id":"b","content":"same output"},
		{"role":"assistant","content":"Received"},
		{"role":"user","content":"What happened?"}]}`), &req))
	payload := wireKiroPayload(t, OpenAIToKiro(&req, false))
	assertKiroToolPairs(t, payload)
	require.NotNil(t, payload.ConversationState.History[1].AssistantResponseMessage)
	require.Len(t, payload.ConversationState.History[1].AssistantResponseMessage.ToolUses, 2)
	require.NotNil(t, payload.ConversationState.History[2].UserInputMessage.UserInputMessageContext)
	require.Len(t, payload.ConversationState.History[2].UserInputMessage.UserInputMessageContext.ToolResults, 2)
}

func TestClaudeToKiro_ToolHistoryTruncation(t *testing.T) {
	for _, resultBytes := range []int{180 * 1024, 700 * 1024} {
		t.Run(fmt.Sprint(resultBytes), func(t *testing.T) {
			req := &ClaudeRequest{Model: "claude-opus-5", System: "Keep the fixtures", Messages: []ClaudeMessage{{Role: "user", Content: "Inspect"}}}
			for i := 0; i < 10; i++ {
				id := fmt.Sprintf("read-%d", i)
				req.Messages = append(req.Messages,
					ClaudeMessage{Role: "assistant", Content: []any{map[string]any{"type": "tool_use", "id": id, "name": "Read", "input": map[string]any{}}}},
					ClaudeMessage{Role: "user", Content: []any{map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": true, "content": strings.Repeat("x", resultBytes)}}},
				)
			}
			payload := wireKiroPayload(t, ClaudeToKiro(req, false))
			assertKiroToolPairs(t, payload)
			require.LessOrEqual(t, payloadByteSize(payload), maxPayloadBytes)
			current := payload.ConversationState.CurrentMessage.UserInputMessage
			require.Equal(t, "read-9", current.UserInputMessageContext.ToolResults[0].ToolUseID)
			require.Equal(t, "error", current.UserInputMessageContext.ToolResults[0].Status)
			require.Contains(t, payload.ConversationState.History[0].UserInputMessage.Content, "Keep the fixtures")
		})
	}
}

func wireKiroPayload(t *testing.T, payload *KiroPayload) *KiroPayload {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var got KiroPayload
	require.NoError(t, json.Unmarshal(raw, &got))
	return &got
}

func TestClaudeToKiro_SplitResultsAndToolNames(t *testing.T) {
	name := "mcp__fixture__read"
	req := &ClaudeRequest{Model: "claude-opus-5", Tools: []ClaudeTool{{Name: name, InputSchema: map[string]any{"type": "object"}}}, Messages: []ClaudeMessage{
		{Role: "user", Content: "Inspect"},
		{Role: "assistant", Content: []any{
			map[string]any{"type": "tool_use", "id": "a", "name": name, "input": map[string]any{}},
			map[string]any{"type": "tool_use", "id": "b", "name": name, "input": map[string]any{}},
		}},
		{Role: "user", Content: []any{map[string]any{"type": "tool_result", "tool_use_id": "a", "is_error": true, "content": "result a"}}},
		{Role: "user", Content: []any{map[string]any{"type": "tool_result", "tool_use_id": "b", "content": "result b"}}},
	}}
	payload := ClaudeToKiro(req, false)
	assertKiroToolPairs(t, payload)
	current := payload.ConversationState.CurrentMessage.UserInputMessage
	require.Len(t, current.UserInputMessageContext.Tools, 1)
	lastAssistant := payload.ConversationState.History[len(payload.ConversationState.History)-1].AssistantResponseMessage
	require.Len(t, lastAssistant.ToolUses, 2)
	callName := lastAssistant.ToolUses[0].Name
	require.Equal(t, current.UserInputMessageContext.Tools[0].ToolSpecification.Name, callName)
	require.Equal(t, name, payload.ToolNameMap[callName])
	before := wireKiroPayload(t, payload)
	normalizeKiroToolHistory(payload)
	require.Equal(t, before, wireKiroPayload(t, payload), "normalization must be idempotent")
}

func assertKiroToolPairs(t *testing.T, payload *KiroPayload) {
	t.Helper()
	messages := append(append([]KiroHistoryMessage{}, payload.ConversationState.History...), KiroHistoryMessage{UserInputMessage: &payload.ConversationState.CurrentMessage.UserInputMessage})
	var pending []KiroToolUse
	for i, msg := range messages {
		if a := msg.AssistantResponseMessage; a != nil {
			require.Empty(t, pending, "unanswered tools before message %d", i)
			pending = a.ToolUses
			continue
		}
		u := msg.UserInputMessage
		require.NotNil(t, u)
		var results []KiroToolResult
		if u.UserInputMessageContext != nil {
			results = u.UserInputMessageContext.ToolResults
			if i < len(messages)-1 {
				require.Empty(t, u.UserInputMessageContext.Tools)
			}
		}
		require.Len(t, results, len(pending), "message %d", i)
		ids := make(map[string]bool)
		for _, call := range pending {
			ids[call.ToolUseID] = true
		}
		for _, result := range results {
			require.True(t, ids[result.ToolUseID], "orphan/duplicate %s at %d", result.ToolUseID, i)
			delete(ids, result.ToolUseID)
			require.NotContains(t, u.Content, result.Content[0].Text, "structured result duplicated as prose")
		}
		pending = nil
	}
	require.Empty(t, pending)
}
