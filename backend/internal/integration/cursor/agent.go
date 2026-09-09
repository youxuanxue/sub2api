package cursor

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const AgentBaseURL = "https://agentn.global.api5.cursor.sh"
const AgentClientVersion = "cli-2026.09.02-c22c1a3"
const agentRunPath = "/agent.v1.AgentService/Run"
const maxAgentFrame = 4 << 20
const maxAgentBlobs = 16 << 20
const maxAgentOutput = 4 << 20

// AgentMessage is request history, supplied by the caller on every request.
// No run id, upstream checkpoint, or previous connection is accepted.
type AgentMessage struct {
	Role       string
	Text       string
	ToolCalls  []AgentToolCall
	ToolCallID string
	ToolName   string
	IsError    bool
}
type AgentToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}
type AgentTool struct {
	Name        string
	Description string
	Schema      map[string]any
}
type AgentRequest struct {
	System     string
	Model      string
	WireModel  string
	Parameters []Parameter
	Messages   []AgentMessage
	Tools      []AgentTool
}
type AgentUsage struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
	Reasoning  int64 `json:"reasoning_tokens"`
}
type AgentResult struct {
	Thinking    string          `json:"thinking,omitempty"`
	Text        string          `json:"text"`
	ToolCalls   []AgentToolCall `json:"tool_calls,omitempty"`
	Usage       *AgentUsage     `json:"usage,omitempty"`
	ToolHandoff bool            `json:"tool_handoff"`
}
type AgentEvent struct {
	Thinking string
	Text     string
	ToolCall *AgentToolCall
}

type agentBlobs struct {
	data map[string][]byte
	size int
}

func (b *agentBlobs) put(id, value []byte) error {
	if len(id) != sha256.Size || len(value) > maxAgentFrame {
		return errors.New("invalid Cursor blob")
	}
	key := string(id)
	size := b.size - len(b.data[key]) + len(value)
	if size > maxAgentBlobs || (len(b.data) >= 4096 && b.data[key] == nil) {
		return errors.New("cursor request blob limit exceeded")
	}
	b.data[key] = bytes.Clone(value)
	b.size = size
	return nil
}
func (b *agentBlobs) store(value []byte) ([]byte, error) {
	id := sha256.Sum256(value)
	return id[:], b.put(id[:], value)
}
func (b *agentBlobs) storeProto(value proto.Message) ([]byte, error) {
	raw, err := proto.Marshal(value)
	if err != nil {
		return nil, err
	}
	return b.store(raw)
}

func cursorWireToolName(name string) string { return "mcp__tokenkey__" + name }

func agentCallArgs(call AgentToolCall) (*pb.McpArgs, error) {
	args, err := structpb.NewStruct(call.Arguments)
	if err != nil {
		return nil, errors.New("invalid Cursor tool arguments")
	}
	return &pb.McpArgs{Name: cursorWireToolName(call.Name), ToolName: call.Name,
		ProviderIdentifier: "tokenkey", ToolCallId: call.ID, Args: args.Fields}, nil
}

