package logredact

import (
	"encoding/json"
	"strings"
	"testing"
)

// Keep the pre-optimization pipeline as an oracle: guards must never change
// the output, including replacement ordering and case-insensitive matches.
func redactUnstructuredReference(input string, patterns *textRedactPatterns) string {
	out := rePrivateKey.ReplaceAllString(input, "<private key redacted>")
	out = reProviderToken.ReplaceAllString(out, "***")
	out = reBearer.ReplaceAllString(out, "Bearer ***")
	out = reGOCSPX.ReplaceAllString(out, "GOCSPX-***")
	out = reAIza.ReplaceAllString(out, "AIza***")
	out = patterns.reJSONLike.ReplaceAllString(out, `$1***$3`)
	out = patterns.reQueryLike.ReplaceAllString(out, `$1=***`)
	return patterns.rePlain.ReplaceAllString(out, `$1$2***`)
}

func redactionGuardCorpus() []string {
	inputs := []string{
		"", "***", " ordinary text\n中文内容 ", `{"message":"ordinary: content"}`,
		"-----BEGIN PRIVATE KEY-----\nmaterial",
		"-----BEGIN RSA PRIVATE KEY-----\nmaterial\n-----END RSA PRIVATE KEY-----",
		"Bearer a", "bEaReR\nabc._~+/=-", "notBearer abc", "Bearer", "BEARER\tabc",
		"GOCSPX-" + strings.Repeat("a", 24), "AIza" + strings.Repeat("a", 35),
		"AKIA" + strings.Repeat("A", 16), "ASIA" + strings.Repeat("9", 16),
		"LTAI" + strings.Repeat("a", 12), "glpat-" + strings.Repeat("a", 16),
		"sk-" + strings.Repeat("a", 20), "github_pat_" + strings.Repeat("a", 20),
		`"api_key":"abc"`, "TOKEN = abc", "?token=abc&safe=yes", "ſecret: abc",
		`"x.custom key":"abc"`, "x.custom key = abc", "x.custom key=abc",
	}
	for _, kind := range "pousr" {
		inputs = append(inputs, "gh"+string(kind)+"_"+strings.Repeat("a", 20))
	}
	for _, key := range append(append([]string(nil), defaultSensitiveKeyList...), "x.custom key", `custom"`, "custom:key", "custom=key", "ключ", "k") {
		for _, spelling := range []string{key, strings.ToUpper(key), strings.ReplaceAll(strings.ReplaceAll(key, "s", "ſ"), "k", "K")} {
			inputs = append(inputs, `"`+spelling+`" : "abc"`, spelling+"\t:\tabc", spelling+"=abc&safe=yes", "prefix-"+spelling+"=abc")
		}
	}
	return inputs
}

func TestRedactionGuardsMatchOriginalPipeline(t *testing.T) {
	for _, patterns := range []*textRedactPatterns{defaultTextRedactPatterns, getTextRedactPatterns([]string{"x.custom key", `custom"`, "custom:key", "custom=key", "ключ", "k"})} {
		for _, input := range redactionGuardCorpus() {
			for _, wrapped := range []string{input, "prefix " + input + " suffix", input + " password=abc"} {
				if got, want := redactUnstructuredText(wrapped, patterns), redactUnstructuredReference(wrapped, patterns); got != want {
					t.Fatalf("input %q: got %q, want %q", wrapped, got, want)
				}
			}
		}
	}
}

func FuzzRedactionGuardsMatchOriginalPipeline(f *testing.F) {
	for _, input := range redactionGuardCorpus() {
		f.Add(input)
	}
	patterns := getTextRedactPatterns([]string{"x.custom key", `custom"`, "custom:key", "custom=key", "ключ", "k"})
	f.Fuzz(func(t *testing.T, input string) {
		if got, want := redactUnstructuredText(input, patterns), redactUnstructuredReference(input, patterns); got != want {
			t.Fatalf("input %q: got %q, want %q", input, got, want)
		}
	})
}

func BenchmarkRedactJSONResponses(b *testing.B) {
	for _, prose := range []struct{ name, text string }{
		{"ordinary", "The response explains how to build and verify the application. "},
		{"punctuation", "Example: build the application; result = successful. "},
	} {
		b.Run(prose.name, func(b *testing.B) {
			content := make([]any, 512)
			for i := range content {
				content[i] = map[string]any{"type": "output_text", "text": strings.Repeat(prose.text, 8)}
			}
			raw, err := json.Marshal(map[string]any{"output": content})
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(raw)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				RedactJSON(raw)
			}
		})
	}
}

func BenchmarkRedactSSEChunk(b *testing.B) {
	for _, size := range []struct {
		name    string
		repeats int
	}{{"small", 1}, {"large", 256}} {
		b.Run(size.name, func(b *testing.B) {
			payload, err := json.Marshal(map[string]any{"type": "response.output_text.delta", "delta": strings.Repeat("Example: build the application; result = successful. ", size.repeats)})
			if err != nil {
				b.Fatal(err)
			}
			input := "event: response.output_text.delta\ndata: " + string(payload) + "\n\n"
			for _, path := range []struct {
				name   string
				redact func(string) string
			}{
				{"original", func(s string) string {
					return redactUnstructuredReference(strings.TrimSpace(s), defaultTextRedactPatterns)
				}},
				{"optimized", func(s string) string { return RedactText(s) }},
			} {
				b.Run(path.name, func(b *testing.B) {
					b.SetBytes(int64(len(input)))
					b.ReportAllocs()
					for b.Loop() {
						path.redact(input)
					}
				})
			}
		})
	}
}

func TestRedactSSEPreservesOriginalCoverage(t *testing.T) {
	for _, raw := range []string{
		"data: [DONE]\n\n",
		`data: {"token":"hidden","delta":"truncated`,
		"data: {\ndata: \"token\":\"hidden\"}\n\n",
		"data: -----BEGIN PRIVATE KEY-----\ndata: hidden\n\n",
		"event: Bearer\ndata: {}\n\n",
		"id: token=hidden\ndata: {}\n\n",
		"data: {\"password=hidden\":\"ordinary\"}\n\n",
		"event: content_block_delta\ndata: {\"delta\":{\"type\":\"signature_delta\",\"signature\":\"hidden\"}}\n\n",
	} {
		if got, want := RedactText(raw), redactUnstructuredReference(strings.TrimSpace(raw), defaultTextRedactPatterns); got != want {
			t.Fatalf("input %q: got %q, want %q", raw, got, want)
		}
	}
}
