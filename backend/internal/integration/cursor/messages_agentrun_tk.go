package cursor

import (
	"sort"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Messages ↔ AgentRun conversion boundary (Cursor supply #150).
//
// AgentRun sits at the same gateway level as Messages/Chat/Responses, but it is
// Cursor's agent harness wire — not a peer public API. TokenKey therefore:
//
//   ingress (Messages → AgentRun): buildAgentRun / relayRequestContext / AGENT mode
//     map only Messages-shaped history, system, and declared tools onto a trimmed
//     RunAgent request. No workspace agent runtime is advertised.
//
//   egress (AgentRun → Messages): map only surfaces Messages can express:
//     text / thinking deltas, terminal usage, and declared-tool McpArgs (or
//     InteractionUpdate tool_call_started carrying McpToolCall) as tool_use
//     handoff. RequestContext + history KV stay protocol-internal.
//     Native Read/Write/Shell/Grep (including supported Pi variants) become
//     declared, schema-validated client tool_use. Unmapped execs receive
//     ExecClientThrow + stream_close. No tool executes on the gateway. Interaction queries and non-MCP
//     tool_call_started envelopes are skipped without failing the Messages turn.

// execClientThrowAndClose answers an unserviceable ExecServerMessage in-band so
// Cursor can surface the error to the model and keep generating instead of
// blocking on a missing ExecClientMessage result (oh-my-pi / CLI pattern).
func execClientThrowAndClose(exec *pb.ExecServerMessage, errText, errCode string) []*pb.AgentClientMessage {
	if exec == nil {
		return nil
	}
	code := errCode
	throw := &pb.ExecClientThrow{Id: exec.Id, Error: errText, ErrorCode: &code}
	return []*pb.AgentClientMessage{
		{ExecClientControlMessage: &pb.ExecClientControlMessage{Throw: throw}},
		{ExecClientControlMessage: &pb.ExecClientControlMessage{StreamClose: &pb.ExecClientStreamClose{Id: exec.Id}}},
	}
}

func outsideExecThrowMessage() (errText, errCode string) {
	return "Workspace tools are unavailable through this gateway. Continue without local execution.", publicToolUnavailableCode
}

// protobufFieldNumbers lists set known fields plus unknown wire tags on m.
func protobufFieldNumbers(m proto.Message) []int {
	if m == nil {
		return nil
	}
	seen := map[int]struct{}{}
	var out []int
	add := func(n int) {
		if n <= 0 {
			return
		}
		if _, ok := seen[n]; ok {
			return
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	msg := m.ProtoReflect()
	msg.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		add(int(fd.Number()))
		return true
	})
	unknown := msg.GetUnknown()
	for len(unknown) > 0 {
		num, typ, n := protowire.ConsumeTag(unknown)
		if n < 0 {
			break
		}
		unknown = unknown[n:]
		add(int(num))
		n = protowire.ConsumeFieldValue(num, typ, unknown)
		if n < 0 {
			break
		}
		unknown = unknown[n:]
	}
	sort.Ints(out)
	return out
}

// messagesAlignedExecAction classifies an ExecServerMessage against the Messages surface.
// request_context stays internal; mcp_args becomes tool_use; anything else is outside.
type messagesAlignedExecAction int

const (
	messagesExecRequestContext messagesAlignedExecAction = iota
	messagesExecToolUse
	messagesExecOutside
)

func classifyMessagesAlignedExec(exec *pb.ExecServerMessage) messagesAlignedExecAction {
	if exec == nil {
		return messagesExecOutside
	}
	switch {
	case exec.RequestContextArgs != nil:
		return messagesExecRequestContext
	case exec.McpArgs != nil:
		return messagesExecToolUse
	default:
		return messagesExecOutside
	}
}

// mcpArgsFromInteractionToolCall extracts Messages-mappable tool args from an
// InteractionUpdate tool_call_started/completed envelope, if present.
func mcpArgsFromInteractionToolCall(update *pb.ToolCallStartedUpdate) *pb.McpArgs {
	if update == nil || update.ToolCall == nil || update.ToolCall.McpToolCall == nil {
		return nil
	}
	return update.ToolCall.McpToolCall.Args
}
