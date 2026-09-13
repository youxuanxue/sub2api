package apicompat

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type geminiMessagesPart struct {
	Text      *string `json:"text,omitempty"`
	Thought   bool    `json:"thought,omitempty"`
	Signature string  `json:"thoughtSignature,omitempty"`
	Image     *struct {
		MIME string `json:"mimeType"`
		Data string `json:"data"`
	} `json:"inlineData,omitempty"`
	Call   *GeminiFunctionCall `json:"functionCall,omitempty"`
	Result *struct {
		ID       string          `json:"id,omitempty"`
		Name     string          `json:"name"`
		Response json.RawMessage `json:"response"`
	} `json:"functionResponse,omitempty"`
}
type GeminiFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}
type geminiMessagesContent struct {
	Role  string               `json:"role,omitempty"`
	Parts []geminiMessagesPart `json:"parts"`
}

// GeminiToMessagesRequest is shared by Plan admission and execution. Unsupported
// fields fail closed; Gemini-native signatures and provider tools are not portable.
func GeminiToMessagesRequest(body []byte, model string, stream bool) (*AnthropicRequest, error) {
	var in struct {
		Contents []geminiMessagesContent `json:"contents"`
		System   *geminiMessagesContent  `json:"systemInstruction,omitempty"`
		Config   *struct {
			Temperature *float64 `json:"temperature,omitempty"`
			TopP        *float64 `json:"topP,omitempty"`
			MaxTokens   *int     `json:"maxOutputTokens,omitempty"`
			Stop        []string `json:"stopSequences,omitempty"`
			Candidates  *int     `json:"candidateCount,omitempty"`
			Thinking    *struct {
				Budget  *int  `json:"thinkingBudget,omitempty"`
				Include *bool `json:"includeThoughts,omitempty"`
			} `json:"thinkingConfig,omitempty"`
		} `json:"generationConfig,omitempty"`
		Tools []struct {
			Functions []struct {
				Name        string          `json:"name"`
				Description string          `json:"description,omitempty"`
				Parameters  json.RawMessage `json:"parameters,omitempty"`
				JSONSchema  json.RawMessage `json:"parametersJsonSchema,omitempty"`
			} `json:"functionDeclarations"`
		} `json:"tools,omitempty"`
		ToolConfig *struct {
			Function *struct {
				Mode    string   `json:"mode,omitempty"`
				Allowed []string `json:"allowedFunctionNames,omitempty"`
			} `json:"functionCallingConfig,omitempty"`
		} `json:"toolConfig,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return nil, fmt.Errorf("unsupported Gemini Messages request: %w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid trailing Gemini request data")
	}
	if len(in.Contents) == 0 || strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("contents and model required")
	}
	out := &AnthropicRequest{Model: model, Stream: stream, MaxTokens: 4096}
	if c := in.Config; c != nil {
		if c.Candidates != nil && *c.Candidates != 1 {
			return nil, fmt.Errorf("only candidateCount=1 supported")
		}
		if c.MaxTokens != nil {
			if *c.MaxTokens <= 0 {
				return nil, fmt.Errorf("maxOutputTokens must be positive")
			}
			out.MaxTokens = *c.MaxTokens
		}
		if c.Temperature != nil && (*c.Temperature < 0 || *c.Temperature > 1) {
			return nil, fmt.Errorf("messages temperature must be between 0 and 1")
		}
		if c.TopP != nil && (*c.TopP < 0 || *c.TopP > 1) {
			return nil, fmt.Errorf("invalid topP")
		}
		if len(c.Stop) > 4 {
			return nil, fmt.Errorf("too many stopSequences")
		}
		for _, s := range c.Stop {
			if s == "" {
				return nil, fmt.Errorf("empty stop sequence")
			}
		}
		out.Temperature, out.TopP, out.StopSeqs = c.Temperature, c.TopP, c.Stop
		if t := c.Thinking; t != nil {
			if t.Budget == nil {
				return nil, fmt.Errorf("explicit thinkingBudget required")
			}
			if *t.Budget == 0 {
				out.Thinking = &AnthropicThinking{Type: "disabled"}
			} else {
				if *t.Budget < 1024 || *t.Budget >= out.MaxTokens || t.Include == nil || !*t.Include {
					return nil, fmt.Errorf("thinking needs budget >=1024 below maxOutputTokens and includeThoughts=true")
				}
				out.Thinking = &AnthropicThinking{Type: "enabled", BudgetTokens: *t.Budget}
			}
		}
	}
	names := map[string]bool{}
	for _, tool := range in.Tools {
		if len(tool.Functions) == 0 {
			return nil, fmt.Errorf("empty function declarations")
		}
		for _, f := range tool.Functions {
			if f.Name == "" || names[f.Name] {
				return nil, fmt.Errorf("invalid or duplicate function name")
			}
			names[f.Name] = true
			schema := f.JSONSchema
			if len(f.Parameters) > 0 {
				if len(schema) > 0 {
					return nil, fmt.Errorf("multiple parameter schemas")
				}
				var err error
				schema, err = geminiFunctionSchema(f.Parameters)
				if err != nil {
					return nil, err
				}
			}
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			if !geminiJSONObject(schema) {
				return nil, fmt.Errorf("function parameters must be an object")
			}
			out.Tools = append(out.Tools, AnthropicTool{Name: f.Name, Description: f.Description, InputSchema: schema})
		}
	}
	if in.ToolConfig != nil {
		f := in.ToolConfig.Function
		if f == nil || len(names) == 0 {
			return nil, fmt.Errorf("function calling config requires tools")
		}
		choice := map[string]string{}
		switch f.Mode {
		case "", "AUTO":
			choice["type"] = "auto"
		case "NONE":
			choice["type"] = "none"
		case "ANY":
			choice["type"] = "any"
		default:
			return nil, fmt.Errorf("unsupported function calling mode")
		}
		if len(f.Allowed) > 0 {
			if f.Mode != "ANY" || len(f.Allowed) != 1 || !names[f.Allowed[0]] {
				return nil, fmt.Errorf("unsupported allowed functions")
			}
			choice = map[string]string{"type": "tool", "name": f.Allowed[0]}
		}
		out.ToolChoice, _ = json.Marshal(choice)
	}
	if out.Thinking != nil && out.Thinking.Type == "enabled" && len(out.ToolChoice) > 0 {
		var choice map[string]string
		_ = json.Unmarshal(out.ToolChoice, &choice)
		if choice["type"] == "any" || choice["type"] == "tool" {
			return nil, fmt.Errorf("messages thinking cannot force tool choice")
		}
	}
	if in.System != nil {
		if in.System.Role != "" && in.System.Role != "system" {
			return nil, fmt.Errorf("unsupported system role")
		}
		var b strings.Builder
		for _, p := range in.System.Parts {
			if p.Text == nil || p.Thought || p.Signature != "" || p.Image != nil || p.Call != nil || p.Result != nil {
				return nil, fmt.Errorf("systemInstruction must contain text")
			}
			_, _ = b.WriteString(*p.Text)
		}
		if len(in.System.Parts) == 0 {
			return nil, fmt.Errorf("empty systemInstruction")
		}
		out.System, _ = json.Marshal(b.String())
	}
	pending := map[string]string{}
	used := map[string]bool{}
	sequence := 0
	for _, c := range in.Contents {
		role := c.Role
		switch role {
		case "", "user":
			role = "user"
		case "model":
			role = "assistant"
		default:
			return nil, fmt.Errorf("unsupported content role")
		}
		if len(c.Parts) == 0 {
			return nil, fmt.Errorf("empty content")
		}
		if len(pending) > 0 && role != "user" {
			return nil, fmt.Errorf("function results must immediately follow calls")
		}
		var blocks []AnthropicContentBlock
		for _, p := range c.Parts {
			if role == "user" && len(pending) > 0 && p.Result == nil {
				return nil, fmt.Errorf("function results must precede other user content")
			}
			count := 0
			for _, present := range []bool{p.Text != nil, p.Image != nil, p.Call != nil, p.Result != nil} {
				if present {
					count++
				}
			}
			if count != 1 {
				return nil, fmt.Errorf("each part must contain exactly one content kind")
			}
			if (p.Thought || p.Signature != "") && (p.Text == nil || !p.Thought || role != "assistant") {
				return nil, fmt.Errorf("unportable thought signature")
			}
			switch {
			case p.Text != nil:
				if p.Thought {
					sig, err := decodeMessagesThoughtSignature(p.Signature)
					if err != nil {
						return nil, err
					}
					blocks = append(blocks, AnthropicContentBlock{Type: "thinking", Thinking: *p.Text, Signature: sig})
				} else {
					blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: *p.Text})
				}
			case p.Image != nil:
				if role != "user" {
					return nil, fmt.Errorf("image requires user role")
				}
				switch p.Image.MIME {
				case "image/png", "image/jpeg", "image/gif", "image/webp":
				default:
					return nil, fmt.Errorf("unsupported image MIME")
				}
				data, err := base64.StdEncoding.DecodeString(p.Image.Data)
				if err != nil || len(data) == 0 {
					return nil, fmt.Errorf("invalid image data")
				}
				blocks = append(blocks, AnthropicContentBlock{Type: "image", Source: &AnthropicImageSource{Type: "base64", MediaType: p.Image.MIME, Data: p.Image.Data}})
			case p.Call != nil:
				if role != "assistant" || p.Call.Name == "" || !geminiJSONObject(p.Call.Args) {
					return nil, fmt.Errorf("invalid functionCall")
				}
				id := p.Call.ID
				if id == "" {
					id = fmt.Sprintf("tk_gemini_%d", sequence)
					sequence++
				}
				if used[id] {
					return nil, fmt.Errorf("duplicate functionCall id")
				}
				used[id] = true
				pending[id] = p.Call.Name
				blocks = append(blocks, AnthropicContentBlock{Type: "tool_use", ID: id, Name: p.Call.Name, Input: p.Call.Args})
			case p.Result != nil:
				if role != "user" || p.Result.Name == "" || !geminiJSONObject(p.Result.Response) {
					return nil, fmt.Errorf("invalid functionResponse")
				}
				id := p.Result.ID
				if id == "" {
					for key, name := range pending {
						if name == p.Result.Name {
							if id != "" {
								return nil, fmt.Errorf("ambiguous functionResponse requires id")
							}
							id = key
						}
					}
				}
				if id == "" || pending[id] != p.Result.Name {
					return nil, fmt.Errorf("unmatched functionResponse")
				}
				delete(pending, id)
				content, _ := json.Marshal(string(p.Result.Response))
				blocks = append(blocks, AnthropicContentBlock{Type: "tool_result", ToolUseID: id, Content: content})
			}
		}
		content, _ := json.Marshal(blocks)
		out.Messages = append(out.Messages, AnthropicMessage{Role: role, Content: content})
	}
	if len(pending) > 0 {
		return nil, fmt.Errorf("function calls require results before generation")
	}
	return out, nil
}
func geminiJSONObject(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	return json.Unmarshal(raw, &obj) == nil && obj != nil
}

// Gemini Schema is not JSON Schema. Translate only its proven common subset.
func geminiFunctionSchema(raw json.RawMessage) (json.RawMessage, error) {
	var schema map[string]json.RawMessage
	if json.Unmarshal(raw, &schema) != nil || schema == nil {
		return nil, fmt.Errorf("invalid Gemini schema")
	}
	for key, value := range schema {
		switch key {
		case "type":
			var kind string
			if json.Unmarshal(value, &kind) != nil {
				return nil, fmt.Errorf("invalid schema type")
			}
			switch kind {
			case "OBJECT", "ARRAY", "STRING", "NUMBER", "INTEGER", "BOOLEAN":
			default:
				return nil, fmt.Errorf("unsupported Gemini schema type")
			}
			schema[key], _ = json.Marshal(strings.ToLower(kind))
		case "properties":
			var props map[string]json.RawMessage
			if json.Unmarshal(value, &props) != nil || props == nil {
				return nil, fmt.Errorf("invalid schema properties")
			}
			for name, v := range props {
				converted, err := geminiFunctionSchema(v)
				if err != nil {
					return nil, err
				}
				props[name] = converted
			}
			schema[key], _ = json.Marshal(props)
		case "items":
			converted, err := geminiFunctionSchema(value)
			if err != nil {
				return nil, err
			}
			schema[key] = converted
		case "description", "required", "enum":
		default:
			return nil, fmt.Errorf("unsupported Gemini schema field %s", key)
		}
	}
	return json.Marshal(schema)
}

const messagesThoughtPrefix = "tk-messages-v1:"

func encodeMessagesThoughtSignature(sig string) string {
	return base64.StdEncoding.EncodeToString([]byte(messagesThoughtPrefix + sig))
}
func decodeMessagesThoughtSignature(sig string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil || !strings.HasPrefix(string(decoded), messagesThoughtPrefix) {
		return "", fmt.Errorf("only Messages-origin thought signatures are supported")
	}
	return strings.TrimPrefix(string(decoded), messagesThoughtPrefix), nil
}
