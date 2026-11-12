package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

// convertChatCompletionsToGeminiGenerateContent maps a Chat Completions body
// onto Gemini generateContent without a Responses or Anthropic hop.
// Remote image URLs, legacy functions, strict tools, disabled parallel calls,
// and mixed built-in plus function tools are rejected so Plan does not send
// a body that would drop those semantics.
func convertChatCompletionsToGeminiGenerateContent(body []byte, model string) ([]byte, error) {
	var req apicompat.ChatCompletionsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(model) == "" {
		model = req.Model
	}
	if len(req.Functions) > 0 || hasChatJSONValue(req.FunctionCall) {
		return nil, errors.New("legacy functions are not converted to Gemini")
	}
	if req.ParallelToolCalls != nil && !*req.ParallelToolCalls {
		return nil, errors.New("parallel_tool_calls=false is not preserved on Gemini")
	}

	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}

	systemText, contents, err := convertChatMessagesToGemini(req.Messages)
	if err != nil {
		return nil, err
	}
	functionTools, builtinTools, names, err := convertChatToolsToGemini(req.Tools)
	if err != nil {
		return nil, err
	}
	if len(functionTools) > 0 && len(builtinTools) > 0 {
		return nil, errors.New("gemini cannot mix function tools with built-in tools")
	}

	out := map[string]any{}
	if systemText != "" {
		out["systemInstruction"] = map[string]any{
			"parts": []any{map[string]any{"text": systemText}},
		}
	}
	if contents == nil {
		contents = []any{}
	}
	out["contents"] = contents

	geminiTools := make([]any, 0, len(builtinTools)+1)
	if len(functionTools) > 0 {
		geminiTools = append(geminiTools, map[string]any{"functionDeclarations": functionTools})
	}
	geminiTools = append(geminiTools, builtinTools...)
	if len(geminiTools) > 0 {
		out["tools"] = geminiTools
	}
	toolConfig, err := chatToolChoiceToGemini(req.ToolChoice, names)
	if err != nil {
		return nil, err
	}
	if toolConfig != nil {
		if len(geminiTools) == 0 {
			return nil, errors.New("tool_choice requires tools")
		}
		out["toolConfig"] = toolConfig
	}

	generation, err := chatGeminiGenerationConfig(req, root)
	if err != nil {
		return nil, err
	}
	if len(generation) > 0 {
		out["generationConfig"] = generation
	}

	stripGeminiFunctionIDs(out)
	_ = antigravity.EnsureImageResponseModalities(out, model)
	_ = antigravity.OmitImageModelMaxOutputTokens(out, model)
	return json.Marshal(out)
}

func chatReasoningEffortClaudeBody(effort string) []byte {
	effort = strings.ToLower(strings.TrimSpace(effort))
	switch effort {
	case "", "none":
		if effort == "none" {
			return []byte(`{"thinking":{"type":"disabled"}}`)
		}
		return []byte(`{}`)
	default:
		encoded, err := json.Marshal(map[string]any{
			"thinking":      map[string]any{"type": "enabled"},
			"output_config": map[string]any{"effort": effort},
		})
		if err != nil {
			return []byte(`{}`)
		}
		return encoded
	}
}

