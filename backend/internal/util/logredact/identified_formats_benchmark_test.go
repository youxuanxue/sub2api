package logredact

import (
	"strings"
	"testing"
)

// logredact-v3 baseline from main before this PR; retain its guarded regexp
// dispatch so benchmarks do not exaggerate gains by comparing to eight scans.
func redactUnstructuredV3(input string, patterns *textRedactPatterns) string {
	out := input
	// Each guard checks a necessary literal from its regexp, not a guess about
	// what a credential looks like. Keep the replacement order and check the
	// current output: earlier replacements can expose later matches.
	if strings.Contains(out, "-----BEGIN ") {
		out = rePrivateKey.ReplaceAllString(out, "<private key redacted>")
	}
	if strings.Contains(out, "sk-") || containsGitHubTokenPrefix(out) ||
		strings.Contains(out, "github_pat_") ||
		strings.Contains(out, "glpat-") || strings.Contains(out, "AKIA") ||
		strings.Contains(out, "ASIA") || strings.Contains(out, "LTAI") {
		out = reProviderToken.ReplaceAllString(out, "***")
	}
	if containsBearer(out) {
		out = reBearer.ReplaceAllString(out, "Bearer ***")
	}
	if strings.Contains(out, "GOCSPX-") {
		out = reGOCSPX.ReplaceAllString(out, "GOCSPX-***")
	}
	if strings.Contains(out, "AIza") {
		out = reAIza.ReplaceAllString(out, "AIza***")
	}
	if !patterns.mayContainAssignment(out) {
		return out
	}
	if strings.Contains(out, ":") && strings.Contains(out, `"`) {
		out = patterns.reJSONLike.ReplaceAllString(out, `$1***$3`)
	}
	if strings.Contains(out, "=") {
		out = patterns.reQueryLike.ReplaceAllString(out, `$1=***`)
	}
	if strings.ContainsAny(out, ":=") {
		out = patterns.rePlain.ReplaceAllString(out, `$1$2***`)
	}
	return out
}

func BenchmarkIdentifiedFormats(b *testing.B) {
	for _, tc := range []struct{ name, input string }{
		{"json", `{"text":"` + strings.Repeat("The thought process explains the result. ", 256) + `"}`},
		{"sse_json", "event: message\ndata: {\"text\":\"" + strings.Repeat("The thought process explains the result. ", 256) + "\"}\n\n"},
		{"assignment", strings.Repeat(`token=synthetic&safe=yes "password":"synthetic" `, 256)},
		{"known_token", strings.Repeat("Bearer synthetic-token ", 256)},
		{"unknown_passthrough", strings.Repeat("ordinary custom_field=value; result: successful. ", 256)},
	} {
		for _, version := range []string{"v3", "v4"} {
			if tc.name == "json" && version == "v3" {
				continue
			}
			b.Run(tc.name+"/"+version, func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(tc.input)))
				for b.Loop() {
					switch {
					case tc.name == "json":
						RedactJSON([]byte(tc.input))
					case version == "v3":
						redactUnstructuredV3(strings.TrimSpace(tc.input), defaultTextRedactPatterns)
					case tc.name == "sse_json":
						RedactSSE(tc.input)
					default:
						redactUnstructuredText(tc.input, defaultTextRedactPatterns)
					}
				}
			})
		}
	}
}
