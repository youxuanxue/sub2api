package kiro

import (
	"strings"
	"testing"
)

// Only endpoints named by current Kiro guidance/client resources belong in the
// retry chain: runtime is current, while q is an explicitly transitional legacy
// endpoint. Undocumented hosts or alternate legacy protocol shapes must not be
// added as speculative fallbacks.
func TestKiroEndpoints_OnlyOfficialRuntimeAndTransitionalQ(t *testing.T) {
	if len(kiroEndpoints) != 2 {
		t.Fatalf("expected runtime + transitional q only, got %d", len(kiroEndpoints))
	}
	if got := kiroEndpoints[0].URL; got != "https://runtime.us-east-1.kiro.dev/generateAssistantResponse" {
		t.Fatalf("endpoint[0] must be the runtime.kiro.dev host, got %q", got)
	}
	if kiroEndpoints[0].AmzTarget != "AmazonCodeWhispererStreamingService.GenerateAssistantResponse" {
		t.Fatalf("runtime endpoint must carry the CodeWhisperer streaming X-Amz-Target, got %q", kiroEndpoints[0].AmzTarget)
	}

	// "auto" (the default selector) -> runtime first, then transitional q.
	auto := getSortedEndpoints("auto")
	if len(auto) != 2 {
		t.Fatalf("auto must contain exactly the supported fallback chain, got %d", len(auto))
	}
	if !strings.Contains(auto[0].URL, "runtime.us-east-1.kiro.dev") {
		t.Fatalf("auto[0] must be runtime.kiro.dev, got %q", auto[0].URL)
	}
	if !strings.Contains(auto[1].URL, "q.us-east-1.amazonaws.com") {
		t.Fatalf("auto[1] must be the transitional q host, got %q", auto[1].URL)
	}
	for _, ep := range auto {
		if strings.Contains(ep.URL, "codewhisperer.") || strings.Contains(ep.AmzTarget, "AmazonQDeveloperStreamingService") {
			t.Fatalf("unsupported fallback remains configured: %+v", ep)
		}
		if ep.AmzTarget != "AmazonCodeWhispererStreamingService.GenerateAssistantResponse" {
			t.Fatalf("endpoint %q uses unsupported target %q", ep.URL, ep.AmzTarget)
		}
	}
}

func TestGetSortedEndpoints_PreferenceMapping(t *testing.T) {
	cases := map[string]string{
		"runtime":       "runtime.us-east-1.kiro.dev",
		"kiro":          "q.us-east-1.amazonaws.com",
		"codewhisperer": "runtime.us-east-1.kiro.dev",
		"amazonq":       "q.us-east-1.amazonaws.com",
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
