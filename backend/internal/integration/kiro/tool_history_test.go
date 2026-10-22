//go:build unit

package kiro

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

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
	req.Messages = append(req.Messages,
		ClaudeMessage{Role: "assistant", Content: "I received the results."},
		ClaudeMessage{Role: "user", Content: "Summarize the results without calling tools."})
	payload := ClaudeToKiro(req, false)
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

func TestClaudeToKiro_HistoryTruncationPreservesCurrentUserContent(t *testing.T) {
	const current = "请继续处理最新请求：保留这段 UTF-8 内容"
	req := &ClaudeRequest{
		Model:    "claude-opus-5.5",
		Messages: []ClaudeMessage{{Role: "user", Content: "start"}},
	}
	for i := 0; i < 8; i++ {
		req.Messages = append(req.Messages,
			ClaudeMessage{Role: "assistant", Content: strings.Repeat("a", 180*1024)},
			ClaudeMessage{Role: "user", Content: strings.Repeat("u", 180*1024)},
		)
	}
	req.Messages = append(req.Messages,
		ClaudeMessage{Role: "assistant", Content: "ready for the next request"},
		ClaudeMessage{Role: "user", Content: current},
	)

	payload := ClaudeToKiro(req, false)
	require.NoError(t, ValidatePayloadSize(payload))
	require.Less(t, len(payload.ConversationState.History), len(req.Messages)-1)
	require.Equal(t, current, payload.ConversationState.CurrentMessage.UserInputMessage.Content)
	require.True(t, utf8.ValidString(payload.ConversationState.CurrentMessage.UserInputMessage.Content))
	require.Contains(t, payload.ConversationState.History[0].UserInputMessage.Content, truncationPlaceholder)
}

func TestTruncatePayload_StripsOldestHistoryImagesBeforeDroppingTurns(t *testing.T) {
	payload := &KiroPayload{}
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{
		Content: "current text",
		ModelID: "claude-opus-4.8",
		Origin:  "AI_EDITOR",
	}
	const imageBytes = 200 * 1024
	for i := 0; i < 6; i++ {
		user := &KiroUserInputMessage{
			Content: fmt.Sprintf("user turn %d", i),
			ModelID: "claude-opus-4.8",
			Origin:  "AI_EDITOR",
			Images: []KiroImage{{Format: "png", Source: struct {
				Bytes string `json:"bytes"`
			}{Bytes: strings.Repeat("A", imageBytes)}}},
		}
		payload.ConversationState.History = append(payload.ConversationState.History,
			KiroHistoryMessage{UserInputMessage: user},
			KiroHistoryMessage{AssistantResponseMessage: &KiroAssistantResponseMessage{Content: fmt.Sprintf("assistant turn %d", i)}},
		)
	}
	require.Greater(t, payloadByteSize(payload), maxPayloadBytes)
	truncatePayloadToLimit(payload, false)
	require.LessOrEqual(t, payloadByteSize(payload), maxPayloadBytes)
	require.Len(t, payload.ConversationState.History, 12, "text turns stay; only images are removed")
	first := payload.ConversationState.History[0].UserInputMessage
	require.Empty(t, first.Images)
	require.Contains(t, first.Content, "user turn 0")
	require.Contains(t, first.Content, historyImagePlaceholder)
	lastUser := payload.ConversationState.History[10].UserInputMessage
	require.Contains(t, lastUser.Content, "user turn 5")
	require.NotEmpty(t, lastUser.Images, "newest screenshot stays while older ones cover the excess")
	require.Equal(t, "current text", payload.ConversationState.CurrentMessage.UserInputMessage.Content)
}

func oversizedToolResult(id, blob string) KiroToolResult {
	return KiroToolResult{
		ToolUseID: id,
		Status:    "success",
		Content:   []KiroResultContent{{Text: blob}},
	}
}

