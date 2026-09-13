package apicompat

import (
	"encoding/json"
	"fmt"
)

type GeminiMessagesPart struct {
	Text      *string             `json:"text,omitempty"`
	Thought   bool                `json:"thought,omitempty"`
	Signature string              `json:"thoughtSignature,omitempty"`
	Call      *GeminiFunctionCall `json:"functionCall,omitempty"`
}
type GeminiMessagesResponse struct {
	Candidates []GeminiMessagesCandidate `json:"candidates,omitempty"`
	Usage      *GeminiChatUsage          `json:"usageMetadata,omitempty"`
}
type GeminiMessagesCandidate struct {
	Content *struct {
		Role  string               `json:"role"`
		Parts []GeminiMessagesPart `json:"parts"`
	} `json:"content,omitempty"`
	Index        int    `json:"index"`
	FinishReason string `json:"finishReason,omitempty"`
}

func geminiMessagesResponse(parts []GeminiMessagesPart, finish string, usage *AnthropicUsage) (*GeminiMessagesResponse, error) {
	c := GeminiMessagesCandidate{}
	switch finish {
	case "":
	case "end_turn", "stop_sequence", "tool_use":
		c.FinishReason = "STOP"
	case "max_tokens":
		c.FinishReason = "MAX_TOKENS"
	case "refusal":
		c.FinishReason = "SAFETY"
	default:
		return nil, fmt.Errorf("unsupported Messages stop reason")
	}
	if len(parts) > 0 {
		c.Content = &struct {
			Role  string               `json:"role"`
			Parts []GeminiMessagesPart `json:"parts"`
		}{Role: "model", Parts: parts}
	}
	out := &GeminiMessagesResponse{}
	if len(parts) > 0 || finish != "" {
		out.Candidates = []GeminiMessagesCandidate{c}
	}
	if usage != nil {
		input := usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
		out.Usage = &GeminiChatUsage{Prompt: input, Candidates: usage.OutputTokens, Total: input + usage.OutputTokens, Cached: usage.CacheReadInputTokens}
	}
	return out, nil
}
func messagesPart(block AnthropicContentBlock) (GeminiMessagesPart, error) {
	p := GeminiMessagesPart{}
	switch block.Type {
	case "text":
		p.Text = &block.Text
	case "thinking":
		p.Text = &block.Thinking
		p.Thought = true
		p.Signature = encodeMessagesThoughtSignature(block.Signature)
	case "tool_use":
		if block.ID == "" || block.Name == "" || !geminiJSONObject(block.Input) {
			return p, fmt.Errorf("invalid Messages tool use")
		}
		p.Call = &GeminiFunctionCall{ID: block.ID, Name: block.Name, Args: block.Input}
	default:
		return p, fmt.Errorf("unsupported Messages content block")
	}
	return p, nil
}
func MessagesToGeminiResponse(body []byte) (*GeminiMessagesResponse, error) {
	var response AnthropicResponse
	if json.Unmarshal(body, &response) != nil || response.Type != "message" || response.Role != "assistant" || AnthropicStopReasonString(response.StopReason) == "" {
		return nil, fmt.Errorf("invalid Messages response")
	}
	parts := make([]GeminiMessagesPart, 0, len(response.Content))
	for _, b := range response.Content {
		p, err := messagesPart(b)
		if err != nil {
			return nil, err
		}
		parts = append(parts, p)
	}
	return geminiMessagesResponse(parts, AnthropicStopReasonString(response.StopReason), &response.Usage)
}

// MessagesToGeminiStream keeps one bounded Anthropic content block. Text is
// emitted immediately; tools and signed thoughts emit when their block closes.
// message_stop is mandatory, so a truncated stream cannot claim success.
type MessagesToGeminiStream struct {
	started   bool
	ended     bool
	stop      string
	usage     AnthropicUsage
	block     *AnthropicContentBlock
	nextIndex int
	jsonInput []byte
}

