package kiro

import "testing"

func TestExtractTaglessThinking_NoReliableSplitReturnsEmpty(t *testing.T) {
	got := ExtractTaglessThinking("short answer without separator")
	if got != "" {
		t.Fatalf("ExtractTaglessThinking() = %q, want empty", got)
	}
}

func TestResolveStashThinking_NoReliableSplitReturnsEmpty(t *testing.T) {
	got := ResolveStashThinking("short answer without separator", "...", "SIG")
	if got != "" {
		t.Fatalf("ResolveStashThinking() = %q, want empty", got)
	}
}

func TestExtractTaglessThinking_SplitsTrailingAnswer(t *testing.T) {
	raw := "17 × 23:\n\n17 × 20 = 340\n17 × 3 = 51\n340 + 51 = 391\n\n391"
	got := ExtractTaglessThinking(raw)
	want := "17 × 23:\n\n17 × 20 = 340\n17 × 3 = 51\n340 + 51 = 391"
	if got != want {
		t.Fatalf("ExtractTaglessThinking() = %q, want %q", got, want)
	}
}

func TestExtractTaglessThinking_PrefersInlineTags(t *testing.T) {
	raw := "\n<thinking>\nhidden plan\n</thinking>\n\nvisible answer"
	got := ExtractTaglessThinking(raw)
	if got != "hidden plan" {
		t.Fatalf("ExtractTaglessThinking() = %q, want hidden plan", got)
	}
}

func TestResolveStashThinking_UsesTurnThinkingBeforeTagless(t *testing.T) {
	got := ResolveStashThinking("ignored raw", "explicit reasoning", "SIG")
	if got != "explicit reasoning" {
		t.Fatalf("ResolveStashThinking() = %q, want explicit reasoning", got)
	}
}

func TestResolveStashThinking_IgnoresReasoningPlaceholder(t *testing.T) {
	raw := "step one\n\n42"
	got := ResolveStashThinking(raw, "...", "SIG")
	if got != "step one" {
		t.Fatalf("ResolveStashThinking() = %q, want step one", got)
	}
}

func TestResolveStashThinking_NoSignatureNoTaglessFallback(t *testing.T) {
	raw := "step one\n\n42"
	got := ResolveStashThinking(raw, "", "")
	if got != "" {
		t.Fatalf("ResolveStashThinking() = %q, want empty", got)
	}
}

func TestResolveStashThinking_TaggedRawAssistant(t *testing.T) {
	raw := "<thinking>chain</thinking>answer"
	got := ResolveStashThinking(raw, "", "SIG")
	if got != "chain" {
		t.Fatalf("ResolveStashThinking() = %q, want chain", got)
	}
}
