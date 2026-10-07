package cursor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func clientBashExec(t *testing.T, id uint32, command string) *pb.ExecServerMessage {
	t.Helper()
	args, err := agentCallArgs(AgentToolCall{ID: fmt.Sprint(id), Name: "Bash", Arguments: map[string]any{"command": command}})
	require.NoError(t, err)
	return &pb.ExecServerMessage{Id: id, ExecId: fmt.Sprint(id), McpArgs: args}
}

func TestClientToolRoutingPreservesDeclarations(t *testing.T) {
	for _, model := range []string{"composer-2.5", "claude-fable-5-1"} {
		input := AgentRequest{Model: model, System: "Keep caller rules.", Tools: []AgentTool{nativeTestTool("Bash", "command")}, Messages: []AgentMessage{{Role: "user", Text: "run a command"}}}
		input.Tools[0].Description = "Client permissions and environment policy."
		run, _, err := buildAgentRun(input)
		require.NoError(t, err)
		require.Equal(t, input.System, run.SystemPromptSpec.GetAppend(), "routing must not rewrite caller instructions")
		tool := run.McpTools.McpTools[0]
		require.Equal(t, "Bash", tool.ToolName)
		require.Equal(t, input.Tools[0].Description, tool.Description)
		require.Equal(t, input.Tools[0].Schema, tool.InputSchema.AsInterface())
		require.Equal(t, "run a command", input.Messages[0].Text)
		require.Equal(t, "Keep caller rules.", input.System)
	}
}

func TestClientToolFilterConstrainsUpstreamWithoutDeclarations(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
	}{
		{name: "omitted_tools", body: map[string]any{"model": "composer-2.5", "messages": []any{map[string]any{"role": "user", "content": "hello"}}}},
		{name: "empty_tools", body: map[string]any{"model": "composer-2.5", "tools": []any{}, "messages": []any{map[string]any{"role": "user", "content": "hello"}}}},
		{name: "tool_choice_none", body: map[string]any{"model": "composer-2.5", "tool_choice": map[string]any{"type": "none"}, "tools": []any{map[string]any{"name": "Bash", "input_schema": map[string]any{"type": "object"}}}, "messages": []any{map[string]any{"role": "user", "content": "hello"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.body)
			require.NoError(t, err)
			input, _, err := parseMessagesContent(raw)
			require.NoError(t, err)
			run, _, err := buildAgentRun(input)
			require.NoError(t, err)
			require.Empty(t, run.GetMcpTools().GetMcpTools(), "tool-free requests keep an empty client tool catalog")
			var frames bytes.Buffer
			require.NoError(t, writeAgentFrame(&frames, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TurnEnded: &pb.TurnEndedUpdate{
				InputTokens: proto.Int64(1), OutputTokens: proto.Int64(1), CacheReadTokens: proto.Int64(0), CacheWriteTokens: proto.Int64(0),
			}}}))
			var allowed string
			_, err = RunAgent(t.Context(), "test-token", input, func(req *http.Request) (*http.Response, error) {
				allowed = req.Header.Get("X-Cursor-Agent-Allowed-Tools")
				return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: io.NopCloser(&frames)}, nil
			}, nil)
			require.NoError(t, err)
			require.Equal(t, clientToolAllowlist, allowed, "tool-free AGENT runs must still exclude native Shell")
		})
	}
}

func TestClientToolAllowlistDoesNotLearnOrReplay(t *testing.T) {
	input := AgentRequest{Model: "composer-2.5", Tools: []AgentTool{nativeTestTool("Bash", "command")}, Messages: []AgentMessage{{Role: "user", Text: "task"}}}
	var calls atomic.Int32
	var allowed string
	for attempt := 1; attempt <= 2; attempt++ {
		_, err := RunAgent(t.Context(), "test-token", input, func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			allowed = req.Header.Get("X-Cursor-Agent-Allowed-Tools")
			return &http.Response{StatusCode: 400, ProtoMajor: 2, Body: io.NopCloser(bytes.NewBufferString(`{"error":"Required tool SHELL not found in allTools"}`))}, nil
		}, nil)
		require.Error(t, err)
		require.EqualValues(t, attempt, calls.Load(), "an upstream error must not trigger another Run")
		require.Equal(t, "mcp_tool_call,get_mcp_tools_tool_call", allowed, "upstream errors must not expand this or later requests' tool allowlist")
	}
}