func TestTruncatePayload_ClipsOversizedCurrentToolResult(t *testing.T) {
	blob := "HEAD-MARKER" + strings.Repeat("x", 1100*1024) + "TAIL-MARKER"
	payload := &KiroPayload{}
	payload.ConversationState.History = []KiroHistoryMessage{
		{UserInputMessage: &KiroUserInputMessage{Content: "earlier", ModelID: "claude-opus-4.8", Origin: "AI_EDITOR"}},
		{AssistantResponseMessage: &KiroAssistantResponseMessage{
			Content:  "reading",
			ToolUses: []KiroToolUse{{ToolUseID: "read-1", Name: "Read", Input: map[string]any{}}},
		}},
	}
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{
		Content:                 "please continue",
		ModelID:                 "claude-opus-4.8",
		Origin:                  "AI_EDITOR",
		UserInputMessageContext: &UserInputMessageContext{ToolResults: []KiroToolResult{oversizedToolResult("read-1", blob)}},
	}
	require.Greater(t, payloadByteSize(payload), maxPayloadBytes)
	truncatePayloadToLimit(payload, false)
	require.NoError(t, ValidatePayloadSize(payload))
	got := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.ToolResults[0]
	require.Equal(t, "read-1", got.ToolUseID)
	require.Contains(t, got.Content[0].Text, "HEAD-MARKER")
	require.Contains(t, got.Content[0].Text, "TAIL-MARKER")
	require.Contains(t, got.Content[0].Text, "tool output omitted")
	require.Contains(t, payload.ConversationState.CurrentMessage.UserInputMessage.Content, "please continue")
	require.Less(t, len(got.Content[0].Text), 32*1024)
	require.Contains(t, historyText(payload), "earlier", "history stays when the current tool result was the excess")
}

func TestTruncatePayload_DropsOneHistoryImageAtATime(t *testing.T) {
	filler := strings.Repeat("x", maxPayloadBytes-150*1024)
	payload := &KiroPayload{}
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{
		Content: filler,
		ModelID: "claude-opus-4.8",
		Origin:  "AI_EDITOR",
	}
	images := []KiroImage{
		kiroImage(100 * 1024),
		kiroImage(100 * 1024),
	}
	payload.ConversationState.History = []KiroHistoryMessage{{
		UserInputMessage: &KiroUserInputMessage{
			Content: "turn with two screenshots",
			ModelID: "claude-opus-4.8",
			Origin:  "AI_EDITOR",
			Images:  images,
		},
	}, {
		AssistantResponseMessage: &KiroAssistantResponseMessage{Content: "noted"},
	}}
	require.Greater(t, payloadByteSize(payload), maxPayloadBytes)
	truncatePayloadToLimit(payload, false)
	require.LessOrEqual(t, payloadByteSize(payload), maxPayloadBytes)
	user := payload.ConversationState.History[0].UserInputMessage
	require.Len(t, user.Images, 1)
	require.Contains(t, user.Content, "turn with two screenshots")
	require.Contains(t, user.Content, historyImagePlaceholder)
}

func TestTruncatePayload_KeepsCurrentImageWhenToolResultClipFits(t *testing.T) {
	blob := "HEAD-MARKER" + strings.Repeat("x", 1100*1024) + "TAIL-MARKER"
	payload := &KiroPayload{}
	payload.ConversationState.History = []KiroHistoryMessage{
		{UserInputMessage: &KiroUserInputMessage{Content: "earlier", ModelID: "claude-opus-4.8", Origin: "AI_EDITOR"}},
		{AssistantResponseMessage: &KiroAssistantResponseMessage{Content: "ok"}},
	}
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{
		Content: "describe this",
		ModelID: "claude-opus-4.8",
		Origin:  "AI_EDITOR",
		Images:  []KiroImage{kiroImage(32 * 1024)},
		UserInputMessageContext: &UserInputMessageContext{ToolResults: []KiroToolResult{
			oversizedToolResult("read-1", blob),
		}},
	}
	truncatePayloadToLimit(payload, false)
	require.NoError(t, ValidatePayloadSize(payload))
	current := payload.ConversationState.CurrentMessage.UserInputMessage
	require.Len(t, current.Images, 1)
	require.NotContains(t, current.Content, historyImagePlaceholder)
	require.Contains(t, historyText(payload), "earlier")
}

func TestClaudeToKiro_ClipsOrphanToolResultAlreadyNarrated(t *testing.T) {
	blob := "HEAD-MARKER" + strings.Repeat("z", 1100*1024) + "TAIL-MARKER"
	req := &ClaudeRequest{Model: "claude-opus-4.8", Messages: []ClaudeMessage{{
		Role: "user",
		Content: []any{
			map[string]any{"type": "text", "text": "please continue"},
			map[string]any{"type": "tool_result", "tool_use_id": "read-1", "content": blob},
		},
	}}}
	payload := ClaudeToKiro(req, false)
	require.NoError(t, ValidatePayloadSize(payload))
	content := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	require.Contains(t, content, "please continue")
	require.Contains(t, content, "HEAD-MARKER")
	require.Contains(t, content, "TAIL-MARKER")
	require.Contains(t, content, "tool output omitted")
	require.Less(t, len(content), 64*1024)
}

