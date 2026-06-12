package handler

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// The output-token ceiling feeds the hold's upper bound, so missing a surface's
// field name silently under-caps nothing (fallback is huge) but over-caps cost:
// every supported field spelling must be honoured, and absence must fall back
// to the conservative ceiling.
func TestTkParseMaxOutputTokens(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"chat max_tokens", `{"max_tokens":1024}`, 1024},
		{"chat max_completion_tokens", `{"max_completion_tokens":2048}`, 2048},
		{"responses max_output_tokens", `{"max_output_tokens":4096}`, 4096},
		{"max of multiple fields", `{"max_tokens":100,"max_output_tokens":300,"max_completion_tokens":200}`, 300},
		{"absent falls back", `{}`, tkHoldFallbackMaxOutputTokens},
		{"zero falls back", `{"max_tokens":0}`, tkHoldFallbackMaxOutputTokens},
		{"negative falls back", `{"max_tokens":-5}`, tkHoldFallbackMaxOutputTokens},
	}
	for _, tc := range cases {
		if got := tkParseMaxOutputTokens([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: tkParseMaxOutputTokens(%s) = %d, want %d", tc.name, tc.body, got, tc.want)
		}
	}
}

// The hand-off protocol is the release-ordering half of the overdraft
// invariant: once a usage-record task owns the refund, the handler's deferred
// release must become a no-op — otherwise the hold is refunded while the bill
// is still queued and the funds are re-exposed to concurrent admission.
func TestTkHoldHandle_HandOffDisablesDeferredRelease(t *testing.T) {
	h := &OpenAIGatewayHandler{gatewayService: &service.OpenAIGatewayService{}}
	hh := &tkHoldHandle{h: h, ctx: context.Background(), requestID: "local:req-1"}

	if got := hh.HandOffToSettlement(); got != "local:req-1" {
		t.Fatalf("HandOffToSettlement() = %q, want the hold request id", got)
	}
	if !hh.settling {
		t.Fatal("hand-off must mark the handle as settling")
	}
	// Must be a no-op (and must not panic on the zero gateway service): the
	// settlement transaction owns the refund now.
	hh.ReleaseUnlessSettling()
}

// A nil handle (request not gated: subscription / unpriced / no capability)
// must make both lifecycle calls safe no-ops so call sites need no nil checks.
func TestTkHoldHandle_NilSafe(t *testing.T) {
	var hh *tkHoldHandle
	hh.ReleaseUnlessSettling()
	if got := hh.HandOffToSettlement(); got != "" {
		t.Fatalf("nil handle HandOffToSettlement() = %q, want empty", got)
	}
}
