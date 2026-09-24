package cursor

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"testing"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestClassifyMessagesAlignedExec(t *testing.T) {
	require.Equal(t, messagesExecRequestContext, classifyMessagesAlignedExec(&pb.ExecServerMessage{RequestContextArgs: &pb.Empty{}}))
	require.Equal(t, messagesExecToolUse, classifyMessagesAlignedExec(&pb.ExecServerMessage{McpArgs: &pb.McpArgs{Name: "mcp__tokenkey__lookup"}}))
	require.Equal(t, messagesExecOutside, classifyMessagesAlignedExec(&pb.ExecServerMessage{Id: 1}))
	require.Equal(t, messagesExecOutside, classifyMessagesAlignedExec(nil))
}

func TestExecClientThrowAndCloseShapes(t *testing.T) {
	// Positive: throw + stream_close pair for Cursor recovery. Negative: nil exec → no frames.
	require.Nil(t, execClientThrowAndClose(nil, "x", "y"))
	msgs := execClientThrowAndClose(&pb.ExecServerMessage{Id: 9, ExecId: "e1"}, "TokenKey Messages relay (fields=2)", "exec_variant_unsupported")
	require.Len(t, msgs, 2)
	throw := msgs[0].GetExecClientControlMessage().GetThrow()
	require.NotNil(t, throw)
	require.EqualValues(t, 9, throw.GetId())
	require.Equal(t, "TokenKey Messages relay (fields=2)", throw.GetError())
	require.Equal(t, "exec_variant_unsupported", throw.GetErrorCode())
	require.NotNil(t, msgs[1].GetExecClientControlMessage().GetStreamClose())
	require.EqualValues(t, 9, msgs[1].GetExecClientControlMessage().GetStreamClose().GetId())
}

func TestAgentRunOutsideExecThrowsAndContinuesText(t *testing.T) {
	// Positive: unknown shell_args (field 2) triggers throw (2 replies) and later
	// text+usage still surface. Negative: must not fail the Messages turn.
	var wire []byte
	wire = protowire.AppendTag(wire, 1, protowire.VarintType)
	wire = protowire.AppendVarint(wire, 9)
	wire = protowire.AppendTag(wire, 2, protowire.BytesType)
	wire = protowire.AppendBytes(wire, []byte{0x0a, 0x01, 0x78})
	var exec pb.ExecServerMessage
	require.NoError(t, proto.Unmarshal(wire, &exec))
	require.Equal(t, messagesExecOutside, classifyMessagesAlignedExec(&exec))
	require.Contains(t, protobufFieldNumbers(&exec), 2)

	var stream bytes.Buffer
	require.NoError(t, writeAgentFrame(&stream, &pb.AgentServerMessage{ExecServerMessage: &exec}))
	require.NoError(t, writeAgentFrame(&stream, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{
		TextDelta: &pb.TextDeltaUpdate{Text: "hello after throw"},
	}}))
	require.NoError(t, writeAgentFrame(&stream, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{
		TurnEnded: &pb.TurnEndedUpdate{InputTokens: proto.Int64(3), OutputTokens: proto.Int64(2), CacheReadTokens: proto.Int64(0), CacheWriteTokens: proto.Int64(0)},
	}}))

	var sawFields []int
	var sawReplies int
	testOutsideExecThrowHook = func(fields []int, replyCount int) {
		sawFields = append([]int(nil), fields...)
		sawReplies = replyCount
	}
	t.Cleanup(func() { testOutsideExecThrowHook = nil })

	result, err := RunAgent(context.Background(), "test-credential", AgentRequest{
		Model:    "composer-2.5",
		Messages: []AgentMessage{{Role: "user", Text: "hello"}},
	}, func(req *http.Request) (*http.Response, error) {
		go func() { _, _ = io.Copy(io.Discard, req.Body) }()
		return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: io.NopCloser(bytes.NewReader(stream.Bytes()))}, nil
	}, nil)
	require.NoError(t, err)
	require.Equal(t, "hello after throw", result.Text)
	require.NotNil(t, result.Usage)
	require.EqualValues(t, 2, result.Usage.Output)
	require.Contains(t, sawFields, 2)
	require.Equal(t, 2, sawReplies, "throw + stream_close")
}

func TestAgentRunMapsInteractionToolCallStartedToMessagesHandoff(t *testing.T) {
	// Positive: InteractionUpdate tool_call_started with McpToolCall → tool_use handoff.
	// Negative: empty tool_call_started is skipped so later text can still complete.
	t.Run("mcp_handoff", func(t *testing.T) {
		var stream bytes.Buffer
		require.NoError(t, writeAgentFrame(&stream, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{
			ToolCallStarted: &pb.ToolCallStartedUpdate{
				CallId: "call_a",
				ToolCall: &pb.ToolCall{McpToolCall: &pb.McpToolCall{Args: &pb.McpArgs{
					Name: cursorWireToolName("lookup"), ToolCallId: "call_a", ToolName: "lookup",
					Args: map[string]*structpb.Value{"key": structpb.NewStringValue("demo")},
				}}},
			},
		}}))
		trailer := []byte(`{"error":{"code":"canceled"}}`)
		header := [5]byte{2}
		binary.BigEndian.PutUint32(header[1:], uint32(len(trailer)))
		_, _ = stream.Write(header[:])
		_, _ = stream.Write(trailer)

		result, err := RunAgent(context.Background(), "test-credential", AgentRequest{
			Model:    "composer-2.5",
			Messages: []AgentMessage{{Role: "user", Text: "lookup"}},
			Tools:    []AgentTool{{Name: "lookup", Schema: map[string]any{"type": "object"}}},
		}, func(req *http.Request) (*http.Response, error) {
			go func() { _, _ = io.Copy(io.Discard, req.Body) }()
			return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: io.NopCloser(bytes.NewReader(stream.Bytes()))}, nil
		}, nil)
		require.NoError(t, err)
		require.True(t, result.ToolHandoff)
		require.Len(t, result.ToolCalls, 1)
		require.Equal(t, "lookup", result.ToolCalls[0].Name)
		require.Equal(t, "demo", result.ToolCalls[0].Arguments["key"])
	})

	t.Run("non_mcp_skipped_continues_text", func(t *testing.T) {
		var stream bytes.Buffer
		require.NoError(t, writeAgentFrame(&stream, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{
			ToolCallStarted: &pb.ToolCallStartedUpdate{CallId: "call_x", ToolCall: &pb.ToolCall{}},
		}}))
		require.NoError(t, writeAgentFrame(&stream, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{
			TextDelta: &pb.TextDeltaUpdate{Text: "still here"},
		}}))
		require.NoError(t, writeAgentFrame(&stream, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{
			TurnEnded: &pb.TurnEndedUpdate{InputTokens: proto.Int64(1), OutputTokens: proto.Int64(1), CacheReadTokens: proto.Int64(0), CacheWriteTokens: proto.Int64(0)},
		}}))
		result, err := RunAgent(context.Background(), "test-credential", AgentRequest{
			Model:    "composer-2.5",
			Messages: []AgentMessage{{Role: "user", Text: "hello"}},
		}, func(req *http.Request) (*http.Response, error) {
			go func() { _, _ = io.Copy(io.Discard, req.Body) }()
			return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: io.NopCloser(bytes.NewReader(stream.Bytes()))}, nil
		}, nil)
		require.NoError(t, err)
		require.Equal(t, "still here", result.Text)
	})
}
