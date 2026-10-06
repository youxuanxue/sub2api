package cursor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestNativeClientResultEvidence(t *testing.T) {
	t.Run("shell errors do not imply permission rejection or an exit code", func(t *testing.T) {
		for _, exec := range []*pb.ExecServerMessage{
			{Id: 9, ShellArgs: &pb.ShellArgs{Command: "exit 7"}},
			{Id: 9, ShellStreamArgs: &pb.ShellArgs{Command: "exit 7"}},
			{Id: 9, MiniSweAgentBashArgs: &pb.ShellArgs{Command: "exit 7"}},
		} {
			text := "client failed: exit code 7; original output"
			replies := nativeClientResults(exec, AgentMessage{Text: text, IsError: true})
			require.Nil(t, replies[0].ExecClientMessage)
			require.Equal(t, text, replies[0].ExecClientControlMessage.Throw.Error)
			require.Equal(t, "client_tool_error", replies[0].ExecClientControlMessage.Throw.GetErrorCode())
			require.EqualValues(t, 9, replies[1].ExecClientControlMessage.StreamClose.Id)
		}
	})
	t.Run("background response keeps original client evidence", func(t *testing.T) {
		text := "Command running in background with ID: client-123\nclient output"
		replies := nativeClientResults(&pb.ExecServerMessage{Id: 10, ShellStreamArgs: &pb.ShellArgs{Command: "task"}}, AgentMessage{Text: text})
		require.Nil(t, replies[0].ExecClientMessage)
		require.Equal(t, "Client output cannot confirm foreground completion; no process is managed by the gateway.\n"+text, replies[0].ExecClientControlMessage.Throw.Error)
		require.Equal(t, "client_tool_result_unrepresentable", replies[0].ExecClientControlMessage.Throw.GetErrorCode())
	})
	t.Run("read page is not whole file size", func(t *testing.T) {
		exec := &pb.ExecServerMessage{Id: 11, ReadArgs: &pb.ReadArgs{Path: "/client/file", Offset: proto.Int32(10), Limit: proto.Uint32(1)}}
		result := nativeClientResults(exec, AgentMessage{Text: "10→page"})[0].ExecClientMessage.ReadResult.Success
		require.Equal(t, "page", result.GetContent())
		require.True(t, result.RangeApplied)
		require.Zero(t, result.FileSize)
		require.Zero(t, result.TotalLines)
	})
	t.Run("grep match content can contain colon numbers", func(t *testing.T) {
		result, ok := clientGrepResult(&pb.GrepArgs{Pattern: "target"}, "file.go:12:target:123:tail")
		require.True(t, ok)
		match := result.WorkspaceResults[""].Content.Matches[0]
		require.Equal(t, "file.go", match.File)
		require.EqualValues(t, 12, match.Matches[0].LineNumber)
		require.Equal(t, "target:123:tail", match.Matches[0].Content)
	})
	t.Run("grep count cannot overflow into a false total", func(t *testing.T) {
		_, ok := clientGrepResult(&pb.GrepArgs{OutputMode: proto.String("count")}, "a:2147483647\nb:1")
		require.False(t, ok)
	})
}

func TestNativeContinuationTrailingTextFailsClosed(t *testing.T) {
	id := newPendingToolID()
	for _, body := range []string{
		fmt.Sprintf(`{"messages":[{"role":"tool","tool_call_id":%q,"content":"result"},{"role":"user","content":"continue"}]}`, id),
		fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":"result"},{"type":"text","text":"continue"}]}]}`, id),
		fmt.Sprintf(`{"input":[{"type":"function_call_output","call_id":%q,"output":"result"},{"role":"user","content":"continue"}]}`, id),
		fmt.Sprintf(`{"messages":[{"role":"tool","tool_call_id":%q,"content":"result"},{"role":"system","content":"same rules"},{"role":"user","content":"continue"}]}`, id),
		fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":"result"}]},{"role":"system","content":"same rules"}]}`, id),
		fmt.Sprintf(`{"input":[{"type":"function_call_output","call_id":%q,"output":"result"},{"role":"developer","content":"same rules"},{"role":"user","content":"continue"}]}`, id),
	} {
		require.Equal(t, []string{id}, PendingToolIDs([]byte(body)))
		account, err := ContinuationAccount(16, 23, []byte(body))
		require.Zero(t, account)
		require.ErrorIs(t, err, ErrContinuationUnavailable, "an unknown result must fail before ordinary scheduling")
	}
	ctx := WithRunOwner(t.Context(), RunOwner{16, 23, 150})
	input := AgentRequest{Model: "composer-2.5", Tools: []AgentTool{nativeTestTool("Read", "file_path")}, Messages: []AgentMessage{
		{Role: "tool", ToolCallID: id, Text: "result"}, {Role: "user", Text: "continue"},
	}}
	var calls atomic.Int32
	do := func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, fmt.Errorf("unexpected upstream replay")
	}
	_, _, err := newNativeRunManager().segment(ctx, "test", input, do, nil)
	require.ErrorIs(t, err, ErrContinuationUnavailable)
	require.Zero(t, calls.Load())
}