func convertChatMessagesToGemini(messages []apicompat.ChatMessage) (string, []any, error) {
	var systemParts []string
	contents := make([]any, 0, len(messages))
	callNames := map[string]string{}
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		switch role {
		case "system", "developer":
			text, parts, err := chatMessageGeminiParts(message.Content)
			if err != nil {
				return "", nil, err
			}
			if len(parts) > 0 {
				return "", nil, errors.New("system messages only preserve text")
			}
			if strings.TrimSpace(text) != "" {
				systemParts = append(systemParts, text)
			}
		case "user", "assistant":
			text, parts, err := chatMessageGeminiParts(message.Content)
			if err != nil {
				return "", nil, err
			}
			if text != "" {
				parts = append([]any{map[string]any{"text": text}}, parts...)
			}
			if role == "assistant" {
				for _, call := range message.ToolCalls {
					if strings.TrimSpace(call.ID) == "" || (call.Type != "" && call.Type != "function") {
						return "", nil, errors.New("only function tool calls are converted to Gemini")
					}
					name := strings.TrimSpace(call.Function.Name)
					if name == "" {
						return "", nil, errors.New("function tool call is missing a name")
					}
					var args map[string]any
					if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args == nil {
						return "", nil, fmt.Errorf("function arguments must be a JSON object")
					}
					callNames[call.ID] = name
					part := map[string]any{
						"functionCall": map[string]any{"name": name, "args": args},
					}
					parts = append(parts, part)
				}
				if message.FunctionCall != nil {
					return "", nil, errors.New("legacy function_call is not converted to Gemini")
				}
			}
			if len(parts) == 0 {
				continue
			}
			gRole := "user"
			if role == "assistant" {
				gRole = "model"
			}
			contents = append(contents, map[string]any{"role": gRole, "parts": parts})
		case "tool", "function":
			name := strings.TrimSpace(message.Name)
			if name == "" {
				name = callNames[message.ToolCallID]
			}
			if name == "" {
				return "", nil, errors.New("tool result does not match a function call")
			}
			text, parts, err := chatMessageGeminiParts(message.Content)
			if err != nil {
				return "", nil, err
			}
			response := map[string]any{"name": name, "response": map[string]any{"content": text}}
			if len(parts) > 0 {
				response["parts"] = chatInlineParts(parts)
			}
			userParts := []any{map[string]any{"functionResponse": response}}
			if len(contents) == 0 || geminiContentRole(contents[len(contents)-1]) == "model" {
				contents = append(contents, map[string]any{"role": "user", "parts": userParts})
			} else {
				last, ok := contents[len(contents)-1].(map[string]any)
				if !ok {
					return "", nil, errors.New("gemini content turn is not an object")
				}
				existing, _ := last["parts"].([]any)
				last["parts"] = append(existing, userParts...)
			}
		default:
			if role == "" {
				continue
			}
			return "", nil, fmt.Errorf("unsupported chat role %q", role)
		}
	}
	return strings.TrimSpace(strings.Join(systemParts, "\n")), contents, nil
}

func geminiContentRole(content any) string {
	msg, _ := content.(map[string]any)
	role, _ := msg["role"].(string)
	return role
}

