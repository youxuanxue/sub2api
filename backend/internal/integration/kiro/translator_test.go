//go:build unit

package kiro

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// TestClaudeToKiro_MinimalUserMessage exercises the Claude→Kiro translation on a
// minimal single-user-turn request and asserts the resulting KiroPayload carries
// the user content and the version-normalized model ID.
func TestClaudeToKiro_MinimalUserMessage(t *testing.T) {
	req := &ClaudeRequest{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 1024,
		Messages: []ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}

	payload := ClaudeToKiro(req, false)
	if payload == nil {
		t.Fatal("ClaudeToKiro returned nil payload")
	}
	if payload.InferenceConfig.MaxTokens != req.MaxTokens {
		t.Fatalf("expected native maxTokens %d, got %d", req.MaxTokens, payload.InferenceConfig.MaxTokens)
	}

	got := payload.ConversationState.CurrentMessage.UserInputMessage
	if got.Content == "" {
		t.Fatalf("expected non-empty current user message content, got empty")
	}
	if got.Content != "hello" {
		t.Fatalf("expected current message content %q, got %q", "hello", got.Content)
	}

	// MapModel normalizes dash version form to dot form: claude-sonnet-4-5 -> claude-sonnet-4.5.
	wantModel := "claude-sonnet-4.5"
	if mapped := MapModel("claude-sonnet-4-5"); mapped != wantModel {
		t.Fatalf("MapModel sanity: expected %q, got %q", wantModel, mapped)
	}
	if got.ModelID != wantModel {
		t.Fatalf("expected current message ModelID %q, got %q", wantModel, got.ModelID)
	}
}

func TestMapModel_DatedAnthropicSnapshotsStripToDotForm(t *testing.T) {
	cases := map[string]string{
		"claude-haiku-4-5-20251001":  "claude-haiku-4.5",
		"claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
		"claude-opus-4-8":            "claude-opus-4.8",
		"claude-opus-5-5":            "claude-opus-5.5",
		"claude-sonnet-5-5":          "claude-sonnet-5.5",
	}
	for input, want := range cases {
		if got := MapModel(input); got != want {
			t.Fatalf("MapModel(%q): want %q, got %q", input, want, got)
		}
	}
}

// TestKiroToClaudeResponse_Basic asserts the basic fields of the Claude-shaped
// response synthesized from a Kiro completion.
func TestKiroToClaudeResponse_Basic(t *testing.T) {
	resp := KiroToClaudeResponse(
		"hi there", // content
		"",         // thinkingContent
		false,      // includeEmptyThinkingBlock
		nil,        // toolUses
		7,          // inputTokens
		3,          // outputTokens
		"claude-sonnet-4.5",
		"end_turn",
	)
	if resp == nil {
		t.Fatal("KiroToClaudeResponse returned nil")
	}
	if resp.Type != "message" {
		t.Errorf("expected Type %q, got %q", "message", resp.Type)
	}
	if resp.Role != "assistant" {
		t.Errorf("expected Role %q, got %q", "assistant", resp.Role)
	}
	if resp.Model != "claude-sonnet-4.5" {
		t.Errorf("expected Model %q, got %q", "claude-sonnet-4.5", resp.Model)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("expected StopReason %q (no tool use), got %q", "end_turn", resp.StopReason)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 {
		t.Errorf("expected usage in=7 out=3, got in=%d out=%d", resp.Usage.InputTokens, resp.Usage.OutputTokens)
	}

	// A single text block carrying the content.
	var foundText bool
	for _, b := range resp.Content {
		if b.Type == "text" {
			foundText = true
			if b.Text != "hi there" {
				t.Errorf("expected text block %q, got %q", "hi there", b.Text)
			}
		}
	}
	if !foundText {
		t.Errorf("expected a text content block, got %+v", resp.Content)
	}
}

func TestClaudeToKiro_AdaptiveThinkingEmitsAdditionalModelRequestFields(t *testing.T) {
	req := &ClaudeRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 256,
		Messages: []ClaudeMessage{
			{Role: "user", Content: "hi"},
		},
		Thinking:     &ClaudeThinkingConfig{Type: "adaptive"},
		OutputConfig: &ClaudeOutputConfig{Effort: "high"},
	}

	payload := ClaudeToKiro(req, true)
	if payload == nil {
		t.Fatal("ClaudeToKiro returned nil payload")
	}
	fields := payload.AdditionalModelRequestFields
	if fields == nil {
		t.Fatal("expected additionalModelRequestFields when thinking enabled")
	}
	if fields.Thinking == nil || fields.Thinking.Type != "adaptive" || fields.Thinking.Display != "summarized" {
		t.Fatalf("unexpected thinking request fields: %+v", fields.Thinking)
	}
	if fields.OutputConfig == nil || fields.OutputConfig.Effort != "high" {
		t.Fatalf("unexpected output_config: %+v", fields.OutputConfig)
	}
	if fields.MaxTokens != 0 || payload.InferenceConfig.MaxTokens != 256 {
		t.Fatalf("expected omitted AMRF limit and native maxTokens=256, got AMRF=%d native=%d", fields.MaxTokens, payload.InferenceConfig.MaxTokens)
	}
}