func TestClientBashRejectsInvalidArguments(t *testing.T) {
	for _, args := range []map[string]any{{}, {"command": 7}, {"command": "pwd", "skip_approval": true}} {
		wire, err := agentCallArgs(AgentToolCall{ID: "call", Name: "Bash", Arguments: args})
		require.NoError(t, err)
		var frames bytes.Buffer
		require.NoError(t, writeAgentFrame(&frames, &pb.AgentServerMessage{ExecServerMessage: &pb.ExecServerMessage{McpArgs: wire}}))
		result, err := RunAgent(t.Context(), "test-token", AgentRequest{Model: "composer-2.5", Tools: []AgentTool{nativeTestTool("Bash", "command")}, Messages: []AgentMessage{{Role: "user", Text: "run"}}}, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: io.NopCloser(&frames)}, nil
		}, nil)
		require.ErrorIs(t, err, errAgentToolProtocol)
		require.Empty(t, result.ToolCalls)
	}
}

func TestClientBashPreservesCallerSchemaSemantics(t *testing.T) {
	for _, schema := range []map[string]any{
		{"type": "object"},
		{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "additionalProperties": map[string]any{"type": "string"}},
		{"$ref": "#/$defs/args", "$defs": map[string]any{"args": map[string]any{"type": "object"}}},
	} {
		args := map[string]any{"command": "pwd", "client_label": "diagnostic"}
		wire, err := agentCallArgs(AgentToolCall{ID: "call", Name: "Bash", Arguments: args})
		require.NoError(t, err)
		var frames bytes.Buffer
		require.NoError(t, writeAgentFrame(&frames, &pb.AgentServerMessage{ExecServerMessage: &pb.ExecServerMessage{McpArgs: wire}}))
		// The non-retained path ends at a tool handoff; cancellation is its
		// established transport behavior, independent of schema validation.
		require.NoError(t, writeAgentFrame(&frames, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TurnEnded: &pb.TurnEndedUpdate{}}}))
		result, err := RunAgent(t.Context(), "test-token", AgentRequest{Model: "composer-2.5", Tools: []AgentTool{{Name: "Bash", Schema: schema}}, Messages: []AgentMessage{{Role: "user", Text: "run"}}}, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: io.NopCloser(&frames)}, nil
		}, nil)
		require.NoError(t, err)
		require.Len(t, result.ToolCalls, 1)
		require.Equal(t, args, result.ToolCalls[0].Arguments)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	require.False(t, clientToolSchemaAccepts(map[string]any{"$ref": server.URL}, map[string]any{"command": "pwd"}))
	require.Zero(t, requests.Load(), "client schema references must never fetch external resources")
}

func TestNativeShellFailsBeforeClientHandoff(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	for _, stream := range []bool{false, true} {
		for name, exec := range map[string]*pb.ExecServerMessage{
			"shell":  {ShellArgs: &pb.ShellArgs{Command: "touch " + marker}},
			"stream": {ShellStreamArgs: &pb.ShellArgs{Command: "touch " + marker}},
			"mini":   {MiniSweAgentBashArgs: &pb.ShellArgs{Command: "touch " + marker}},
			"pi":     {PiBashArgs: &pb.PiBashExecArgs{Command: "touch " + marker}},
		} {
			t.Run(fmt.Sprintf("%s/stream=%t", name, stream), func(t *testing.T) {
				var calls atomic.Int32
				var frames bytes.Buffer
				require.NoError(t, writeAgentFrame(&frames, &pb.AgentServerMessage{ExecServerMessage: exec}))
				tool := nativeTestTool("Bash", "command")
				_, mapped := nativeClientToolCall(exec, []AgentTool{tool})
				require.False(t, mapped)
				_, mapped = nativeClientToolCall(exec, nil)
				require.False(t, mapped, "undeclared native Shell must not become a client tool")
				body, err := json.Marshal(map[string]any{"model": "composer-2.5", "stream": stream, "tools": []any{map[string]any{"name": tool.Name, "input_schema": tool.Schema}}, "messages": []any{map[string]any{"role": "user", "content": "run on client"}}})
				require.NoError(t, err)
				response, err := Messages(WithRunOwner(t.Context(), RunOwner{16, 23, 150}), "test-token", body, nil, "composer-2.5", func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: io.NopCloser(&frames)}, nil
				})
				require.NoError(t, err)
				raw, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode)
				require.Contains(t, string(raw), "gateway_tool_unavailable")
				require.NotContains(t, string(raw), `"type":"tool_use"`)
				require.NotContains(t, string(raw), "cursor")
				require.NotContains(t, string(raw), marker)
				require.EqualValues(t, 1, calls.Load(), "no replay or reselection request")
				_, err = os.Stat(marker)
				require.True(t, os.IsNotExist(err))
			})
		}
	}
}
