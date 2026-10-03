package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/protobuf/types/known/structpb"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/stretchr/testify/require"
)

func TestNormalizeDeclaredTool(t *testing.T) {
	declared := []AgentTool{{Name: "lookup"}, {Name: "other"}}
	for _, model := range []string{"composer-2.5", "claude-sonnet-4.6"} {
		for _, alias := range []string{"lookup", "mcp__tokenkey__lookup", "mcp_tokenkey_lookup"} {
			for _, args := range []*pb.McpArgs{{Name: alias}, {ToolName: alias}, {Name: alias, ToolName: "lookup", ProviderIdentifier: "tokenkey"}} {
				name, ok := normalizeDeclaredTool(args, model, declared)
				require.True(t, ok, "%v", args)
				require.Equal(t, "lookup", name)
			}
		}
	}
	for _, args := range []*pb.McpArgs{
		nil, {}, {Name: "unknown"}, {ToolName: "unknown"}, {Name: "mcp__foreign__lookup"},
		{Name: "lookup", ToolName: "other"}, {Name: "unknown", ToolName: "lookup"},
		{Name: "lookup", ToolName: "unknown"}, {Name: "lookup", ProviderIdentifier: "foreign"},
		{Name: "Lookup"}, {Name: " lookup"}, {Name: "mcp__tokenkey__mcp__tokenkey__lookup"},
	} {
		name, ok := normalizeDeclaredTool(args, "composer-2.5", declared)
		require.False(t, ok, "%v", args)
		require.Empty(t, name)
	}
	for _, alias := range []string{"mcp__tokenkey__lookup", "mcp_tokenkey_lookup"} {
		collision := append(append([]AgentTool{}, declared...), AgentTool{Name: alias})
		name, ok := normalizeDeclaredTool(&pb.McpArgs{Name: alias, ToolName: "lookup"}, "composer-2.5", collision)
		require.False(t, ok)
		require.Empty(t, name)
	}
	name, ok := normalizeDeclaredTool(&pb.McpArgs{Name: "lookup"}, "composer-2.5", nil)
	require.False(t, ok)
	require.Empty(t, name)
}

func TestMessagesToolAliasesHandoffAndReplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, interaction := range []bool{false, true} {
			for _, args := range []*pb.McpArgs{{Name: "lookup"}, {Name: "mcp_tokenkey_lookup"}, {ToolName: "lookup"}} {
				t.Run(fmt.Sprintf("stream=%t/interaction=%t/%s%s", stream, interaction, args.Name, args.ToolName), func(t *testing.T) {
					args.ToolCallId = "call_fixture"
					frame := &pb.AgentServerMessage{ExecServerMessage: &pb.ExecServerMessage{McpArgs: args}}
					if interaction {
						frame = &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{ToolCallStarted: &pb.ToolCallStartedUpdate{ToolCall: &pb.ToolCall{McpToolCall: &pb.McpToolCall{Args: args}}}}}
					}
					body := []byte(fmt.Sprintf(`{"model":"composer-2.5","stream":%t,"messages":[{"role":"user","content":"lookup"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`, stream))
					resp, err := Messages(t.Context(), "test", body, nil, "composer-2.5", messagesTestTransport(t, frame))
					require.NoError(t, err)
					raw, err := io.ReadAll(resp.Body)
					require.NoError(t, err)
					require.NoError(t, resp.Body.Close())
					require.Contains(t, string(raw), `"name":"lookup"`)
					require.Contains(t, string(raw), `"stop_reason":"tool_use"`)
					require.NotContains(t, string(raw), "mcp_tokenkey_")
					// Replay uses the canonical client name, never the upstream alias.
					replay := []byte(`{"model":"composer-2.5","messages":[{"role":"user","content":"lookup"},{"role":"assistant","content":[{"type":"tool_use","id":"call_fixture","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_fixture","content":"result","is_error":true}]}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`)
					input, _, err := parseMessages(replay, nil, "composer-2.5")
					require.NoError(t, err)
					require.Equal(t, "lookup", input.Messages[1].ToolCalls[0].Name)
					require.Equal(t, "call_fixture", input.Messages[2].ToolCallID)
					require.True(t, input.Messages[2].IsError)
					run, _, err := buildAgentRun(input)
					require.NoError(t, err)
					require.NotNil(t, run.Action.ResumeAction)
				})
			}
		}
	}
}

