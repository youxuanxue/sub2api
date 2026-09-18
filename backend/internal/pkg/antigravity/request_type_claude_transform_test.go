package antigravity

import (
	"encoding/json"
	"testing"
)

func TestTransformClaudeToGemini_OmitsRequestTypeForPlainText(t *testing.T) {
	t.Parallel()
	req := &ClaudeRequest{
		Model: "claude-sonnet-4-5",
		Messages: []ClaudeMessage{
			{Role: "user", Content: json.RawMessage(`"hello"`)},
		},
	}
	raw, err := TransformClaudeToGemini(req, "proj", "gemini-3.1-pro-preview")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["requestType"]; ok {
		t.Fatalf("plain text must omit requestType, got %#v", m["requestType"])
	}
}

func TestTransformClaudeToGemini_SetsAgentWhenToolsPresent(t *testing.T) {
	t.Parallel()
	req := &ClaudeRequest{
		Model: "claude-sonnet-4-5",
		Messages: []ClaudeMessage{
			{Role: "user", Content: json.RawMessage(`"hello"`)},
		},
		Tools: []ClaudeTool{
			{Name: "get_weather", Description: "weather", InputSchema: map[string]any{"type": "object"}},
		},
	}
	raw, err := TransformClaudeToGemini(req, "proj", "gemini-3.1-pro-preview")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["requestType"] != "agent" {
		t.Fatalf("want agent, got %#v", m["requestType"])
	}
}
