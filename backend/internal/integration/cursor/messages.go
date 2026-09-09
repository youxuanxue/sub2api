package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
)

type messagesInput struct {
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
	System   json.RawMessage `json:"system"`
	Messages []struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id"`
	} `json:"messages"`
	Tools []struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Schema      map[string]any `json:"input_schema"`
	} `json:"tools"`
	ToolChoice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"tool_choice"`
}
type messageBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func textContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []messageBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return "", errors.New("invalid Cursor text content")
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type != "text" {
			return "", errors.New("cursor currently accepts text content only")
		}
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, "\n"), nil
}

func parseMessages(body []byte, parameters []Parameter, wireModel string) (AgentRequest, bool, error) {
	var raw messagesInput
	var input AgentRequest
	if len(body) > 16<<20 || json.Unmarshal(body, &raw) != nil {
		return input, false, errors.New("invalid Cursor Messages request")
	}
	input.Model, input.WireModel, input.Parameters = raw.Model, wireModel, parameters
	var err error
	input.System, err = textContent(raw.System)
	if err != nil {
		return input, false, err
	}
	for _, tool := range raw.Tools {
		input.Tools = append(input.Tools, AgentTool{Name: tool.Name, Description: tool.Description, Schema: tool.Schema})
	}
	switch raw.ToolChoice.Type {
	case "", "auto":
	case "none":
		input.Tools = nil
	default:
		return input, false, errors.New("cursor supports tool_choice auto or none")
	}
	for _, message := range raw.Messages {
		if message.Role == "system" {
			text, err := textContent(message.Content)
			if err != nil {
				return input, false, err
			}
			if text != "" {
				if input.System != "" {
					input.System += "\n\n"
				}
				input.System += text
			}
			continue
		}
		if message.Role == "tool" {
			text, err := textContent(message.Content)
			if err != nil {
				return input, false, err
			}
			input.Messages = append(input.Messages, AgentMessage{Role: "tool", ToolCallID: message.ToolCallID, Text: text})
			continue
		}
		if message.Role != "user" && message.Role != "assistant" {
			return input, false, errors.New("invalid Cursor message role")
		}
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			input.Messages = append(input.Messages, AgentMessage{Role: message.Role, Text: text})
			continue
		}
		var blocks []messageBlock
		if json.Unmarshal(message.Content, &blocks) != nil {
			return input, false, errors.New("invalid Cursor message content")
		}
		current := AgentMessage{Role: message.Role}
		flush := func() {
			if current.Text != "" || len(current.ToolCalls) > 0 {
				input.Messages = append(input.Messages, current)
				current = AgentMessage{Role: message.Role}
			}
		}
		for _, block := range blocks {
			switch block.Type {
			case "text":
				if current.Text != "" {
					current.Text += "\n"
				}
				current.Text += block.Text
			case "tool_use":
				if message.Role != "assistant" {
					return input, false, errors.New("tool_use requires assistant role")
				}
				current.ToolCalls = append(current.ToolCalls, AgentToolCall{ID: block.ID, Name: block.Name, Arguments: block.Input})
			case "tool_result":
				if message.Role != "user" {
					return input, false, errors.New("tool_result requires user role")
				}
				flush()
				text, err := textContent(block.Content)
				if err != nil {
					return input, false, err
				}
				input.Messages = append(input.Messages, AgentMessage{Role: "tool", ToolCallID: block.ToolUseID, Text: text, IsError: block.IsError})
			default:
				return input, false, errors.New("unsupported Cursor content block: " + block.Type)
			}
		}
		flush()
	}
	if _, _, err := buildAgentRun(input); err != nil {
		return input, false, err
	}
	return input, raw.Stream, nil
}

// MessagesBody owns one request's output pipe and settlement evidence. Closing
// it cancels the native call and joins the producer; it never parks a run.
type MessagesBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	tier   string
	err    error
}

func (b *MessagesBody) Close() error {
	b.cancel()
	err := b.ReadCloser.Close()
	<-b.done
	return err
}
func (b *MessagesBody) Outcome() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tier, b.err
}
func messageUsage(u AgentUsage, tier string) map[string]any {
	usage := map[string]any{"input_tokens": u.Input, "output_tokens": u.Output, "cache_read_input_tokens": u.CacheRead, "cache_creation_input_tokens": u.CacheWrite}
	if tier != "" {
		usage["tk_billing_tier"] = tier
	}
	return usage
}
func messageContent(result AgentResult) []map[string]any {
	content := make([]map[string]any, 0, len(result.ToolCalls)+1)
	if result.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": result.Text})
	}
	for _, call := range result.ToolCalls {
		content = append(content, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": call.Arguments})
	}
	return content
}

// Messages translates the supplier's native protocol once. The gateway keeps
// ownership of Chat/Responses conversion, candidate selection and billing.
func Messages(ctx context.Context, token string, body []byte, parameters []Parameter, wireModel string, do func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	input, stream, err := parseMessages(body, parameters, wireModel)
	if err != nil {
		return messagesError(http.StatusBadRequest, err.Error()), nil
	}
	ctx, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	output := &MessagesBody{ReadCloser: reader, cancel: cancel, done: make(chan struct{})}
	ready := make(chan *http.Response, 1)
	id := "msg_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	go func() {
		defer close(output.done)
		defer cancel()
		defer func() { _ = writer.Close() }()
		started, blockOpen := false, false
		index := 0
		event := func(kind string, value map[string]any) error {
			value["type"] = kind
			raw, err := json.Marshal(value)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", kind, raw)
			return err
		}
		start := func() error {
			if started {
				return nil
			}
			started = true
			ready <- &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {id}}, Body: output}
			return event("message_start", map[string]any{"message": map[string]any{"id": id, "type": "message", "role": "assistant", "model": input.Model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": messageUsage(AgentUsage{}, "")}})
		}
		stopBlock := func() error {
			if !blockOpen {
				return nil
			}
			blockOpen = false
			err := event("content_block_stop", map[string]any{"index": index})
			index++
			return err
		}
		emit := func(delta AgentEvent) error {
			if !stream || (delta.Text == "" && delta.ToolCall == nil) {
				return nil
			}
			if err := start(); err != nil {
				return err
			}
			if delta.ToolCall != nil {
				if err := stopBlock(); err != nil {
					return err
				}
				call := delta.ToolCall
				if err := event("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": map[string]any{}}}); err != nil {
					return err
				}
				blockOpen = true
				raw, err := json.Marshal(call.Arguments)
				if err != nil {
					return err
				}
				if err := event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(raw)}}); err != nil {
					return err
				}
				return stopBlock()
			}
			if !blockOpen {
				if err := event("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": "text", "text": ""}}); err != nil {
					return err
				}
				blockOpen = true
			}
			return event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "text_delta", "text": delta.Text}})
		}
		result, runErr := RunAgent(ctx, token, input, do, emit)
		tier := ReportedBillingTier
		if runErr == nil && result.Usage == nil && result.ToolHandoff {
			u := EstimateHandoffUsage(input, result)
			result.Usage, tier = &u, EstimatedBillingTier
		}
		if runErr == nil && result.Usage == nil {
			runErr = errors.New("cursor returned no terminal usage")
		}
		output.mu.Lock()
		output.err = runErr
		if runErr == nil {
			output.tier = tier
		}
		output.mu.Unlock()
		if runErr != nil {
			slog.Error("cursor_messages_run_agent_failed", "err", runErr)
			if !started {
				status := http.StatusBadGateway
				var upstream *Error
				if errors.As(runErr, &upstream) {
					status = upstream.Status
				}
				ready <- messagesError(status, runErr.Error())
			} else {
				_ = event("error", map[string]any{"error": map[string]any{"type": "api_error", "message": "Cursor upstream stream failed"}})
			}
			_ = writer.CloseWithError(runErr)
			return
		}
		stop := "end_turn"
		if len(result.ToolCalls) > 0 {
			stop = "tool_use"
		}
		if !stream {
			ready <- &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {id}}, Body: output}
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": id, "type": "message", "role": "assistant", "model": input.Model, "content": messageContent(result), "stop_reason": stop, "stop_sequence": nil, "usage": messageUsage(*result.Usage, tier)})
			return
		}
		if err := start(); err != nil {
			return
		}
		if err := stopBlock(); err != nil {
			return
		}
		if err := event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": messageUsage(*result.Usage, tier)}); err != nil {
			return
		}
		_ = event("message_stop", map[string]any{})
	}()
	select {
	case response := <-ready:
		if response.Body != output {
			_ = output.Close()
		}
		return response, nil
	case <-ctx.Done():
		// Completion cancels ctx after publishing; prefer an already-ready error.
		select {
		case response := <-ready:
			if response.Body != output {
				_ = output.Close()
			}
			return response, nil
		default:
		}
		_ = output.Close()
		return nil, ctx.Err()
	}
}
func messagesError(status int, message string) *http.Response {
	raw, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": message}})
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw))}
}