func buildAgentRun(input AgentRequest) (*pb.AgentRunRequest, *agentBlobs, error) {
	if input.Model == "" || input.Model == "default" || input.Model == "auto" || len(input.Messages) == 0 {
		return nil, nil, errors.New("cursor requires a fixed model and messages")
	}
	if len(input.Messages) > 4096 || len(input.Tools) > 256 {
		return nil, nil, errors.New("cursor request item limit exceeded")
	}
	blobs := &agentBlobs{data: make(map[string][]byte)}
	state := &pb.ConversationStateStructure{Mode: 1}
	root := func(value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		id, err := blobs.store(raw)
		if err != nil {
			return err
		}
		state.RootPromptMessagesJson = append(state.RootPromptMessagesJson, id)
		return nil
	}
	// Cursor owns these two slots. Real history follows them.
	for _, role := range []string{"system", "user"} {
		if err := root(map[string]any{"role": role, "content": ""}); err != nil {
			return nil, nil, err
		}
	}
	if input.System != "" {
		if err := root(map[string]any{"role": "system", "content": input.System}); err != nil {
			return nil, nil, err
		}
	}
	if strings.TrimSpace(input.System) != "" {
		firstUserPrepared := false
		var preparedMessages []AgentMessage
		for _, m := range input.Messages {
			if m.Role == "user" && !firstUserPrepared {
				firstUserPrepared = true
				m.Text = strings.TrimSpace(input.System) + "\n\n" + m.Text
			}
			preparedMessages = append(preparedMessages, m)
		}
		input.Messages = preparedMessages
	}
	active := len(input.Messages) - 1
	if input.Messages[active].Role != "user" {
		active = -1
	}
	contextInfo := &pb.RequestContext{Env: &pb.Empty{}, GitRepoInfoComplete: true, McpInfoComplete: true,
		RulesInfoComplete: true, EnvInfoComplete: true, RepositoryInfoComplete: true, CustomSubagentsInfoComplete: true,
		AgentSkillsInfoComplete: true, McpFileSystemInfoComplete: true, GitStatusInfoComplete: true}
	if input.System != "" {
		contextInfo.NonFileRules = []*pb.CursorRule{{Content: input.System, Source: 2, Type: &pb.CursorRuleType{Global: &pb.Empty{}}}}
	}
	run := &pb.AgentRunRequest{ConversationState: state, ConversationId: uuid.NewString(),
		ModelDetails: &pb.ModelDetails{ModelId: input.Model}, RequestedModel: &pb.RequestedModel{ModelId: input.Model},
		Action: &pb.ConversationAction{ResumeAction: &pb.ResumeAction{RequestContext: contextInfo}}}
	if input.WireModel != "" {
		run.ModelDetails.ModelId = input.WireModel
	}
	if input.System != "" {
		run.SystemPromptSpec = &pb.SystemPromptSpec{Append: &input.System}
	}
	for _, parameter := range input.Parameters {
		run.RequestedModel.Parameters = append(run.RequestedModel.Parameters, &pb.ModelParameter{Id: parameter.ID, Value: parameter.Value})
	}
	toolNames := make(map[string]bool)
	for _, tool := range input.Tools {
		if !validAgentToolName(tool.Name) || toolNames[tool.Name] {
			return nil, nil, errors.New("invalid or duplicate Cursor tool name")
		}
		toolNames[tool.Name] = true
		schema, err := structpb.NewValue(tool.Schema)
		if err != nil {
			return nil, nil, errors.New("invalid Cursor tool schema")
		}
		contextInfo.Tools = append(contextInfo.Tools, &pb.McpToolDefinition{Name: cursorWireToolName(tool.Name),
			ToolName: tool.Name, ProviderIdentifier: "tokenkey", Description: tool.Description, InputSchema: schema})
	}
	run.McpTools = &pb.McpTools{McpTools: contextInfo.Tools}
	results := make(map[string]AgentMessage)
	for _, message := range input.Messages {
		if message.Role == "tool" {
			if message.ToolCallID == "" || results[message.ToolCallID].Role != "" {
				return nil, nil, errors.New("duplicate or missing Cursor tool result id")
			}
			results[message.ToolCallID] = message
		}
	}
	var turn *pb.AgentConversationTurnStructure
	flushTurn := func() error {
		if turn == nil {
			return nil
		}
		id, err := blobs.storeProto(&pb.ConversationTurnStructure{AgentConversationTurn: turn})
		if err != nil {
			return err
		}
		state.Turns = append(state.Turns, id)
		turn = nil
		return nil
	}
	paired := make(map[string]string)
	for index, message := range input.Messages {
		if index == active {
			if strings.TrimSpace(message.Text) == "" {
				return nil, nil, errors.New("empty Cursor user message")
			}
			run.Action = &pb.ConversationAction{UserMessageAction: &pb.UserMessageAction{
				UserMessage: &pb.UserMessage{Text: message.Text, MessageId: uuid.NewString(), Mode: 1}, RequestContext: contextInfo}}
			break
		}
		content := []map[string]any{}
		if message.Text != "" && message.Role != "tool" {
			content = append(content, map[string]any{"type": "text", "text": message.Text})
		}
		switch message.Role {
		case "user":
			if err := flushTurn(); err != nil {
				return nil, nil, err
			}
			id, err := blobs.storeProto(&pb.UserMessage{Text: message.Text, MessageId: uuid.NewString(), Mode: 1})
			if err != nil {
				return nil, nil, err
			}
			turn = &pb.AgentConversationTurnStructure{UserMessage: id}
		case "assistant":
			if turn == nil {
				return nil, nil, errors.New("cursor assistant history requires a user turn")
			}
			if message.Text != "" {
				id, err := blobs.storeProto(&pb.ConversationStep{AssistantMessage: &pb.AssistantMessage{Text: message.Text}})
				if err != nil {
					return nil, nil, err
				}
				turn.Steps = append(turn.Steps, id)
			}
			for _, call := range message.ToolCalls {
				result, ok := results[call.ID]
				if !ok || call.ID == "" || paired[call.ID] != "" || !validAgentToolName(call.Name) {
					return nil, nil, errors.New("cursor tool history requires unique paired calls and results")
				}
				paired[call.ID] = call.Name
				args, err := agentCallArgs(call)
				if err != nil {
					return nil, nil, err
				}
				step := &pb.ConversationStep{ToolCall: &pb.ToolCall{ToolCallId: call.ID, McpToolCall: &pb.McpToolCall{
					Args: args, Result: &pb.McpResult{Success: &pb.McpSuccess{IsError: result.IsError,
						Content: []*pb.McpToolResultContentItem{{Text: &pb.McpTextContent{Text: result.Text}}}}}}}}
				id, err := blobs.storeProto(step)
				if err != nil {
					return nil, nil, err
				}
				turn.Steps = append(turn.Steps, id)
				content = append(content, map[string]any{"type": "tool-call", "toolCallId": call.ID,
					"toolName": cursorWireToolName(call.Name), "args": call.Arguments})
			}
		case "tool":
			name := paired[message.ToolCallID]
			if name == "" {
				return nil, nil, errors.New("cursor tool result has no preceding call")
			}
			content = append(content, map[string]any{"type": "tool-result", "toolCallId": message.ToolCallID,
				"toolName": cursorWireToolName(name), "result": message.Text, "isError": message.IsError})
		default:
			return nil, nil, errors.New("unsupported Cursor history role")
		}
		if err := root(map[string]any{"role": message.Role, "content": content}); err != nil {
			return nil, nil, err
		}
	}
	if err := flushTurn(); err != nil {
		return nil, nil, err
	}
	if active < 0 && len(state.Turns) == 0 {
		return nil, nil, errors.New("cursor continuation requires full history")
	}
	for id, value := range blobs.data {
		run.PreFetchedBlobs = append(run.PreFetchedBlobs, &pb.PreFetchedBlob{Id: []byte(id), Value: value})
	}
	return run, blobs, nil
}

