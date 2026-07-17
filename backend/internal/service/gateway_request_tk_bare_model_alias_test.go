//go:build unit

package service

import (
	"bytes"
	"reflect"
	"testing"
)

// Synthetic servable set exercising numeric compare (4-10 > 4-9), shorter-
// prefix-wins (haiku-4-5 vs dated) and family emergence (zenith is not
// hand-listed anywhere in the implementation).
var fixtureBareAliasSet = map[string]struct{}{
	"claude-opus-4-1": {}, "claude-opus-4-7": {}, "claude-opus-4-9": {}, "claude-opus-4-10": {},
	"claude-sonnet-4-5": {}, "claude-sonnet-4-6": {},
	"claude-haiku-4-5": {}, "claude-haiku-4-5-20251001": {},
	"claude-fable-5": {}, "claude-zenith-6-2": {},
}

func TestTkDeriveBareModelAliases_Fixture(t *testing.T) {
	want := map[string]string{
		"opus":   "claude-opus-4-10", // numeric, not lexicographic: 4-10 > 4-9
		"sonnet": "claude-sonnet-4-6",
		"haiku":  "claude-haiku-4-5", // strict-prefix tie → shorter (undated) wins
		"fable":  "claude-fable-5",
		"zenith": "claude-zenith-6-2", // new family auto-emerges, zero code edits
	}
	if got := tkDeriveBareModelAliases(fixtureBareAliasSet); !reflect.DeepEqual(got, want) {
		t.Fatalf("derived aliases:\n got=%v\nwant=%v", got, want)
	}
}

func TestTkDeriveDottedVersionAliases_Fixture(t *testing.T) {
	aliases := tkDeriveDottedVersionAliases(fixtureBareAliasSet)
	for in, want := range map[string]string{
		"opus-4.1":   "claude-opus-4-1",
		"opus-4.7":   "claude-opus-4-7",
		"opus-4.10":  "claude-opus-4-10",
		"sonnet-4.6": "claude-sonnet-4-6",
		"haiku-4.5":  "claude-haiku-4-5",
		"zenith-6.2": "claude-zenith-6-2",
	} {
		if got := aliases[in]; got != want {
			t.Errorf("dotted alias %q = %q, want %q", in, got, want)
		}
	}
	if got := aliases["haiku-4.5.20251001"]; got != "" {
		t.Fatalf("dated model must not derive a dotted shorthand alias, got %q", got)
	}
}

// Pin against the REAL servable table. When a servable-allowlist refresh
// changes a family's latest id, update this pin in the same PR — failure here
// is the deliberate human-confirmation gate for "the meaning of bare `opus`
// just changed" (by design, not brittleness).
func TestTkDeriveBareModelAliases_RealTablePin(t *testing.T) {
	aliases := tkDeriveBareModelAliases(supportedAnthropicCatalogModels)
	for family, want := range map[string]string{
		"opus": "claude-opus-4-8", "sonnet": "claude-sonnet-5",
		"haiku": "claude-haiku-4-5", "fable": "claude-fable-5",
	} {
		if got := aliases[family]; got != want {
			t.Errorf("real-table pin: bare %q → %q, pinned %q — servable allowlist changed; update pin consciously", family, got, want)
		}
	}
}

func TestTkResolveBareModelAlias_Trigger(t *testing.T) {
	aliases := tkDeriveAnthropicModelAliases(fixtureBareAliasSet)
	for in, want := range map[string]string{
		"opus": "claude-opus-4-10", "Opus ": "claude-opus-4-10", "OPUS": "claude-opus-4-10",
		"claude-opus": "claude-opus-4-10", "opus[1m]": "claude-opus-4-10",
		"opus-4.7": "claude-opus-4-7", "claude-opus-4.7": "claude-opus-4-7",
		"sonnet": "claude-sonnet-4-6", "claude-haiku": "claude-haiku-4-5",
		"fable": "claude-fable-5", "zenith": "claude-zenith-6-2",
	} {
		if got, ok := tkResolveBareModelAlias(in, aliases); !ok || got != want {
			t.Errorf("resolve(%q) = (%q, %v), want (%q, true)", in, got, ok, want)
		}
	}
	for _, in := range []string{
		"claude-opus-4-8",                            // full id — never rewritten
		"claude-opus-4-8[1m]",                        // strips to full id, still miss
		"claude-sonnet-4-5-20250929",                 // dated snapshot
		"claude-3-5-haiku-20241022",                  // retired: versioned → natural miss; deprecated interceptor owns it
		"Claude-Opus-4.8",                            // dotted variant without a fixture-backed canonical id
		"gpt", "gemini", "claude-zzz-5", "", "opusx", // no fuzzy/substring matching
	} {
		if got, ok := tkResolveBareModelAlias(in, aliases); ok {
			t.Errorf("resolve(%q) = (%q, true), want miss", in, got)
		}
	}
}

