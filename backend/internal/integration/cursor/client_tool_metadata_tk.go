package cursor

import (
	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"google.golang.org/protobuf/proto"
)

// clientToolState reports the already-declared transport namespace. It does not
// start, connect to, or inspect any server or client execution environment.
func clientToolState(exec *pb.ExecServerMessage, tools []*pb.McpToolDefinition) []*pb.AgentClientMessage {
	args := exec.GetMcpStateExecArgs()
	if args == nil {
		return nil
	}
	for _, field := range protobufFieldNumbers(exec) {
		switch field {
		case 1, 15, 19, 36, 55, 57: // IDs, tracing, metadata request, hook/machine hints
		default:
			return execClientThrowAndClose(exec, "Ambiguous tool metadata request.", publicToolProtocolCode)
		}
	}
	success := &pb.McpStateSuccess{}
	requested := len(args.ServerIdentifiers) == 0
	for _, name := range args.ServerIdentifiers {
		requested = requested || name == "tokenkey"
	}
	if requested && len(tools) > 0 {
		// Ready refers only to the static declaration set. Client permissions,
		// availability and process state are established by client tool_result.
		success.Servers = []*pb.McpStateServer{{ServerName: "Client tools", ServerIdentifier: "tokenkey", Tools: tools, Status: proto.String("ready")}}
	}
	// kick_only asks the CLI to start loading without waiting. This namespace
	// is already loaded from the request, so both forms return the same snapshot.
	return []*pb.AgentClientMessage{
		{ExecClientMessage: &pb.ExecClientMessage{Id: exec.Id, ExecId: exec.ExecId, McpStateExecResult: &pb.McpStateExecResult{Success: success}}},
		{ExecClientControlMessage: &pb.ExecClientControlMessage{StreamClose: &pb.ExecClientStreamClose{Id: exec.Id}}},
	}
}
