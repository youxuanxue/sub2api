package geminicli

import (
	"regexp"
	"strings"
	"testing"
)

// Gemini CLI 0.49+ emits User-Agent as GeminiCLI/<semver>/<model> (<platform>; <arch>).
func TestGeminiCLIUserAgentShape(t *testing.T) {
	t.Parallel()

	const wantVersion = "0.52.0"
	if !strings.Contains(GeminiCLIUserAgent, "GeminiCLI/"+wantVersion+"/") {
		t.Fatalf("UA missing GeminiCLI/%s/ prefix segment: %q", wantVersion, GeminiCLIUserAgent)
	}
	if !regexp.MustCompile(`GeminiCLI/\d+\.\d+\.\d+/[a-z0-9.-]+ \([a-z0-9]+; [a-z0-9]+\)`).MatchString(GeminiCLIUserAgent) {
		t.Fatalf("UA does not match Gemini CLI 0.49+ shape: %q", GeminiCLIUserAgent)
	}
}