func TestBuildAdditionalModelRequestFields_IgnoresClientDisplayOmitted(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-sonnet-4-6",
		Thinking: &ClaudeThinkingConfig{
			Type:    "adaptive",
			Display: "omitted",
		},
	}
	fields := buildAdditionalModelRequestFields(req)
	if fields == nil || fields.Thinking == nil {
		t.Fatal("expected thinking request fields")
	}
	if fields.Thinking.Display != "summarized" {
		t.Fatalf("display = %q, want summarized", fields.Thinking.Display)
	}
	if fields.MaxTokens != 32000 {
		t.Fatalf("default max_tokens changed: got %d", fields.MaxTokens)
	}
	if fields.OutputConfig == nil || fields.OutputConfig.Effort != "medium" {
		t.Fatalf("default effort = %+v, want medium", fields.OutputConfig)
	}
}

func TestClaudeToKiro_ThinkingOutputLimitWire(t *testing.T) {
	// Kiro rejects AMRF max_tokens below 1024, even though the native
	// inferenceConfig accepts smaller output limits. Preserve the native field;
	// forwarding it is not a guarantee that upstream enforces a hard output cap.
	for _, limit := range []int{1, 256, 1023, 1024, 4096} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			req := &ClaudeRequest{
				Model: "claude-opus-5-5", MaxTokens: limit,
				Messages:     []ClaudeMessage{{Role: "user", Content: "Reply OK."}},
				Thinking:     &ClaudeThinkingConfig{Type: "adaptive"},
				OutputConfig: &ClaudeOutputConfig{Effort: "high"},
			}
			payload := ClaudeToKiro(req, true)
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				InferenceConfig struct {
					MaxTokens int `json:"maxTokens"`
				} `json:"inferenceConfig"`
				AMRF struct {
					MaxTokens    *int                `json:"max_tokens"`
					Thinking     KiroThinkingRequest `json:"thinking"`
					OutputConfig KiroOutputConfig    `json:"output_config"`
				} `json:"additionalModelRequestFields"`
			}
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.InferenceConfig.MaxTokens != limit || req.MaxTokens != limit {
				t.Fatalf("native or caller limit changed: wire=%d request=%d want=%d", wire.InferenceConfig.MaxTokens, req.MaxTokens, limit)
			}
			if limit < 1024 {
				if wire.AMRF.MaxTokens != nil {
					t.Fatalf("unsupported duplicate AMRF max_tokens sent: %d", *wire.AMRF.MaxTokens)
				}
			} else if wire.AMRF.MaxTokens == nil || *wire.AMRF.MaxTokens != limit {
				t.Fatalf("legal AMRF max_tokens not preserved: %v", wire.AMRF.MaxTokens)
			}
			if wire.AMRF.Thinking.Type != "adaptive" || wire.AMRF.Thinking.Display != "summarized" || wire.AMRF.OutputConfig.Effort != "high" {
				t.Fatalf("thinking configuration changed: %+v", wire.AMRF)
			}
		})
	}
}

func TestClaudeToKiro_ThinkingDisabledOmitsAdditionalModelRequestFields(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-sonnet-4-6",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "hi"},
		},
	}
	payload := ClaudeToKiro(req, false)
	if payload.AdditionalModelRequestFields != nil {
		t.Fatalf("expected no additionalModelRequestFields, got %+v", payload.AdditionalModelRequestFields)
	}
}

