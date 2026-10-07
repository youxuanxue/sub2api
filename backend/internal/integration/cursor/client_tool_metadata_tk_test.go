package cursor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestClientToolMetadataSnapshot(t *testing.T) {
	input := AgentRequest{Model: "composer-2.5", Tools: []AgentTool{nativeTestTool("Bash", "command")}, Messages: []AgentMessage{{Role: "user", Text: "task"}}}
	input.Tools[0].Description = "Caller-owned execution policy."
	run, _, err := buildAgentRun(input)
	require.NoError(t, err)
	tools := run.McpTools.McpTools
	for _, kick := range []bool{false, true} {
		for _, names := range [][]string{nil, {"tokenkey"}, {"other"}, {"tokenkey", "tokenkey", "other"}} {
			exec := &pb.ExecServerMessage{Id: 7, ExecId: "metadata", McpStateExecArgs: &pb.McpStateExecArgs{ServerIdentifiers: names, KickOnly: kick}}
			replies := clientToolState(exec, tools)
			require.Len(t, replies, 2)
			reply := replies[0].ExecClientMessage
			require.EqualValues(t, 7, reply.Id)
			require.Equal(t, "metadata", reply.ExecId)
			servers := reply.McpStateExecResult.Success.Servers
			if len(names) == 1 && names[0] == "other" {
				require.Empty(t, servers)
			} else {
				require.Len(t, servers, 1)
				require.Equal(t, "tokenkey", servers[0].ServerIdentifier)
				require.Equal(t, "ready", servers[0].GetStatus())
				require.Len(t, servers[0].Tools, 1)
				require.True(t, proto.Equal(tools[0], servers[0].Tools[0]), "metadata must preserve the caller schema and description")
			}
			require.EqualValues(t, 7, replies[1].ExecClientControlMessage.StreamClose.Id)
			require.Empty(t, clientToolState(exec, nil)[0].ExecClientMessage.McpStateExecResult.Success.Servers, "no previous request's declarations")
		}
	}
	// Literal official wire fields: exec id=7, mcp_state_exec_args=36,
	// server_identifiers=1, kick_only=2. A codec roundtrip alone cannot pin tags.
	var decoded pb.ExecServerMessage
	require.NoError(t, proto.Unmarshal([]byte{8, 7, 0xa2, 2, 12, 10, 8, 't', 'o', 'k', 'e', 'n', 'k', 'e', 'y', 16, 1}, &decoded))
	require.Equal(t, []string{"tokenkey"}, decoded.McpStateExecArgs.ServerIdentifiers)
	require.True(t, decoded.McpStateExecArgs.KickOnly)
	decoded.ReadArgs = &pb.ReadArgs{Path: "/gateway/private"}
	replies := clientToolState(&decoded, tools)
	require.Nil(t, replies[0].ExecClientMessage)
	require.Equal(t, publicToolProtocolCode, replies[0].ExecClientControlMessage.Throw.GetErrorCode())
}

func TestClientToolMetadataContinuesToClientHandoff(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var calls atomic.Int32
			steps := []nativeFixtureStep{}
			for _, kick := range []bool{true, false} {
				steps = append(steps, nativeFixtureStep{exec: &pb.ExecServerMessage{Id: uint32(len(steps) + 1), McpStateExecArgs: &pb.McpStateExecArgs{KickOnly: kick}}, check: func(reply *pb.ExecClientMessage) error {
					servers := reply.GetMcpStateExecResult().GetSuccess().GetServers()
					if len(servers) != 1 || len(servers[0].Tools) != 1 || servers[0].Tools[0].ToolName != "Bash" {
						return fmt.Errorf("request-declared tool metadata lost")
					}
					return nil
				}})
			}
			steps = append(steps, nativeFixtureStep{exec: clientBashExec(t, 3, "pwd"), check: func(reply *pb.ExecClientMessage) error {
				if reply.GetMcpResult().GetSuccess().GetContent()[0].GetText().GetText() != "client output" {
					return fmt.Errorf("client result lost")
				}
				return nil
			}})
			fixture := retainedFixture(t, steps, &calls)
			transport := func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("X-Cursor-Agent-Allowed-Tools") != "mcp_tool_call,get_mcp_tools_tool_call" {
					return nil, fmt.Errorf("client tool discovery dependency missing")
				}
				return fixture(req)
			}
			tool := nativeTestTool("Bash", "command")
			body := map[string]any{"model": "composer-2.5", "stream": stream, "tools": []any{map[string]any{"name": tool.Name, "input_schema": tool.Schema}}, "messages": []any{map[string]any{"role": "user", "content": "task"}}}
			ctx := WithRunOwner(t.Context(), RunOwner{16, 23, 150})
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			response, err := Messages(ctx, "test-token", raw, nil, "composer-2.5", transport)
			require.NoError(t, err)
			call, _ := readNativeResponse(t, response)
			require.Equal(t, "Bash", call.Name)
			require.Equal(t, map[string]any{"command": "pwd"}, call.Arguments)
			body["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": call.ID, "content": "client output"}}}}
			raw, err = json.Marshal(body)
			require.NoError(t, err)
			response, err = Messages(ctx, "test-token", raw, nil, "composer-2.5", transport)
			require.NoError(t, err)
			last, final := readNativeResponse(t, response)
			require.Empty(t, last.ID)
			require.Contains(t, final, "client work acknowledged")
			require.EqualValues(t, 1, calls.Load())
		})
	}
}
