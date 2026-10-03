package cursor

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"io"
	"net/http"
	"testing"
	"time"

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
	text, code := outsideExecThrowMessage()
	msgs := execClientThrowAndClose(&pb.ExecServerMessage{Id: 9, ExecId: "e1"}, text, code)
	require.Len(t, msgs, 2)
	throw := msgs[0].GetExecClientControlMessage().GetThrow()
	require.NotNil(t, throw)
	require.EqualValues(t, 9, throw.GetId())
	require.Equal(t, "Workspace tools are unavailable through this gateway. Continue without local execution.", throw.GetError())
	require.Equal(t, "gateway_tool_unavailable", throw.GetErrorCode())
	require.NotNil(t, msgs[1].GetExecClientControlMessage().GetStreamClose())
	require.EqualValues(t, 9, msgs[1].GetExecClientControlMessage().GetStreamClose().GetId())
}

func TestAgentRunOutsideExecThrowsAndContinuesText(t *testing.T) {
	// Unknown workspace exec variants stay opaque; the fake upstream waits for
	// both control replies before allowing text/usage to continue.
	for _, field := range []protowire.Number{2, 3, 4, 5, 99} {
		t.Run(fmt.Sprint(field), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			core, logs := observer.New(zap.DebugLevel)
			ctx = logger.IntoContext(ctx, zap.New(core))
			reader, writer := io.Pipe()
			defer func() { _ = reader.Close() }()
			wireErr := make(chan error, 1)
			result, err := RunAgent(ctx, "test-credential", AgentRequest{
				Model: "composer-2.5", Messages: []AgentMessage{{Role: "user", Text: "hello"}},
			}, func(req *http.Request) (*http.Response, error) {
				go func() {
					defer func() { _ = writer.Close() }()
					check := func() error {
						read := func() (*pb.AgentClientMessage, error) {
							_, raw, err := readAgentFrame(req.Body)
							if err != nil {
								return nil, err
							}
							msg := &pb.AgentClientMessage{}
							return msg, proto.Unmarshal(raw, msg)
						}
						run, err := read()
						if err != nil {
							return err
						}
						if run.GetRunRequest().GetConversationState().GetMode() != agentModeAsk {
							return fmt.Errorf("gateway advertised an execution mode")
						}
						if req.Header.Get("X-Cursor-Agent-Allowed-Tools") != "" {
							return fmt.Errorf("native tool allowlist advertised")
						}
						exec := &pb.ExecServerMessage{Id: 9, ExecId: "native-exec"}
						exec.ProtoReflect().SetUnknown(protowire.AppendBytes(protowire.AppendTag(nil, field, protowire.BytesType), []byte("opaque workspace arguments")))
						if err := writeAgentFrame(writer, &pb.AgentServerMessage{ExecServerMessage: exec}); err != nil {
							return err
						}
						throw, err := read()
						if err != nil {
							return err
						}
						feedback := throw.GetExecClientControlMessage().GetThrow()
						text, code := outsideExecThrowMessage()
						if feedback.GetId() != 9 || feedback.GetError() != text || feedback.GetErrorCode() != code {
							return fmt.Errorf("unexpected throw: %v", throw)
						}
						closeMsg, err := read()
						if err != nil {
							return err
						}
						if closeMsg.GetExecClientControlMessage().GetStreamClose().GetId() != 9 {
							return fmt.Errorf("missing stream close: %v", closeMsg)
						}
						if err := writeAgentFrame(writer, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "hello after throw"}}}); err != nil {
							return err
						}
						return writeAgentFrame(writer, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TurnEnded: &pb.TurnEndedUpdate{InputTokens: proto.Int64(3), OutputTokens: proto.Int64(2), CacheReadTokens: proto.Int64(0), CacheWriteTokens: proto.Int64(0)}}})
					}
					wireErr <- check()
				}()
				return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: reader}, nil
			}, nil)
			require.NoError(t, err)
			require.NoError(t, <-wireErr)
			require.Equal(t, "hello after throw", result.Text)
			require.EqualValues(t, 2, result.Usage.Output)
			require.Empty(t, result.ToolCalls)
			entries := logs.FilterMessage("cursor_agentrun_outside_exec_throw").All()
			require.Len(t, entries, 1)
			require.Equal(t, "exec_variant_unsupported", entries[0].ContextMap()["error_code"])
			require.Equal(t, "gateway_tool_unavailable", entries[0].ContextMap()["public_error_code"])
		})
	}
}

func TestAgentRunUndeclaredMcpExecThrowsAndContinuesText(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	core, logs := observer.New(zap.DebugLevel)
	ctx = logger.IntoContext(ctx, zap.New(core))
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	wireErr := make(chan error, 1)
	result, err := RunAgent(ctx, "test-credential", AgentRequest{
		Model: "claude-fable-5-1", Messages: []AgentMessage{{Role: "user", Text: "hello"}},
	}, func(req *http.Request) (*http.Response, error) {
		go func() {
			defer func() { _ = writer.Close() }()
			read := func() (*pb.AgentClientMessage, error) {
				_, raw, err := readAgentFrame(req.Body)
				if err != nil {
					return nil, err
				}
				message := &pb.AgentClientMessage{}
				return message, proto.Unmarshal(raw, message)
			}
			if _, err := read(); err != nil {
				wireErr <- err
				return
			}
			if err := writeAgentFrame(writer, &pb.AgentServerMessage{ExecServerMessage: &pb.ExecServerMessage{
				Id: 11, ExecId: "workspace-shell", McpArgs: &pb.McpArgs{Name: "shell", ToolCallId: "native-call"},
			}}); err != nil {
				wireErr <- err
				return
			}
			throw, err := read()
			if err != nil {
				wireErr <- err
				return
			}
			feedback := throw.GetExecClientControlMessage().GetThrow()
			if feedback.GetId() != 11 || feedback.GetErrorCode() != "gateway_tool_unavailable" || feedback.GetError() != "Workspace tools are unavailable through this gateway. Continue without local execution." {
				wireErr <- fmt.Errorf("unexpected undeclared-tool throw: %v", throw)
				return
			}
			closeMsg, err := read()
			if err != nil {
				wireErr <- err
				return
			}
			if closeMsg.GetExecClientControlMessage().GetStreamClose().GetId() != 11 {
				wireErr <- fmt.Errorf("unexpected undeclared-tool close: %v", closeMsg)
				return
			}
			if err := writeAgentFrame(writer, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "continued"}}}); err != nil {
				wireErr <- err
				return
			}
			wireErr <- writeAgentFrame(writer, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TurnEnded: &pb.TurnEndedUpdate{
				InputTokens: proto.Int64(3), OutputTokens: proto.Int64(2), CacheReadTokens: proto.Int64(0), CacheWriteTokens: proto.Int64(0),
			}}})
		}()
		return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: reader}, nil
	}, nil)
	require.NoError(t, err)
	require.NoError(t, <-wireErr)
	require.Equal(t, "continued", result.Text)
	require.Empty(t, result.ToolCalls)
	require.EqualValues(t, 2, result.Usage.Output)
	entries := logs.FilterMessage("cursor_agentrun_mcp_tool_throw").All()
	require.Len(t, entries, 1)
	require.Equal(t, "undeclared_mcp_tool", entries[0].ContextMap()["error_code"])
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