func TestClaudeToKiro_ToolChoiceNoneOmitsTools(t *testing.T) {
	req := &ClaudeRequest{
		Model:      "claude-sonnet-4-5",
		Messages:   []ClaudeMessage{{Role: "user", Content: "hello"}},
		Tools:      []ClaudeTool{{Name: "Read", Description: "read", InputSchema: map[string]any{"type": "object"}}},
		ToolChoice: "none",
	}
	payload := ClaudeToKiro(req, false)
	if ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext; ctx != nil && len(ctx.Tools) != 0 {
		t.Fatalf("tool_choice none forwarded tools: %+v", ctx.Tools)
	}
	if strings.Contains(payload.ConversationState.CurrentMessage.UserInputMessage.Content, "Backend tool directive") {
		t.Fatal("tool_choice none unexpectedly added a directive")
	}
}

func TestClaudeToKiro_ToolChoiceAnyAddsDirective(t *testing.T) {
	req := &ClaudeRequest{
		Model:      "claude-sonnet-4-5",
		Messages:   []ClaudeMessage{{Role: "user", Content: "hello"}},
		Tools:      []ClaudeTool{{Name: "Read", Description: "read", InputSchema: map[string]any{"type": "object"}}},
		ToolChoice: map[string]any{"type": "any"},
	}
	payload := ClaudeToKiro(req, false)
	content := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(content, "requires a tool call") {
		t.Fatalf("tool_choice any directive missing: %q", content)
	}
}

func TestClaudeToKiro_ToolChoiceNamedResolvesSanitizedName(t *testing.T) {
	req := &ClaudeRequest{
		Model:      "claude-sonnet-4-5",
		Messages:   []ClaudeMessage{{Role: "user", Content: "hello"}},
		Tools:      []ClaudeTool{{Name: "read-file", Description: "read", InputSchema: map[string]any{"type": "object"}}},
		ToolChoice: map[string]any{"type": "tool", "name": "read-file"},
	}
	payload := ClaudeToKiro(req, false)
	content := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(content, "available tool named") {
		t.Fatalf("named tool directive missing: %q", content)
	}
}

func TestClaudeToKiro_HaikuThinkingOmitsAdditionalModelRequestFields(t *testing.T) {
	// Positive: Haiku + thinking must not emit AMRF (prod 2026-10-04 Kiro 400).
	for _, model := range []string{
		"claude-haiku-4-5-20251001",
		"claude-haiku-4-5",
		"claude-haiku-4.5",
	} {
		req := &ClaudeRequest{
			Model:     model,
			MaxTokens: 256,
			Messages: []ClaudeMessage{
				{Role: "user", Content: "hi"},
			},
			Thinking:     &ClaudeThinkingConfig{Type: "adaptive"},
			OutputConfig: &ClaudeOutputConfig{Effort: "high"},
		}
		payload := ClaudeToKiro(req, true)
		if payload == nil {
			t.Fatalf("model %q: ClaudeToKiro returned nil", model)
		}
		if payload.AdditionalModelRequestFields != nil {
			t.Fatalf("model %q: Haiku must omit additionalModelRequestFields, got %+v",
				model, payload.AdditionalModelRequestFields)
		}
		// Soft-thinking prompt path remains available without AMRF.
		if len(payload.ConversationState.History) == 0 {
			t.Fatalf("model %q: expected system priming history for thinking prompt", model)
		}
	}
}

func TestClaudeToKiro_NonHaikuThinkingStillEmitsAdditionalModelRequestFields(t *testing.T) {
	// Negative / regression: Sonnet/Opus thinking must keep AMRF.
	for _, model := range []string{
		"claude-sonnet-4-6",
		"claude-opus-4-6",
	} {
		req := &ClaudeRequest{
			Model:     model,
			MaxTokens: 256,
			Messages: []ClaudeMessage{
				{Role: "user", Content: "hi"},
			},
			Thinking: &ClaudeThinkingConfig{Type: "adaptive"},
		}
		payload := ClaudeToKiro(req, true)
		if payload == nil || payload.AdditionalModelRequestFields == nil {
			t.Fatalf("model %q: expected additionalModelRequestFields for non-Haiku thinking", model)
		}
		if payload.AdditionalModelRequestFields.OutputConfig == nil || payload.AdditionalModelRequestFields.OutputConfig.Effort != "medium" {
			t.Fatalf("model %q: default effort = %+v, want medium", model, payload.AdditionalModelRequestFields.OutputConfig)
		}
	}
}

