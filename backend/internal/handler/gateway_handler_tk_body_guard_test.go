package handler

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func TestTkEvalBodyGuard_EmptyConfig(t *testing.T) {
	reject, msg := TkEvalBodyGuard(nil, nil, "anthropic", "claude-opus-4-7", 1_000_000)
	if reject {
		t.Fatalf("expected reject=false on empty guards, got msg=%q", msg)
	}
}

func TestTkEvalBodyGuard_ZeroBody(t *testing.T) {
	guards := []config.UpstreamBodyGuardConfig{{Platform: "anthropic", ModelPrefix: "claude-opus-4-7", RejectBytes: 900000}}
	if reject, _ := TkEvalBodyGuard(nil, guards, "anthropic", "claude-opus-4-7", 0); reject {
		t.Fatalf("expected reject=false on bodyLen=0")
	}
}

func TestTkEvalBodyGuard_PlatformMismatch(t *testing.T) {
	guards := []config.UpstreamBodyGuardConfig{{Platform: "anthropic", ModelPrefix: "claude-opus-4-7", RejectBytes: 900000}}
	if reject, _ := TkEvalBodyGuard(nil, guards, "openai", "claude-opus-4-7", 1_000_000); reject {
		t.Fatalf("expected reject=false on platform mismatch")
	}
}

func TestTkEvalBodyGuard_ModelPrefixMismatch(t *testing.T) {
	guards := []config.UpstreamBodyGuardConfig{{Platform: "anthropic", ModelPrefix: "claude-opus-4-7", RejectBytes: 900000}}
	if reject, _ := TkEvalBodyGuard(nil, guards, "anthropic", "claude-sonnet-4-6", 1_000_000); reject {
		t.Fatalf("expected reject=false on model prefix mismatch")
	}
}

func TestTkEvalBodyGuard_BelowWarn(t *testing.T) {
	guards := []config.UpstreamBodyGuardConfig{{Platform: "anthropic", ModelPrefix: "claude-opus-4-7", WarnBytes: 600000, RejectBytes: 900000}}
	if reject, _ := TkEvalBodyGuard(nil, guards, "anthropic", "claude-opus-4-7", 500_000); reject {
		t.Fatalf("expected reject=false below warn threshold")
	}
}

func TestTkEvalBodyGuard_BetweenWarnAndReject(t *testing.T) {
	guards := []config.UpstreamBodyGuardConfig{{Platform: "anthropic", ModelPrefix: "claude-opus-4-7", WarnBytes: 600000, RejectBytes: 900000}}
	reject, msg := TkEvalBodyGuard(nil, guards, "anthropic", "claude-opus-4-7", 700_000)
	if reject {
		t.Fatalf("expected reject=false between warn and reject, got msg=%q", msg)
	}
}

func TestTkEvalBodyGuard_AboveReject(t *testing.T) {
	guards := []config.UpstreamBodyGuardConfig{{Platform: "anthropic", ModelPrefix: "claude-opus-4-7", WarnBytes: 600000, RejectBytes: 900000}}
	reject, msg := TkEvalBodyGuard(nil, guards, "anthropic", "claude-opus-4-7", 1_000_000)
	if !reject {
		t.Fatalf("expected reject=true above reject threshold")
	}
	if !strings.Contains(msg, "1000000") {
		t.Fatalf("reject msg should contain body bytes 1000000, got %q", msg)
	}
	if !strings.Contains(msg, "claude-opus-4-7") {
		t.Fatalf("reject msg should contain model name, got %q", msg)
	}
	if !strings.Contains(msg, "900000") {
		t.Fatalf("reject msg should contain reject limit 900000, got %q", msg)
	}
	if !strings.Contains(msg, "/compact") {
		t.Fatalf("reject msg should contain actionable hint /compact, got %q", msg)
	}
}

func TestTkEvalBodyGuard_RejectDisabledByZeroLimit(t *testing.T) {
	guards := []config.UpstreamBodyGuardConfig{{Platform: "anthropic", ModelPrefix: "claude-opus-4-7", WarnBytes: 600000, RejectBytes: 0}}
	if reject, _ := TkEvalBodyGuard(nil, guards, "anthropic", "claude-opus-4-7", 5_000_000); reject {
		t.Fatalf("expected reject=false when RejectBytes=0 (observe-only mode)")
	}
}

func TestTkEvalBodyGuard_EmptyModelPrefixMatchesAll(t *testing.T) {
	guards := []config.UpstreamBodyGuardConfig{{Platform: "anthropic", ModelPrefix: "", RejectBytes: 900000}}
	if reject, _ := TkEvalBodyGuard(nil, guards, "anthropic", "claude-haiku-4-5", 1_000_000); !reject {
		t.Fatalf("expected reject=true: empty ModelPrefix should match any model on the platform")
	}
}

func TestTkEvalBodyGuard_FirstMatchWins(t *testing.T) {
	guards := []config.UpstreamBodyGuardConfig{
		{Platform: "anthropic", ModelPrefix: "claude-opus-4-7", WarnBytes: 600000, RejectBytes: 0}, // observe-only
		{Platform: "anthropic", ModelPrefix: "claude-opus", WarnBytes: 0, RejectBytes: 500000},     // would reject
	}
	if reject, _ := TkEvalBodyGuard(nil, guards, "anthropic", "claude-opus-4-7", 1_000_000); reject {
		t.Fatalf("expected reject=false: first matching rule (observe-only) should win")
	}
}

func TestTkEvalBodyGuard_PlatformCaseInsensitive(t *testing.T) {
	guards := []config.UpstreamBodyGuardConfig{{Platform: "Anthropic", ModelPrefix: "claude-opus-4-7", RejectBytes: 900000}}
	if reject, _ := TkEvalBodyGuard(nil, guards, "ANTHROPIC", "claude-opus-4-7", 1_000_000); !reject {
		t.Fatalf("expected reject=true: platform comparison must be case-insensitive")
	}
}
