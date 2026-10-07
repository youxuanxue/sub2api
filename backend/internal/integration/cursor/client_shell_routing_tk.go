package cursor

import (
	"errors"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
)

var errNativeShellUnavailable = errors.New("native shell cannot represent client tool results")

// Fixed protocol entry points, never expanded from upstream error text. The
// discovery exec returns request declarations; it cannot start an MCP server.
const clientToolAllowlist = "mcp_tool_call,get_mcp_tools_tool_call"

func nativeShellExec(exec *pb.ExecServerMessage) bool {
	return exec.ShellArgs != nil || exec.ShellStreamArgs != nil || exec.MiniSweAgentBashArgs != nil || exec.PiBashArgs != nil
}
