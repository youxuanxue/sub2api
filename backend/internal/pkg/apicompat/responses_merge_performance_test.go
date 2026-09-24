package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergeConsecutiveMessagesPreservesContentAndInput(t *testing.T) {
	messages := []AnthropicMessage{
		{Role: "user", Content: json.RawMessage(`"hello"`)},
		{Role: "user", Content: json.RawMessage(`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YQ=="}}]`)},
		{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"call_A","content":[{"type":"text","text":"result"}]}]`)},
		{Role: "assistant", Content: json.RawMessage(`[{"type":"text","text":"answer","unknown":"preserved on singleton"}]`)},
		{Role: "user", Content: json.RawMessage(`null`)},
		{Role: "user", Content: json.RawMessage(`[]`)},
		{Role: "user", Content: json.RawMessage(`{"unsupported":true}`)},
		{Role: "assistant", Content: json.RawMessage(`"tail"`)},
	}
	before, err := json.Marshal(messages)
	require.NoError(t, err)
	got := mergeConsecutiveMessages(messages)
	require.Len(t, got, 4)
	require.JSONEq(t, `[{"type":"text","text":"hello"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YQ=="}},{"type":"tool_result","tool_use_id":"call_A","content":[{"type":"text","text":"result"}]}]`, string(got[0].Content))
	require.Equal(t, messages[3], got[1], "singleton content must remain byte-for-byte unchanged")
	require.Equal(t, "null", string(got[2].Content), "empty/unsupported grouped content retains legacy null representation")
	require.Equal(t, messages[7], got[3])
	after, err := json.Marshal(messages)
	require.NoError(t, err)
	require.Equal(t, before, after, "merging must not mutate the caller's messages")
}

func responsesParallelHistory(n int) json.RawMessage {
	items := []ResponsesInputItem{{Role: "user", Content: json.RawMessage(`"run tools"`)}}
	for i := 0; i < n; i++ {
		items = append(items, ResponsesInputItem{Type: "function_call", CallID: fmt.Sprintf("call_%d", i), Name: "exec", Arguments: `{"cmd":"pwd"}`})
	}
	for i := 0; i < n; i++ {
		items = append(items, ResponsesInputItem{Type: "function_call_output", CallID: fmt.Sprintf("call_%d", i), Output: strings.Repeat("result ", 600)})
	}
	items = append(items, ResponsesInputItem{Role: "assistant", Content: json.RawMessage(`"done"`)})
	raw, err := json.Marshal(items)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestResponsesParallelHistoryPreservesToolOrder(t *testing.T) {
	req := &ResponsesRequest{Model: "test-model", Input: responsesParallelHistory(64)}
	out, err := ResponsesToAnthropicRequest(req)
	require.NoError(t, err)
	assertAnthropicPairing(t, out.Messages)
	require.Len(t, out.Messages, 4)
	calls := parseContentBlocks(out.Messages[1].Content)
	results := parseContentBlocks(out.Messages[2].Content)
	require.Len(t, calls, 64)
	require.Len(t, results, 64)
	for i := range calls {
		require.Equal(t, fmt.Sprintf("call_%d", i), calls[i].ID)
		require.Equal(t, calls[i].ID, results[i].ToolUseID)
		require.JSONEq(t, `{"cmd":"pwd"}`, string(calls[i].Input))
		var output string
		require.NoError(t, json.Unmarshal(results[i].Content, &output))
		require.Equal(t, strings.Repeat("result ", 600), output)
	}
}

func BenchmarkResponsesParallelHistory(b *testing.B) {
	for _, n := range []int{1, 16, 64} {
		b.Run(fmt.Sprintf("tools_%d", n), func(b *testing.B) {
			req := &ResponsesRequest{Model: "test-model", Input: responsesParallelHistory(n)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := ResponsesToAnthropicRequest(req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestMergeConsecutiveMessagesAllocationGrowth(t *testing.T) {
	allocated := func(n int) int64 {
		messages := make([]AnthropicMessage, n)
		content, err := json.Marshal(strings.Repeat("result ", 600))
		require.NoError(t, err)
		for i := range messages {
			messages[i] = AnthropicMessage{Role: "user", Content: content}
		}
		result := testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if len(mergeConsecutiveMessages(messages)) != 1 {
					b.Fatal("same-role run was not merged")
				}
			}
		})
		return result.AllocedBytesPerOp()
	}
	small, large := allocated(16), allocated(64)
	require.Less(t, large, small*6, "4x history must not incur quadratic allocation growth")
}

// mergeConsecutiveMessages adapts the standalone merge contract for regression tests.
func mergeConsecutiveMessages(messages []AnthropicMessage) []AnthropicMessage {
	if len(messages) <= 1 {
		return messages
	}
	return materializeAnthropicHistory(mergeAnthropicHistory(newAnthropicHistory(messages)))
}