func TestClipToolResultText_KeepsHeadWhenCutSplitsRune(t *testing.T) {
	body := strings.Repeat("a", toolResultKeepHead-1) + "你" + strings.Repeat("b", toolResultKeepTail+4096) + "TAIL-MARKER"
	got := clipToolResultText(body)
	require.True(t, utf8.ValidString(got))
	require.True(t, strings.HasPrefix(got, strings.Repeat("a", toolResultKeepHead-1)))
	head, _, _ := strings.Cut(got, toolResultOmission)
	require.NotContains(t, head, "你")
	require.Contains(t, got, "TAIL-MARKER")

	raw := []byte(strings.Repeat("a", toolResultKeepHead) + strings.Repeat("b", toolResultKeepTail+128) + "TAIL")
	raw[10] = 0xff
	kept := clipToolResultText(string(raw))
	require.Equal(t, byte(0xff), []byte(kept)[10])
	require.Contains(t, kept, "TAIL")
}

func TestClipToolResultText_KeepsBinaryContinuationHead(t *testing.T) {
	raw := bytes.Repeat([]byte{0x80}, toolResultKeepHead+toolResultKeepTail+4096)
	copy(raw[len(raw)-4:], []byte("TAIL"))
	got := clipToolResultText(string(raw))
	head, _, ok := strings.Cut(got, toolResultOmission)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(head), toolResultKeepHead-(utf8.UTFMax-1))
	require.Equal(t, byte(0x80), head[0])
	require.Contains(t, got, "TAIL")
}

func kiroImage(n int) KiroImage {
	img := KiroImage{Format: "png"}
	img.Source.Bytes = strings.Repeat("A", n)
	return img
}

func historyText(payload *KiroPayload) string {
	var b strings.Builder
	for _, msg := range payload.ConversationState.History {
		if msg.UserInputMessage != nil {
			b.WriteString(msg.UserInputMessage.Content)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func TestTruncatePayload_ClipsOrphanToolResultIntoContent(t *testing.T) {
	blob := "HEAD-MARKER" + strings.Repeat("y", 1100*1024) + "TAIL-MARKER"
	payload := &KiroPayload{}
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{
		Content:                 "please continue",
		ModelID:                 "claude-opus-4.8",
		Origin:                  "AI_EDITOR",
		UserInputMessageContext: &UserInputMessageContext{ToolResults: []KiroToolResult{oversizedToolResult("read-1", blob)}},
	}
	require.Greater(t, payloadByteSize(payload), maxPayloadBytes)
	truncatePayloadToLimit(payload, false)
	require.NoError(t, ValidatePayloadSize(payload))
	current := payload.ConversationState.CurrentMessage.UserInputMessage
	require.Contains(t, current.Content, "please continue")
	require.Contains(t, current.Content, "HEAD-MARKER")
	require.Contains(t, current.Content, "TAIL-MARKER")
	require.Contains(t, current.Content, "tool output omitted")
}

func TestTruncatePayload_LeavesFittingToolOutputUntouched(t *testing.T) {
	text := strings.Repeat("file.go:1: match\n", 40) // already under the cap; RTK would still rewrite this shape
	payload := &KiroPayload{}
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{
		Content: "ok",
		ModelID: "claude-opus-4.8",
		Origin:  "AI_EDITOR",
		UserInputMessageContext: &UserInputMessageContext{ToolResults: []KiroToolResult{{
			ToolUseID: "grep-1",
			Status:    "success",
			Content:   []KiroResultContent{{Text: text}},
		}}},
	}
	require.LessOrEqual(t, payloadByteSize(payload), maxPayloadBytes)
	truncatePayloadToLimit(payload, false)
	got := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.ToolResults[0].Content[0].Text
	require.Equal(t, text, got)
}

func TestClaudeToKiro_OversizedFixedPayloadPreservesCurrentUserContent(t *testing.T) {
	const current = "最新消息不能被替换成点号。"
	req := &ClaudeRequest{
		Model:    "claude-opus-5.5",
		Messages: []ClaudeMessage{{Role: "user", Content: current}},
		Tools: []ClaudeTool{{
			Name: "oversized_tool",
			InputSchema: map[string]any{
				"type":        "object",
				"description": strings.Repeat("x", maxPayloadBytes),
			},
		}},
	}

	payload := ClaudeToKiro(req, false)
	require.Equal(t, current, payload.ConversationState.CurrentMessage.UserInputMessage.Content)
	require.True(t, utf8.ValidString(payload.ConversationState.CurrentMessage.UserInputMessage.Content))

	var tooLarge *PayloadTooLargeError
	err := ValidatePayloadSize(payload)
	require.ErrorAs(t, err, &tooLarge)
	require.Greater(t, tooLarge.Size, tooLarge.Limit)
	require.Equal(t, maxPayloadBytes, tooLarge.Limit)
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
