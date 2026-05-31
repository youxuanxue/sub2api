//go:build unit

package kiro

import "testing"

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