func chatInlineParts(parts []any) []any {
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		msg, _ := part.(map[string]any)
		if _, ok := msg["inlineData"]; ok {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func chatMessageGeminiParts(raw json.RawMessage) (string, []any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return splitChatDataImages(text)
	}
	var parts []struct {
		Type      string                  `json:"type"`
		Text      string                  `json:"text"`
		Thinking  string                  `json:"thinking"`
		Reasoning string                  `json:"reasoning"`
		ImageURL  *apicompat.ChatImageURL `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", nil, errors.New("message content must be text or a part array")
	}
	var b strings.Builder
	extras := make([]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "", "text":
			if part.Text == "" {
				continue
			}
			piece, lifted, err := splitChatDataImages(part.Text)
			if err != nil {
				return "", nil, err
			}
			if piece != "" {
				if b.Len() > 0 {
					_ = b.WriteByte('\n')
				}
				_, _ = b.WriteString(piece)
			}
			extras = append(extras, lifted...)
		case "image_url":
			inline, err := chatImageURLPart(part.ImageURL)
			if err != nil {
				return "", nil, err
			}
			extras = append(extras, inline)
		case "thinking", "reasoning":
			thought := part.Thinking
			if thought == "" {
				thought = part.Reasoning
			}
			if thought == "" {
				thought = part.Text
			}
			if strings.TrimSpace(thought) == "" {
				continue
			}
			extras = append(extras, map[string]any{"thought": true, "text": thought})
		default:
			return "", nil, fmt.Errorf("unsupported chat content part %q", part.Type)
		}
	}
	return b.String(), extras, nil
}

func chatImageURLPart(image *apicompat.ChatImageURL) (map[string]any, error) {
	if image == nil {
		return nil, errors.New("image_url is missing")
	}
	mime, data, ok := geminiDataImage(image.URL)
	if !ok {
		return nil, errors.New("remote image URLs are not inlined")
	}
	return map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": data}}, nil
}

func splitChatDataImages(text string) (string, []any, error) {
	var plain strings.Builder
	var images []any
	rest := text
	for {
		start := strings.Index(rest, "](data:image/")
		if start < 0 {
			_, _ = plain.WriteString(rest)
			break
		}
		open := strings.LastIndex(rest[:start], "![")
		if open < 0 {
			_, _ = plain.WriteString(rest[:start+2])
			rest = rest[start+2:]
			continue
		}
		_, _ = plain.WriteString(rest[:open])
		payloadStart := start + 2
		closeRel := strings.Index(rest[payloadStart:], ")")
		if closeRel < 0 {
			_, _ = plain.WriteString(rest[open:])
			break
		}
		dataURL := rest[payloadStart : payloadStart+closeRel]
		mime, data, ok := geminiDataImage(dataURL)
		if !ok {
			return "", nil, errors.New("markdown image is not an inline data URL")
		}
		images = append(images, map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": data}})
		rest = rest[payloadStart+closeRel+1:]
	}
	return plain.String(), images, nil
}

func geminiDataImage(raw string) (mime, data string, ok bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "data:image/") {
		return "", "", false
	}
	comma := strings.Index(raw, ",")
	if comma < 0 {
		return "", "", false
	}
	meta := raw[len("data:"):comma]
	payload := raw[comma+1:]
	if !strings.Contains(strings.ToLower(meta), ";base64") || payload == "" {
		return "", "", false
	}
	mime = meta
	if semi := strings.Index(mime, ";"); semi >= 0 {
		mime = mime[:semi]
	}
	return mime, payload, true
}

func convertChatToolsToGemini(tools []apicompat.ChatTool) (functions []any, builtin []any, names map[string]bool, err error) {
	names = map[string]bool{}
	for _, tool := range tools {
		switch chatBuiltinGeminiKey(tool.Type) {
		case "googleSearch", "codeExecution", "urlContext":
			builtin = append(builtin, map[string]any{chatBuiltinGeminiKey(tool.Type): map[string]any{}})
			continue
		}
		if tool.Type != "" && tool.Type != "function" {
			return nil, nil, nil, fmt.Errorf("unsupported chat tool type %q", tool.Type)
		}
		if tool.Function == nil || strings.TrimSpace(tool.Function.Name) == "" {
			return nil, nil, nil, errors.New("function tool is missing a name")
		}
		if names[tool.Function.Name] {
			return nil, nil, nil, fmt.Errorf("duplicate function name %q", tool.Function.Name)
		}
		if tool.Function.Strict != nil && *tool.Function.Strict {
			return nil, nil, nil, errors.New("strict function tools are not preserved on Gemini")
		}
		names[tool.Function.Name] = true
		params := any(map[string]any{"type": "object", "properties": map[string]any{}})
		if len(tool.Function.Parameters) > 0 && string(tool.Function.Parameters) != "null" {
			if err := json.Unmarshal(tool.Function.Parameters, &params); err != nil {
				return nil, nil, nil, fmt.Errorf("function parameters: %w", err)
			}
		}
		cleaned, err := tkCleanToolSchema(params)
		if err != nil {
			return nil, nil, nil, err
		}
		functions = append(functions, map[string]any{
			"name":        tool.Function.Name,
			"description": tool.Function.Description,
			"parameters":  cleaned,
		})
	}
	return functions, builtin, names, nil
}

func chatBuiltinGeminiKey(toolType string) string {
	switch strings.ToLower(strings.TrimSpace(toolType)) {
	case "web_search", "google_search":
		return "googleSearch"
	case "code_execution":
		return "codeExecution"
	case "url_context":
		return "urlContext"
	default:
		return ""
	}
}

func chatToolChoiceToGemini(raw json.RawMessage, names map[string]bool) (map[string]any, error) {
	if !hasChatJSONValue(raw) {
		return nil, nil
	}
	mode, allowed, err := chatToolChoiceMode(raw, names)
	if err != nil {
		return nil, err
	}
	config := map[string]any{"mode": mode}
	if allowed != "" {
		config["allowedFunctionNames"] = []any{allowed}
	}
	return map[string]any{"functionCallingConfig": config}, nil
}

func chatToolChoiceMode(raw json.RawMessage, names map[string]bool) (mode, allowed string, err error) {
	var choice string
	if err := json.Unmarshal(raw, &choice); err == nil {
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "auto":
			return "AUTO", "", nil
		case "none":
			return "NONE", "", nil
		case "required":
			return "ANY", "", nil
		default:
			return "", "", fmt.Errorf("unsupported tool_choice %q", choice)
		}
	}
	var object struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", "", errors.New("tool_choice must be auto, none, required, or a function")
	}
	if object.Type != "function" || strings.TrimSpace(object.Function.Name) == "" {
		return "", "", errors.New("tool_choice must name a function")
	}
	if len(names) > 0 && !names[object.Function.Name] {
		return "", "", fmt.Errorf("tool_choice names undeclared function %q", object.Function.Name)
	}
	return "ANY", object.Function.Name, nil
}

func chatGeminiGenerationConfig(req apicompat.ChatCompletionsRequest, root map[string]any) (map[string]any, error) {
	out := map[string]any{}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["topP"] = *req.TopP
	}
	maxTokens := 0
	if req.MaxCompletionTokens != nil && *req.MaxCompletionTokens > 0 {
		maxTokens = *req.MaxCompletionTokens
	} else if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}
	if maxTokens > 0 {
		out["maxOutputTokens"] = maxTokens
	}
	stops, err := chatStopSequences(req.Stop)
	if err != nil {
		return nil, err
	}
	if len(stops) > 0 {
		if len(stops) > 5 {
			stops = stops[:5]
		}
		out["stopSequences"] = stops
	}
	if err := applyChatResponseFormat(out, req.ResponseFormat); err != nil {
		return nil, err
	}
	compat, err := geminiCompatGenerationOptions(root)
	if err != nil {
		return nil, err
	}
	for key, value := range compat {
		out[key] = value
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func chatStopSequences(raw json.RawMessage) ([]string, error) {
	if !hasChatJSONValue(raw) {
		return nil, nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		if strings.TrimSpace(one) == "" {
			return nil, nil
		}
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, errors.New("stop must be a string or array of strings")
	}
	return many, nil
}

func applyChatResponseFormat(generation map[string]any, raw json.RawMessage) error {
	if !hasChatJSONValue(raw) {
		return nil
	}
	var format struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &format); err != nil {
		return errors.New("response_format must be an object")
	}
	switch format.Type {
	case "", "text":
		return nil
	case "json_object", "json_schema":
		generation["responseMimeType"] = "application/json"
	default:
		return fmt.Errorf("unsupported response_format %q", format.Type)
	}
	if format.Type != "json_schema" || len(format.JSONSchema.Schema) == 0 || string(format.JSONSchema.Schema) == "null" {
		return nil
	}
	var schema any
	if err := json.Unmarshal(format.JSONSchema.Schema, &schema); err != nil {
		return errors.New("response_format json_schema.schema must be JSON")
	}
	cleaned, err := tkCleanToolSchema(schema)
	if err != nil {
		return err
	}
	generation["responseSchema"] = cleaned
	return nil
}

func hasChatJSONValue(raw json.RawMessage) bool {
	return len(raw) > 0 && strings.TrimSpace(string(raw)) != "null"
}