func validAgentToolName(name string) bool {
	if name == "" || len(name) > 100 {
		return false
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

func writeAgentFrame(writer io.Writer, message proto.Message) error {
	data, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	if len(data) > maxAgentBlobs {
		return errors.New("cursor outgoing frame too large")
	}
	header := [5]byte{}
	binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

func readAgentFrame(reader io.Reader) (byte, []byte, error) {
	header := [5]byte{}
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, nil, err
	}
	if header[0] & ^byte(3) != 0 {
		return 0, nil, errors.New("unknown Cursor frame flags")
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > maxAgentFrame {
		return 0, nil, errors.New("cursor incoming frame too large")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(reader, data); err != nil {
		return 0, nil, err
	}
	if header[0]&1 != 0 {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return 0, nil, errors.New("invalid Cursor gzip frame")
		}
		data, err = io.ReadAll(io.LimitReader(gz, maxAgentFrame+1))
		_ = gz.Close()
		if err != nil || len(data) > maxAgentFrame {
			return 0, nil, errors.New("invalid Cursor decompressed frame")
		}
	}
	return header[0], data, nil
}

// RunAgent executes one request using only request-local protocol state. do must
// support HTTP/2 duplex requests and must not retry after sending the request.
// A tool handoff is explicitly reported without fabricated provider usage.
func RunAgent(ctx context.Context, token string, input AgentRequest, do func(*http.Request) (*http.Response, error), emit func(AgentEvent) error) (result AgentResult, runErr error) {
	var textOutput, thinkingOutput strings.Builder
	defer func() { result.Text = textOutput.String(); result.Thinking = thinkingOutput.String() }()
	run, blobs, err := buildAgentRun(input)
	if err != nil {
		return result, err
	}
	requestContext := run.Action.GetUserMessageAction().GetRequestContext()
	if requestContext == nil {
		requestContext = run.Action.GetResumeAction().GetRequestContext()
	}
	if action := run.Action.GetUserMessageAction(); action != nil {
		action.RequestContext = nil
	}
	if action := run.Action.GetResumeAction(); action != nil {
		action.RequestContext = nil
	}
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return result, errors.New("invalid Cursor access token")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	reader, writer := io.Pipe()
	queue := make(chan *pb.AgentClientMessage, 32)
	writerDone := make(chan struct{})
	stopClosing := context.AfterFunc(ctx, func() { _ = reader.CloseWithError(ctx.Err()); _ = writer.CloseWithError(ctx.Err()) })
	defer func() { cancel(); stopClosing(); _ = reader.Close(); _ = writer.Close(); <-writerDone }()
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case message := <-queue:
				if err := writeAgentFrame(writer, message); err != nil {
					_ = writer.CloseWithError(err)
					return
				}
			}
		}
	}()
	send := func(message *pb.AgentClientMessage) error {
		select {
		case queue <- message:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, AgentBaseURL+agentRunPath, reader)
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/connect+proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Connect-Accept-Encoding", "gzip")
	req.Header.Set("X-Cursor-Client-Version", AgentClientVersion)
	req.Header.Set("X-Cursor-Client-Type", "cli")
	req.Header.Set("X-Cursor-Agent-Allowed-Tools", "mcp_tool_call")
	req.Header.Set("X-Ghost-Mode", "true")
	req.Header.Set("X-Request-Id", uuid.NewString())
	if err := send(&pb.AgentClientMessage{RunRequest: run}); err != nil {
		return result, err
	}
	resp, err := do(req)
	if err != nil {
		return result, errors.New("cursor upstream transport failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return result, &Error{Status: resp.StatusCode, Message: fmt.Sprintf("cursor upstream returned HTTP %d", resp.StatusCode)}
	}
	if resp.ProtoMajor != 2 {
		return result, errors.New("cursor requires HTTP/2 duplex transport")
	}
	seenCalls := make(map[string]bool)
	toolOutputBytes := 0
	knownTools := make(map[string]string)
	for _, tool := range input.Tools {
		knownTools[cursorWireToolName(tool.Name)] = tool.Name
	}
	publishCall := func(args *pb.McpArgs) error {
		name := knownTools[args.Name]
		if name == "" || args.ToolCallId == "" {
			return errors.New("cursor requested an undeclared tool")
		}
		if seenCalls[args.ToolCallId] {
			return nil
		}
		seenCalls[args.ToolCallId] = true
		call := AgentToolCall{ID: args.ToolCallId, Name: name, Arguments: make(map[string]any)}
		for key, value := range args.Args {
			call.Arguments[key] = value.AsInterface()
		}
		raw, err := json.Marshal(call)
		if err != nil || len(result.ToolCalls) >= 128 || textOutput.Len()+thinkingOutput.Len()+toolOutputBytes+len(raw) > maxAgentOutput {
			return errors.New("cursor tool output limit exceeded")
		}
		toolOutputBytes += len(raw)
		result.ToolCalls = append(result.ToolCalls, call)
		if emit != nil {
			return emit(AgentEvent{ToolCall: &call})
		}
		return nil
	}
	for frames := 0; frames < 20000; frames++ {
		flag, data, err := readAgentFrame(resp.Body)
		if err != nil {
			return result, fmt.Errorf("cursor stream interrupted: %w", err)
		}
		if flag&2 != 0 {
			var trailer struct {
				Error *struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(data, &trailer) != nil {
				return result, errors.New("invalid Cursor terminal frame")
			}
			if result.ToolHandoff && trailer.Error != nil && trailer.Error.Code == "canceled" {
				return result, nil
			}
			if trailer.Error != nil {
				status := http.StatusBadGateway
				switch trailer.Error.Code {
				case "unauthenticated":
					status = 401
				case "permission_denied":
					status = 403
				case "resource_exhausted":
					status = 429
				case "invalid_argument":
					status = 400
				}
				return result, &Error{Status: status, Message: fmt.Sprintf("cursor upstream rejected request (HTTP %d)", status)}
			}
			return result, errors.New("cursor stream ended without terminal usage")
		}
		message := &pb.AgentServerMessage{}
		if proto.Unmarshal(data, message) != nil {
			return result, errors.New("invalid Cursor protobuf frame")
		}
		if kv := message.KvServerMessage; kv != nil {
			reply := &pb.KvClientMessage{Id: kv.Id}
			switch {
			case kv.SetBlobArgs != nil:
				if err := blobs.put(kv.SetBlobArgs.BlobId, kv.SetBlobArgs.BlobData); err != nil {
					return result, err
				}
				reply.SetBlobResult = &pb.Empty{}
			case kv.GetBlobArgs != nil:
				data, ok := blobs.data[string(kv.GetBlobArgs.BlobId)]
				if !ok {
					return result, errors.New("cursor requested an unknown history blob")
				}
				reply.GetBlobResult = &pb.GetBlobResult{BlobData: data}
			default:
				return result, errors.New("unsupported Cursor KV request")
			}
			if err := send(&pb.AgentClientMessage{KvClientMessage: reply}); err != nil {
				return result, err
			}
		}
		if update := message.InteractionUpdate; update != nil {
			if update.ThinkingDelta != nil && !result.ToolHandoff {
				thinking := update.ThinkingDelta.Text
				if thinkingOutput.Len()+textOutput.Len()+toolOutputBytes+len(thinking) > maxAgentOutput {
					return result, errors.New("cursor output limit exceeded")
				}
				_, _ = thinkingOutput.WriteString(thinking)
				if emit != nil {
					if err := emit(AgentEvent{Thinking: thinking}); err != nil {
						return result, err
					}
				}
			}
			if update.TextDelta != nil && !result.ToolHandoff {
				text := update.TextDelta.Text
				if thinkingOutput.Len()+textOutput.Len()+toolOutputBytes+len(text) > maxAgentOutput {
					return result, errors.New("cursor output limit exceeded")
				}
				_, _ = textOutput.WriteString(text)
				if emit != nil {
					if err := emit(AgentEvent{Text: text}); err != nil {
						return result, err
					}
				}
			}
			if usage := update.TurnEnded; usage != nil {
				if usage.GetInputTokens() < 0 || usage.GetOutputTokens() < 0 || usage.GetCacheReadTokens() < 0 || usage.GetCacheWriteTokens() < 0 || usage.GetReasoningTokens() < 0 {
					return result, errors.New("invalid Cursor usage")
				}
				if usage.InputTokens == nil || usage.OutputTokens == nil || usage.CacheReadTokens == nil || usage.CacheWriteTokens == nil {
					if result.ToolHandoff {
						return result, nil
					}
					return result, errors.New("cursor returned incomplete terminal usage")
				}
				result.Usage = &AgentUsage{Input: usage.GetInputTokens(), Output: usage.GetOutputTokens(),
					CacheRead: usage.GetCacheReadTokens(), CacheWrite: usage.GetCacheWriteTokens(), Reasoning: usage.GetReasoningTokens()}
				return result, nil
			}
		}
		if exec := message.ExecServerMessage; exec != nil {
			switch {
			case exec.RequestContextArgs != nil:
				if err := send(&pb.AgentClientMessage{ExecClientMessage: &pb.ExecClientMessage{Id: exec.Id, ExecId: exec.ExecId,
					RequestContextResult: &pb.RequestContextResult{Success: &pb.RequestContextSuccess{RequestContext: requestContext}}}}); err != nil {
					return result, err
				}
			case exec.McpArgs != nil:
				if exec.McpArgs.SmartModeApprovalOnly {
					return result, errors.New("cursor tool approval requires an external policy decision")
				}
				if err := publishCall(exec.McpArgs); err != nil {
					return result, err
				}
				if !result.ToolHandoff {
					result.ToolHandoff = true
					if err := send(&pb.AgentClientMessage{ConversationAction: &pb.ConversationAction{CancelAction: &pb.CancelAction{Reason: "External client tool handoff"}}}); err != nil {
						return result, err
					}
				}
			default:
				return result, errors.New("cursor requested unsupported local execution")
			}
		}
		if message.InteractionQuery != nil {
			return result, errors.New("cursor requested unsupported interaction")
		}
	}
	return result, errors.New("cursor frame count limit exceeded")
}