func (s *MessagesToGeminiStream) Done() bool { return s.ended }
func (s *MessagesToGeminiStream) Convert(data []byte) (*GeminiMessagesResponse, error) {
	var e AnthropicStreamEvent
	if json.Unmarshal(data, &e) != nil {
		return nil, fmt.Errorf("invalid Messages event")
	}
	if s.ended {
		return nil, fmt.Errorf("messages event after message_stop")
	}
	if e.Type == "ping" {
		return nil, nil
	}
	if e.Type == "message_start" {
		if s.started || e.Message == nil || e.Message.Type != "message" || e.Message.Role != "assistant" || len(e.Message.Content) != 0 {
			return nil, fmt.Errorf("invalid message_start")
		}
		s.started = true
		s.usage = e.Message.Usage
		return nil, nil
	}
	if !s.started {
		return nil, fmt.Errorf("messages event before message_start")
	}
	switch e.Type {
	case "content_block_start":
		if s.stop != "" || s.block != nil || e.Index == nil || *e.Index != s.nextIndex || e.ContentBlock == nil {
			return nil, fmt.Errorf("invalid content_block_start")
		}
		b := *e.ContentBlock
		switch b.Type {
		case "text", "thinking", "tool_use":
		default:
			return nil, fmt.Errorf("unsupported Messages block")
		}
		s.block = &b
		s.jsonInput = nil
		if b.Type == "text" && b.Text != "" {
			p, _ := messagesPart(b)
			return geminiMessagesResponse([]GeminiMessagesPart{p}, "", &s.usage)
		}
	case "content_block_delta":
		if s.block == nil || e.Index == nil || *e.Index != s.nextIndex || e.Delta == nil {
			return nil, fmt.Errorf("invalid content_block_delta")
		}
		d := e.Delta
		switch {
		case s.block.Type == "text" && d.Type == "text_delta":
			return geminiMessagesResponse([]GeminiMessagesPart{{Text: &d.Text}}, "", &s.usage)
		case s.block.Type == "thinking" && d.Type == "thinking_delta":
			s.block.Thinking += d.Thinking
		case s.block.Type == "thinking" && d.Type == "signature_delta":
			s.block.Signature += d.Signature
		case s.block.Type == "tool_use" && d.Type == "input_json_delta":
			s.jsonInput = append(s.jsonInput, d.PartialJSON...)
		default:
			return nil, fmt.Errorf("messages delta mismatches block")
		}
		if len(s.jsonInput)+len(s.block.Thinking)+len(s.block.Signature) > 4*1024*1024 {
			return nil, fmt.Errorf("messages block exceeds conversion limit")
		}
	case "content_block_stop":
		if s.block == nil || e.Index == nil || *e.Index != s.nextIndex {
			return nil, fmt.Errorf("invalid content_block_stop")
		}
		b := *s.block
		s.block = nil
		s.nextIndex++
		if b.Type == "text" {
			return nil, nil
		}
		if b.Type == "tool_use" && len(s.jsonInput) > 0 {
			b.Input = s.jsonInput
		}
		s.jsonInput = nil
		p, err := messagesPart(b)
		if err != nil {
			return nil, err
		}
		return geminiMessagesResponse([]GeminiMessagesPart{p}, "", &s.usage)
	case "message_delta":
		if s.block != nil || s.stop != "" || e.Delta == nil || e.Delta.StopReason == "" {
			return nil, fmt.Errorf("invalid message_delta")
		}
		if _, err := geminiMessagesResponse(nil, e.Delta.StopReason, nil); err != nil {
			return nil, err
		}
		s.stop = e.Delta.StopReason
		if e.Usage != nil {
			s.usage.OutputTokens = e.Usage.OutputTokens
		}
	case "message_stop":
		if s.block != nil || s.stop == "" {
			return nil, fmt.Errorf("incomplete Messages stream")
		}
		s.ended = true
		return geminiMessagesResponse(nil, s.stop, &s.usage)
	default:
		return nil, fmt.Errorf("unsupported Messages event")
	}
	return nil, nil
}