func TestNativeContinuationConcurrentSingleConsumption(t *testing.T) {
	m := newNativeRunManager()
	ctx, cancel := context.WithTimeout(WithRunOwner(t.Context(), RunOwner{16, 23, 150}), 3*time.Second)
	defer cancel()
	input := AgentRequest{Model: "composer-2.5", Tools: []AgentTool{nativeTestTool("Read", "file_path")}, Messages: []AgentMessage{{Role: "user", Text: "read"}}}
	var calls atomic.Int32
	var results atomic.Int32
	do := retainedFixture(t, []nativeFixtureStep{{exec: &pb.ExecServerMessage{Id: 1, ReadArgs: &pb.ReadArgs{Path: "/client/file"}}, check: func(reply *pb.ExecClientMessage) error {
		results.Add(1)
		if reply.GetReadResult().GetSuccess().GetContent() != "client data" {
			return fmt.Errorf("wrong result content")
		}
		return nil
	}}}, &calls)
	first, run, err := m.segment(ctx, "test", input, do, nil)
	require.NoError(t, err)
	defer m.abort(run)
	input.Messages = []AgentMessage{{Role: "tool", ToolCallID: first.ToolCalls[0].ID, Text: "client data"}}
	start := make(chan struct{})
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, _, err := m.segment(ctx, "test", input, do, nil)
			errors <- err
		}()
	}
	close(start)
	a, b := <-errors, <-errors
	if a == nil {
		require.ErrorIs(t, b, ErrContinuationUnavailable)
	} else {
		require.ErrorIs(t, a, ErrContinuationUnavailable)
		require.NoError(t, b)
	}
	require.EqualValues(t, 1, calls.Load())
	require.EqualValues(t, 1, results.Load())
}

func TestNativeShellErrorContinuesOriginalRun(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(WithRunOwner(t.Context(), RunOwner{16, 23, 150}), 3*time.Second)
			defer cancel()
			var calls atomic.Int32
			do := retainedFixture(t, []nativeFixtureStep{{
				exec:      &pb.ExecServerMessage{Id: 7, ShellStreamArgs: &pb.ShellArgs{Command: "exit 7"}},
				wantThrow: &pb.ExecClientThrow{Id: 7, Error: "command failed: exit 7", ErrorCode: proto.String("client_tool_error")},
			}}, &calls)
			tool := nativeTestTool("Bash", "command")
			body := map[string]any{"model": "composer-2.5", "stream": stream, "tools": []any{map[string]any{"name": tool.Name, "input_schema": tool.Schema}}, "messages": []any{map[string]any{"role": "user", "content": "run on client"}}}
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			response, err := Messages(ctx, "test-token", raw, nil, "composer-2.5", do)
			require.NoError(t, err)
			call, _ := readNativeResponse(t, response)
			require.Equal(t, "Bash", call.Name)
			body["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": call.ID, "content": "command failed: exit 7", "is_error": true}}}}
			raw, err = json.Marshal(body)
			require.NoError(t, err)
			response, err = Messages(ctx, "test-token", raw, nil, "composer-2.5", do)
			require.NoError(t, err)
			finalCall, final := readNativeResponse(t, response)
			require.Empty(t, finalCall.ID)
			require.Contains(t, final, "client work acknowledged")
			require.Contains(t, final, `"input_tokens":41`)
			require.EqualValues(t, 1, calls.Load())
		})
	}
}