func TestMessagesToolProtocolErrorsAreNeutral(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, afterText := range []bool{false, true} {
			for _, args := range []*pb.McpArgs{
				{Name: "mcp__tokenkey__undeclared", ToolCallId: "call"},
				{Name: "lookup"},
				{Name: "lookup", ToolCallId: "call", SmartModeApprovalOnly: true},
				{Name: "lookup", ToolName: "unknown", ToolCallId: "call"},
			} {
				t.Run(fmt.Sprintf("stream=%t/afterText=%t/%v", stream, afterText, args), func(t *testing.T) {
					frames := []*pb.AgentServerMessage{}
					if afterText {
						frames = append(frames, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "partial"}}})
					}
					frames = append(frames, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{ToolCallStarted: &pb.ToolCallStartedUpdate{
						CallId: "call", ToolCall: &pb.ToolCall{McpToolCall: &pb.McpToolCall{Args: args}},
					}}})
					body := []byte(fmt.Sprintf(`{"model":"composer-2.5","stream":%t,"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`, stream))
					resp, err := Messages(t.Context(), "test", body, nil, "composer-2.5", messagesTestTransport(t, frames...))
					require.NoError(t, err)
					raw, readErr := io.ReadAll(resp.Body)
					require.NoError(t, resp.Body.Close())
					if stream && afterText {
						require.ErrorIs(t, readErr, errAgentToolProtocol)
						require.NotContains(t, string(raw), "message_stop")
						output, ok := resp.Body.(*MessagesBody)
						require.True(t, ok)
						tier, nativeErr := output.Outcome()
						require.Empty(t, tier)
						require.ErrorIs(t, nativeErr, errAgentToolProtocol)
					} else {
						require.NoError(t, readErr)
						require.Equal(t, http.StatusBadGateway, resp.StatusCode)
					}
					require.Contains(t, string(raw), `"code":"upstream_tool_protocol_error"`)
					require.Contains(t, string(raw), "The model returned an unsupported tool call.")
					for _, forbidden := range []string{"cursor", "tokenkey", "agentrun", "undeclared", "tool_use"} {
						require.NotContains(t, strings.ToLower(string(raw)), forbidden)
					}
				})
			}
		}
	}
}

func TestMessagesPublicErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err           error
		code, message string
	}{
		{errAgentToolProtocol, "upstream_tool_protocol_error", "The model returned an unsupported tool call."},
		{context.DeadlineExceeded, "timeout_error", "The model service timed out."},
		{&agentTransportError{cause: errors.New("Cursor AgentRun private-token")}, "upstream_unavailable", "The model service is temporarily unavailable."},
		{newAgentRejection(503, "unavailable", "Cursor secret-model", "", "supplier-request"), "upstream_unavailable", "The model service is temporarily unavailable."},
		{errors.New("invalid Cursor protobuf frame"), "upstream_unavailable", "The model service is temporarily unavailable."},
	} {
		code, message := messagesPublicError(tc.err)
		require.Equal(t, tc.code, code)
		require.Equal(t, tc.message, message)
	}
	resp, err := Messages(t.Context(), "test", []byte(`{"model":"composer-2.5","messages":[{"role":"user","content":[{"type":"Cursor-private-block"}]}]}`), nil, "composer-2.5", func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid request reached upstream")
		return nil, nil
	})
	require.NoError(t, err)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	var payload struct {
		Error struct{ Code, Message string }
	}
	require.NoError(t, json.Unmarshal(raw, &payload))
	require.Equal(t, "invalid_request_error", payload.Error.Code)
	require.Equal(t, "Invalid request.", payload.Error.Message)
}

func TestToolIdentityDiagnosticsRedactCredentialsAndOmitArguments(t *testing.T) {
	const token = "private-credential"
	core, logs := observer.New(zap.DebugLevel)
	ctx := logger.IntoContext(t.Context(), zap.New(core))
	_, err := RunAgent(ctx, token, AgentRequest{Model: "composer-2.5", Messages: []AgentMessage{{Role: "user", Text: "hello"}}}, messagesTestTransport(t,
		&pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{ToolCallStarted: &pb.ToolCallStartedUpdate{ToolCall: &pb.ToolCall{McpToolCall: &pb.McpToolCall{Args: &pb.McpArgs{Name: "unknown-" + token, ToolCallId: "call", ProviderIdentifier: "untrusted", Args: map[string]*structpb.Value{"secret": structpb.NewStringValue("private-arguments")}}}}}}},
	), nil)
	require.ErrorIs(t, err, errAgentToolProtocol)
	entries := logs.FilterMessage("cursor_agentrun_tool_rejected").All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	require.Equal(t, "unknown-[redacted]", fields["raw_name"])
	require.EqualValues(t, 0, fields["declared_tool_count"])
	require.Equal(t, false, fields["declared"])
	require.Equal(t, "", fields["normalized_name"])
	serialized, err := json.Marshal(fields)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), token)
	require.NotContains(t, string(serialized), "private-arguments")
}
