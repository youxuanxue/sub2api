package kiro

import (
	"strings"
	"testing"
)

// runtime.us-east-1.kiro.dev is the go-forward data-plane host and must be the
// preferred (first) endpoint, with the legacy q/codewhisperer hosts retained as
// automatic fallback. Guards the edge-first migration against an accidental
// re-ordering that would silently route serving traffic back to the deprecated
// amazonaws hosts.
func TestKiroEndpoints_RuntimeIsPreferredWithLegacyFallback(t *testing.T) {
	if len(kiroEndpoints) != 4 {
		t.Fatalf("expected 4 endpoints (runtime + 3 legacy), got %d", len(kiroEndpoints))
	}
	if got := kiroEndpoints[0].URL; got != "https://runtime.us-east-1.kiro.dev/generateAssistantResponse" {
		t.Fatalf("endpoint[0] must be the runtime.kiro.dev host, got %q", got)
	}
	if kiroEndpoints[0].AmzTarget != "AmazonCodeWhispererStreamingService.GenerateAssistantResponse" {
		t.Fatalf("runtime endpoint must carry the CodeWhisperer streaming X-Amz-Target, got %q", kiroEndpoints[0].AmzTarget)
	}

	// "auto" (the default selector) → runtime first, then all legacy hosts as fallback.
	auto := getSortedEndpoints("auto")
	if len(auto) != 4 {
		t.Fatalf("auto must keep the full fallback chain, got %d", len(auto))
	}
	if !strings.Contains(auto[0].URL, "runtime.us-east-1.kiro.dev") {
		t.Fatalf("auto[0] must be runtime.kiro.dev, got %q", auto[0].URL)
	}
	// Legacy hosts remain reachable as fallback so a runtime failure self-heals.
	var hasQ, hasCW bool
	for _, ep := range auto[1:] {
		if strings.Contains(ep.URL, "q.us-east-1.amazonaws.com") {
			hasQ = true
		}
		if strings.Contains(ep.URL, "codewhisperer.us-east-1.amazonaws.com") {
			hasCW = true
		}
	}
	if !hasQ || !hasCW {
		t.Fatalf("legacy q + codewhisperer must remain as fallback (q=%v cw=%v)", hasQ, hasCW)
	}
}

func TestGetSortedEndpoints_PreferenceMapping(t *testing.T) {
	cases := map[string]string{
		"runtime":       "runtime.us-east-1.kiro.dev",
		"kiro":          "q.us-east-1.amazonaws.com",
		"codewhisperer": "codewhisperer.us-east-1.amazonaws.com",
	}
	for pref, wantHost := range cases {
		eps := getSortedEndpoints(pref)
		if len(eps) == 0 {
			t.Fatalf("preference %q returned no endpoints", pref)
		}
		if !strings.Contains(eps[0].URL, wantHost) {
			t.Fatalf("preference %q: want primary host %q, got %q", pref, wantHost, eps[0].URL)
		}
	}
}
