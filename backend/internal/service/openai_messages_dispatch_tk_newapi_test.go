//go:build unit

package service

import (
	"testing"
)

// Tests for docs/approved/newapi-as-fifth-platform.md §3.1 U7 / US-009 / US-014 / US-015.
// Covers the pure-function contract of sanitizeGroupMessagesDispatchFields:
// the openai-compat platforms (openai, newapi) preserve all messages_dispatch
// fields; non-compat platforms (anthropic, gemini, antigravity) clear them.

func nonzeroDispatchConfig() OpenAIMessagesDispatchModelConfig {
	return OpenAIMessagesDispatchModelConfig{
		OpusMappedModel:   "gpt-5.4",
		SonnetMappedModel: "gpt-5.3-codex",
		HaikuMappedModel:  "gpt-5.4-mini",
		ExactModelMappings: map[string]string{
			"claude-foo": "gpt-bar",
		},
	}
}

func newGroupWithDispatchConfig(platform string) *Group {
	return &Group{
		Platform:                    platform,
		AllowMessagesDispatch:       true,
		DefaultMappedModel:          "gpt-5.4",
		MessagesDispatchModelConfig: nonzeroDispatchConfig(),
	}
}

func assertDispatchPreserved(t *testing.T, g *Group) {
	t.Helper()
	if !g.AllowMessagesDispatch {
		t.Fatalf("AllowMessagesDispatch must be preserved, got false")
	}
	if g.DefaultMappedModel != "gpt-5.4" {
		t.Fatalf("DefaultMappedModel must be preserved, got %q", g.DefaultMappedModel)
	}
	if g.MessagesDispatchModelConfig.OpusMappedModel != "gpt-5.4" {
		t.Fatalf("MessagesDispatchModelConfig.OpusMappedModel must be preserved, got %q", g.MessagesDispatchModelConfig.OpusMappedModel)
	}
	if len(g.MessagesDispatchModelConfig.ExactModelMappings) != 1 {
		t.Fatalf("MessagesDispatchModelConfig.ExactModelMappings must be preserved, got %v", g.MessagesDispatchModelConfig.ExactModelMappings)
	}
}

func assertDispatchCleared(t *testing.T, g *Group) {
	t.Helper()
	if g.AllowMessagesDispatch {
		t.Fatalf("AllowMessagesDispatch must be cleared (false), got true")
	}
	if g.DefaultMappedModel != "" {
		t.Fatalf("DefaultMappedModel must be cleared, got %q", g.DefaultMappedModel)
	}
	if g.MessagesDispatchModelConfig.OpusMappedModel != "" ||
		g.MessagesDispatchModelConfig.SonnetMappedModel != "" ||
		g.MessagesDispatchModelConfig.HaikuMappedModel != "" ||
		len(g.MessagesDispatchModelConfig.ExactModelMappings) != 0 {
		t.Fatalf("MessagesDispatchModelConfig must be zero, got %+v", g.MessagesDispatchModelConfig)
	}
}

// US-009 AC-001 / US-014 AC-001 / direct injection point U7

func TestUS009_Sanitize_NewAPIGroup_Preserves(t *testing.T) {
	g := newGroupWithDispatchConfig(PlatformNewAPI)
	sanitizeGroupMessagesDispatchFields(g)
	assertDispatchPreserved(t, g)
}

// US-009 AC-002 — anthropic / gemini / antigravity must still be cleared.

func TestUS009_Sanitize_AnthropicGroup_Cleared(t *testing.T) {
	g := newGroupWithDispatchConfig(PlatformAnthropic)
	sanitizeGroupMessagesDispatchFields(g)
	assertDispatchCleared(t, g)
}

func TestUS009_Sanitize_GeminiGroup_Cleared(t *testing.T) {
	g := newGroupWithDispatchConfig(PlatformGemini)
	sanitizeGroupMessagesDispatchFields(g)
	assertDispatchCleared(t, g)
}

func TestUS009_Sanitize_AntigravityGroup_Cleared(t *testing.T) {
	g := newGroupWithDispatchConfig(PlatformAntigravity)
	sanitizeGroupMessagesDispatchFields(g)
	assertDispatchCleared(t, g)
}

// US-009 AC-003 / US-015 AC-004 — openai group regression baseline.

func TestUS009_Sanitize_OpenAIGroup_Preserves(t *testing.T) {
	g := newGroupWithDispatchConfig(PlatformOpenAI)
	sanitizeGroupMessagesDispatchFields(g)
	assertDispatchPreserved(t, g)
}

func TestUS015_OpenAIGroup_MessagesDispatchSanitize_Unchanged(t *testing.T) {
	// Same as TestUS009_Sanitize_OpenAIGroup_Preserves but explicitly framed
	// as the regression-baseline assertion required by US-015 AC-004.
	g := newGroupWithDispatchConfig(PlatformOpenAI)
	sanitizeGroupMessagesDispatchFields(g)
	assertDispatchPreserved(t, g)
}

// US-014 AC-001 — round-trip through the in-memory Group struct (PG round-trip
// is covered by integration tests in milestone 5).

func TestUS014_NewAPIGroup_MessagesDispatchConfig_RoundTrip(t *testing.T) {
	in := newGroupWithDispatchConfig(PlatformNewAPI)
	sanitizeGroupMessagesDispatchFields(in)
	if in.MessagesDispatchModelConfig.ExactModelMappings["claude-foo"] != "gpt-bar" {
		t.Fatalf("round-trip lost ExactModelMappings entry, got %v", in.MessagesDispatchModelConfig.ExactModelMappings)
	}
}

func TestUS014_AnthropicGroup_MessagesDispatchConfig_Cleared(t *testing.T) {
	in := newGroupWithDispatchConfig(PlatformAnthropic)
	sanitizeGroupMessagesDispatchFields(in)
	assertDispatchCleared(t, in)
}

func TestUS014_OpenAIGroup_MessagesDispatchConfig_Preserved(t *testing.T) {
	in := newGroupWithDispatchConfig(PlatformOpenAI)
	sanitizeGroupMessagesDispatchFields(in)
	assertDispatchPreserved(t, in)
}

// Edge cases: nil group, empty platform.

func TestSanitizeGroupMessagesDispatchFields_NilGroup_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil group must not panic: %v", r)
		}
	}()
	sanitizeGroupMessagesDispatchFields(nil)
}

func TestSanitizeGroupMessagesDispatchFields_EmptyPlatform_Cleared(t *testing.T) {
	g := newGroupWithDispatchConfig("")
	sanitizeGroupMessagesDispatchFields(g)
	// empty platform is treated as "non-compat" — cleared.
	assertDispatchCleared(t, g)
}

func TestIsOpenAICompatPlatformGroup_Truth(t *testing.T) {
	cases := []struct {
		name     string
		group    *Group
		expected bool
	}{
		{"nil", nil, false},
		{"openai", &Group{Platform: PlatformOpenAI}, true},
		{"newapi", &Group{Platform: PlatformNewAPI}, true},
		{"anthropic", &Group{Platform: PlatformAnthropic}, false},
		{"gemini", &Group{Platform: PlatformGemini}, false},
		{"antigravity", &Group{Platform: PlatformAntigravity}, false},
		{"empty", &Group{Platform: ""}, false},
		{"unknown", &Group{Platform: "wrybar"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isOpenAICompatPlatformGroup(tc.group)
			if got != tc.expected {
				t.Fatalf("isOpenAICompatPlatformGroup(%v) = %v, want %v", tc.group, got, tc.expected)
			}
		})
	}
}