func TestClaudeToKiro_DefaultThinkingBudgetIsMedium(t *testing.T) {
	req := &ClaudeRequest{
		Model:     "claude-opus-5",
		MaxTokens: 2048,
		Messages:  []ClaudeMessage{{Role: "user", Content: "hi"}},
		Thinking:  &ClaudeThinkingConfig{Type: "enabled"},
	}
	payload := ClaudeToKiro(req, true)
	prompt := ""
	if payload != nil && len(payload.ConversationState.History) > 0 && payload.ConversationState.History[0].UserInputMessage != nil {
		prompt = payload.ConversationState.History[0].UserInputMessage.Content
	}
	if !strings.Contains(prompt, "<max_thinking_length>16000</max_thinking_length>") {
		t.Fatalf("default thinking length missing: %q", prompt)
	}
	if strings.Contains(prompt, "200000") {
		t.Fatalf("default thinking length still uses 200000: %q", prompt)
	}
	if payload.AdditionalModelRequestFields == nil || payload.AdditionalModelRequestFields.OutputConfig == nil || payload.AdditionalModelRequestFields.OutputConfig.Effort != "medium" {
		t.Fatalf("default effort = %+v, want medium", payload.AdditionalModelRequestFields)
	}
}

func TestClaudeToKiro_ClientBudgetAndEffortAreHonored(t *testing.T) {
	req := &ClaudeRequest{
		Model:        "claude-opus-5",
		MaxTokens:    2048,
		Messages:     []ClaudeMessage{{Role: "user", Content: "hi"}},
		Thinking:     &ClaudeThinkingConfig{Type: "enabled", BudgetTokens: 8000},
		OutputConfig: &ClaudeOutputConfig{Effort: "low"},
	}
	payload := ClaudeToKiro(req, true)
	prompt := ""
	if payload != nil && len(payload.ConversationState.History) > 0 && payload.ConversationState.History[0].UserInputMessage != nil {
		prompt = payload.ConversationState.History[0].UserInputMessage.Content
	}
	if !strings.Contains(prompt, "<max_thinking_length>8000</max_thinking_length>") {
		t.Fatalf("client thinking budget missing: %q", prompt)
	}
	if strings.Contains(prompt, "<max_thinking_length>16000</max_thinking_length>") || strings.Contains(prompt, "200000") {
		t.Fatalf("client thinking budget was replaced: %q", prompt)
	}
	if payload.AdditionalModelRequestFields == nil || payload.AdditionalModelRequestFields.OutputConfig == nil || payload.AdditionalModelRequestFields.OutputConfig.Effort != "low" {
		t.Fatalf("client effort = %+v, want low", payload.AdditionalModelRequestFields)
	}
}

func TestClaudeToKiro_HistoryThinkingIsNotForwarded(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-opus-5",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: []any{
				map[string]any{"type": "thinking", "thinking": "secret plan", "signature": "sig_kiro_unsigned"},
				map[string]any{"type": "redacted_thinking", "data": "secret plan"},
				map[string]any{"type": "text", "text": "visible answer"},
			}},
			{Role: "user", Content: "next"},
		},
	}
	payload := ClaudeToKiro(req, false)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(raw)
	if strings.Contains(wire, "secret plan") || strings.Contains(wire, "sig_kiro_unsigned") {
		t.Fatalf("thinking leaked into the next Kiro request: %s", wire)
	}
	if !strings.Contains(wire, "visible answer") {
		t.Fatalf("assistant text missing: %s", wire)
	}
}

func TestKiroSupportsAdditionalModelRequestFields(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"claude-haiku-4.5", false},
		{"claude-haiku-4-5-20251001", false},
		{"CLAUDE-HAIKU-4.5", false},
		{"claude-sonnet-4.6", true},
		{"claude-opus-4.6", true},
		{"", true},
	}
	for _, tc := range cases {
		if got := kiroSupportsAdditionalModelRequestFields(tc.model); got != tc.want {
			t.Fatalf("kiroSupportsAdditionalModelRequestFields(%q)=%v, want %v", tc.model, got, tc.want)
		}
	}
}