func TestTkApplyBareModelAlias_DottedVersionRewrite(t *testing.T) {
	for _, tc := range []struct {
		name     string
		model    string
		resolved string
	}{
		{"no prefix", "opus-4.7", "claude-opus-4-7"},
		{"claude prefix", "claude-opus-4.7", "claude-opus-4-7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := []byte(`{"model":"` + tc.model + `","max_tokens":1}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(append([]byte(nil), orig...)), PlatformAnthropic)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			newBody, resolved := TkApplyBareModelAlias(PlatformAnthropic, parsed)
			if resolved != tc.resolved {
				t.Fatalf("resolved = %q, want %q", resolved, tc.resolved)
			}
			want := []byte(`{"model":"` + tc.resolved + `","max_tokens":1}`)
			if !bytes.Equal(newBody, want) {
				t.Fatalf("dotted rewrite:\n got=%s\nwant=%s", newBody, want)
			}
			if parsed.Model != tc.resolved || !bytes.Equal(parsed.Body.Bytes(), want) {
				t.Fatalf("parsed not refreshed: model=%q body=%s", parsed.Model, parsed.Body.Bytes())
			}
		})
	}
}

func TestTkApplyBareModelAlias_SurgicalBodyRewrite(t *testing.T) {
	orig := []byte(`{"model":"opus","max_tokens":1,"metadata":{"x":"é"}}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(append([]byte(nil), orig...)), PlatformAnthropic)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	newBody, resolved := TkApplyBareModelAlias(PlatformAnthropic, parsed)
	if resolved != "claude-opus-4-8" { // real-table pin
		t.Fatalf("resolved = %q, want claude-opus-4-8", resolved)
	}
	want := []byte(`{"model":"claude-opus-4-8","max_tokens":1,"metadata":{"x":"é"}}`)
	if !bytes.Equal(newBody, want) {
		t.Fatalf("surgical rewrite:\n got=%s\nwant=%s", newBody, want)
	}
	if parsed.Model != "claude-opus-4-8" || !bytes.Equal(parsed.Body.Bytes(), want) {
		t.Fatalf("parsed not refreshed: model=%q body=%s", parsed.Model, parsed.Body.Bytes())
	}
}

func TestTkApplyBareModelAlias_MissAndGates(t *testing.T) {
	for _, tc := range []struct{ name, platform, body string }{
		{"full id byte-identical no-op", PlatformAnthropic, `{"model":"claude-opus-4-8","max_tokens":1}`},
		{"retired dated name stays for deprecated interceptor", PlatformAnthropic, `{"model":"claude-3-5-haiku-20241022","max_tokens":1}`},
		{"openai platform gated out even on bare name", PlatformOpenAI, `{"model":"opus","max_tokens":1}`},
		{"gemini platform gated out", PlatformGemini, `{"model":"opus","max_tokens":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := []byte(tc.body)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(append([]byte(nil), orig...)), PlatformAnthropic)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			origModel := parsed.Model
			if newBody, resolved := TkApplyBareModelAlias(tc.platform, parsed); resolved != "" || newBody != nil {
				t.Fatalf("expected miss, got (%s, %q)", newBody, resolved)
			}
			if parsed.Model != origModel || !bytes.Equal(parsed.Body.Bytes(), orig) {
				t.Fatal("parsed mutated on miss — must be byte-identical")
			}
		})
	}
	// Empty platform (no force-platform, no group) = anthropic default path → applies.
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"sonnet","max_tokens":1}`)), PlatformAnthropic)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, resolved := TkApplyBareModelAlias("", parsed); resolved != "claude-sonnet-5" {
		t.Fatalf("empty-platform gate: resolved = %q, want claude-sonnet-5", resolved)
	}
}
