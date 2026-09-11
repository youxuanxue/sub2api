package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// GeminiToChatRequest is also the route's admission check. Decode the actual
// Gemini schema here: the generic request profile does not classify its parts.
func GeminiToChatRequest(body []byte, model string, stream bool) (*ChatCompletionsRequest, error) {
	type part struct {
		Text *string `json:"text"`
	}
	type content struct {
		Role  string `json:"role,omitempty"`
		Parts []part `json:"parts"`
	}
	var input struct {
		Contents []content `json:"contents"`
		System   *content  `json:"systemInstruction,omitempty"`
		Config   *struct {
			Temperature *float64 `json:"temperature,omitempty"`
			TopP        *float64 `json:"topP,omitempty"`
			MaxTokens   *int     `json:"maxOutputTokens,omitempty"`
			Stop        []string `json:"stopSequences,omitempty"`
			Candidates  *int     `json:"candidateCount,omitempty"`
		} `json:"generationConfig,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return nil, fmt.Errorf("unsupported Gemini request: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing Gemini request data")
	}
	if len(input.Contents) == 0 || strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("gemini contents and model are required")
	}
	text := func(c content) (json.RawMessage, error) {
		if len(c.Parts) == 0 {
			return nil, fmt.Errorf("gemini text parts are required")
		}
		var value strings.Builder
		for _, p := range c.Parts {
			if p.Text == nil {
				return nil, fmt.Errorf("only Gemini text parts are supported")
			}
			_, _ = value.WriteString(*p.Text)
		}
		return json.Marshal(value.String())
	}
	out := &ChatCompletionsRequest{Model: model, Stream: stream}
	if stream {
		out.StreamOptions = &ChatStreamOptions{IncludeUsage: true}
	}
	if input.System != nil {
		if input.System.Role != "" && input.System.Role != "system" {
			return nil, fmt.Errorf("unsupported systemInstruction role")
		}
		value, err := text(*input.System)
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, ChatMessage{Role: "system", Content: value})
	}
	for _, c := range input.Contents {
		role := c.Role
		switch role {
		case "", "user":
			role = "user"
		case "model":
			role = "assistant"
		default:
			return nil, fmt.Errorf("unsupported Gemini content role %q", role)
		}
		value, err := text(c)
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, ChatMessage{Role: role, Content: value})
	}
	if cfg := input.Config; cfg != nil {
		if cfg.Candidates != nil && *cfg.Candidates != 1 {
			return nil, fmt.Errorf("only candidateCount=1 is supported")
		}
		if cfg.MaxTokens != nil && *cfg.MaxTokens <= 0 {
			return nil, fmt.Errorf("maxOutputTokens must be positive")
		}
		if cfg.Temperature != nil && (*cfg.Temperature < 0 || *cfg.Temperature > 2) {
			return nil, fmt.Errorf("temperature must be between 0 and 2")
		}
		if cfg.TopP != nil && (*cfg.TopP < 0 || *cfg.TopP > 1) {
			return nil, fmt.Errorf("topP must be between 0 and 1")
		}
		if len(cfg.Stop) > 4 {
			return nil, fmt.Errorf("at most four stopSequences are supported")
		}
		for _, stop := range cfg.Stop {
			if stop == "" {
				return nil, fmt.Errorf("stopSequences must not be empty")
			}
		}
		out.MaxTokens, out.Temperature, out.TopP = cfg.MaxTokens, cfg.Temperature, cfg.TopP
		if len(cfg.Stop) > 0 {
			out.Stop, _ = json.Marshal(cfg.Stop)
		}
	}
	return out, nil
}

type GeminiChatPart struct {
	Text    string `json:"text"`
	Thought bool   `json:"thought,omitempty"`
}

type GeminiChatCandidate struct {
	Content *struct {
		Role  string           `json:"role"`
		Parts []GeminiChatPart `json:"parts"`
	} `json:"content,omitempty"`
	Index        int    `json:"index"`
	FinishReason string `json:"finishReason,omitempty"`
}

type GeminiChatResponse struct {
	Candidates []GeminiChatCandidate `json:"candidates,omitempty"`
	Usage      *GeminiChatUsage      `json:"usageMetadata,omitempty"`
}

type GeminiChatUsage struct {
	Prompt     int `json:"promptTokenCount"`
	Candidates int `json:"candidatesTokenCount"`
	Total      int `json:"totalTokenCount"`
	Cached     int `json:"cachedContentTokenCount,omitempty"`
	Thoughts   int `json:"thoughtsTokenCount,omitempty"`
}

func geminiChatUsage(u *ChatUsage) *GeminiChatUsage {
	if u == nil {
		return nil
	}
	out := &GeminiChatUsage{Prompt: u.PromptTokens, Candidates: u.CompletionTokens, Total: u.TotalTokens}
	if out.Total == 0 {
		out.Total = u.PromptTokens + u.CompletionTokens
	}
	if u.PromptTokensDetails != nil {
		out.Cached = u.PromptTokensDetails.CachedTokens
	}
	if u.CompletionTokensDetails != nil {
		out.Thoughts = u.CompletionTokensDetails.ReasoningTokens
		out.Candidates = max(0, out.Candidates-out.Thoughts)
	}
	return out
}

func geminiChatCandidate(text, reasoning, finish string) (GeminiChatCandidate, error) {
	out := GeminiChatCandidate{}
	switch finish {
	case "":
	case "stop":
		out.FinishReason = "STOP"
	case "length":
		out.FinishReason = "MAX_TOKENS"
	case "content_filter":
		out.FinishReason = "SAFETY"
	default:
		return out, fmt.Errorf("unsupported Chat finish reason %q", finish)
	}
	var parts []GeminiChatPart
	if reasoning != "" {
		parts = append(parts, GeminiChatPart{Text: reasoning, Thought: true})
	}
	if text != "" {
		parts = append(parts, GeminiChatPart{Text: text})
	}
	if len(parts) > 0 {
		out.Content = &struct {
			Role  string           `json:"role"`
			Parts []GeminiChatPart `json:"parts"`
		}{Role: "model", Parts: parts}
	}
	return out, nil
}

// ChatToGeminiResponse converts one JSON response or one SSE payload. The
// caller owns framing and completion; this function never invents a STOP.
func ChatToGeminiResponse(body []byte, stream bool) (*GeminiChatResponse, error) {
	var input struct {
		Choices []struct {
			Index   int             `json:"index"`
			Message json.RawMessage `json:"message"`
			Delta   json.RawMessage `json:"delta"`
			Finish  string          `json:"finish_reason"`
		} `json:"choices"`
		Usage *ChatUsage      `json:"usage"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, fmt.Errorf("invalid Chat response: %w", err)
	}
	if len(input.Error) > 0 || len(input.Choices) > 1 || (!stream && len(input.Choices) != 1) || (input.Choices == nil && input.Usage == nil) {
		return nil, fmt.Errorf("unsupported Chat response")
	}
	out := &GeminiChatResponse{Usage: geminiChatUsage(input.Usage)}
	for _, choice := range input.Choices {
		if choice.Index != 0 || (!stream && choice.Finish == "") {
			return nil, fmt.Errorf("invalid Chat choice")
		}
		raw := choice.Message
		if stream {
			raw = choice.Delta
		}
		var msg struct {
			Role             string            `json:"role"`
			Content          string            `json:"content"`
			Reasoning        string            `json:"reasoning"`
			ReasoningContent string            `json:"reasoning_content"`
			Refusal          string            `json:"refusal"`
			Tools            []json.RawMessage `json:"tool_calls"`
			Function         json.RawMessage   `json:"function_call"`
			Audio            json.RawMessage   `json:"audio"`
		}
		if len(raw) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&msg); err != nil {
				return nil, fmt.Errorf("invalid Chat text: %w", err)
			}
		}
		if (msg.Role != "" && msg.Role != "assistant") || len(msg.Tools) > 0 ||
			(len(msg.Function) > 0 && string(msg.Function) != "null") || (len(msg.Audio) > 0 && string(msg.Audio) != "null") {
			return nil, fmt.Errorf("non-text Chat output")
		}
		if msg.ReasoningContent != "" {
			msg.Reasoning = msg.ReasoningContent
		}
		if msg.Refusal != "" {
			msg.Content += msg.Refusal
		}
		candidate, err := geminiChatCandidate(msg.Content, msg.Reasoning, choice.Finish)
		if err != nil {
			return nil, err
		}
		if candidate.Content != nil || candidate.FinishReason != "" {
			out.Candidates = append(out.Candidates, candidate)
		}
	}
	if len(out.Candidates) == 0 && out.Usage == nil {
		return nil, nil
	}
	return out, nil
}
