//go:build unit

package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// TestTkStripContextWindowModelAlias exercises the pure core: a trailing
// Claude Code context-window alias suffix (e.g. "[1m]") is removed, every other
// id passes through byte-for-byte. Covers claude-code#60913 / #50803 / #53031.
func TestTkStripContextWindowModelAlias(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantModel string
		wantStrip bool
	}{
		// The #60913 evidence cases: cc serializes these verbatim → upstream 404.
		{"opus48 1m", "claude-opus-4-8[1m]", "claude-opus-4-8", true},
		{"opus47 1m", "claude-opus-4-7[1m]", "claude-opus-4-7", true},
		{"sonnet46 1m", "claude-sonnet-4-6[1m]", "claude-sonnet-4-6", true},
		{"uppercase M", "claude-opus-4-8[1M]", "claude-opus-4-8", true},
		{"k unit", "claude-opus-4-8[200k]", "claude-opus-4-8", true},
		{"uppercase K", "claude-opus-4-8[200K]", "claude-opus-4-8", true},

		// No-ops: bare ids and edge shapes must pass through unchanged.
		{"bare opus", "claude-opus-4-8", "claude-opus-4-8", false},
		{"bare sonnet", "claude-sonnet-4-6", "claude-sonnet-4-6", false},
		{"empty", "", "", false},
		{"non-anthropic", "deepseek-v4-pro", "deepseek-v4-pro", false},
		// Bracket not anchored to end → not a trailing alias, leave it alone.
		{"mid-string bracket", "claude-[x]-foo", "claude-[x]-foo", false},
		// Bracket lacks a unit letter → not a context-window alias.
		{"no unit letter", "claude-opus-4-8[1]", "claude-opus-4-8[1]", false},
		// Bracket has letters but no leading digit → not the alias shape.
		{"no leading digit", "claude-opus-4-8[m]", "claude-opus-4-8[m]", false},
		// Trailing text after the bracket → not anchored, untouched.
		{"suffix after bracket", "claude-opus-4-8[1m]-preview", "claude-opus-4-8[1m]-preview", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, stripped := tkStripContextWindowModelAlias(tc.in)
			if got != tc.wantModel || stripped != tc.wantStrip {
				t.Fatalf("tkStripContextWindowModelAlias(%q) = (%q, %v); want (%q, %v)",
					tc.in, got, stripped, tc.wantModel, tc.wantStrip)
			}

			// Idempotence: a no-op id, or the already-stripped result, must be stable.
			if again, reStrip := tkStripContextWindowModelAlias(got); again != got || reStrip {
				t.Fatalf("not idempotent: tkStripContextWindowModelAlias(%q) = (%q, %v); want (%q, false)",
					got, again, reStrip, got)
			}
		})
	}
}

// TestTkStripContextWindowModelAlias_WireBodyRewrite proves the end-to-end shape
// used at the forward call sites: read model -> strip -> ReplaceModelInBody. The
// rewritten wire body must carry the bare model and stay valid JSON, while an
// already-bare body is returned untouched.
func TestTkStripContextWindowModelAlias_WireBodyRewrite(t *testing.T) {
	t.Run("alias rewritten to bare", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-4-8[1m]","max_tokens":1024,` +
			`"messages":[{"role":"user","content":"hi"}]}`)

		bare, stripped := tkStripContextWindowModelAlias(gjson.GetBytes(body, "model").String())
		if !stripped || bare != "claude-opus-4-8" {
			t.Fatalf("strip = (%q, %v); want (claude-opus-4-8, true)", bare, stripped)
		}

		out := ReplaceModelInBody(body, bare)
		if !json.Valid(out) {
			t.Fatalf("rewritten body is not valid JSON: %s", out)
		}
		if got := gjson.GetBytes(out, "model").String(); got != "claude-opus-4-8" {
			t.Fatalf("wire model = %q; want claude-opus-4-8", got)
		}
		// The rest of the body must be preserved verbatim.
		if got := gjson.GetBytes(out, "max_tokens").Int(); got != 1024 {
			t.Fatalf("max_tokens = %d; want 1024", got)
		}
		if got := gjson.GetBytes(out, "messages.0.content").String(); got != "hi" {
			t.Fatalf("messages[0].content = %q; want hi", got)
		}
	})

	t.Run("bare body is a no-op", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)
		if _, stripped := tkStripContextWindowModelAlias(gjson.GetBytes(body, "model").String()); stripped {
			t.Fatalf("bare model unexpectedly reported as aliased")
		}
	})
}

// TestTkStripContextWindowModelAlias_BillingModelIsBare guards the claude-code#60913
// billing regression. The Forward / passthrough paths feed the (stripped) model into
// forwardResultBillingModel, whose result is the pricing/quota key. If the alias
// survived, billing would resolve `claude-opus-4-8[1m]` — which has no pricing entry —
// to zero/fallback cost, leaking quota on exactly the 1M requests this fix newly lets
// succeed. After stripping, the billing key must be the bare id.
func TestTkStripContextWindowModelAlias_BillingModelIsBare(t *testing.T) {
	clientModel := "claude-opus-4-8[1m]"
	bare, aliased := tkStripContextWindowModelAlias(clientModel)
	if !aliased || bare != "claude-opus-4-8" {
		t.Fatalf("strip = (%q, %v); want (claude-opus-4-8, true)", bare, aliased)
	}

	// forwardResultBillingModel prefers the requested (ForwardResult.Model) value;
	// the Forward path now assigns the bare id to originalModel -> ForwardResult.Model,
	// so the billing key is bare and resolves against a real pricing entry.
	billing := forwardResultBillingModel(bare, bare)
	if billing != "claude-opus-4-8" {
		t.Fatalf("billing model = %q; want claude-opus-4-8", billing)
	}
	if strings.ContainsAny(billing, "[]") {
		t.Fatalf("billing model still carries a context-window alias: %q", billing)
	}
}
