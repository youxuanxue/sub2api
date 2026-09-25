package logredact

import (
	"strings"
	"testing"
)

func TestRedactOnePassClassifiesJSONAndRedactsKeys(t *testing.T) {
	result := RedactOnePass([]byte(`{"token":"secret","text":"ordinary"}`), RedactOptions{})
	if result.Format != FormatJSON {
		t.Fatalf("format = %v, want JSON", result.Format)
	}
	if got := result.String(); !strings.Contains(got, `"token":"***"`) || strings.Contains(got, "secret") {
		t.Fatalf("redacted JSON = %q", got)
	}
}

func TestRedactOnePassClassifiesSSEAndPreservesFraming(t *testing.T) {
	input := "event: message\r\ndata: {\"api_key\":\"secret\"}\r\n\r\n"
	result := RedactOnePass([]byte(input), RedactOptions{})
	if result.Format != FormatSSE {
		t.Fatalf("format = %v, want SSE", result.Format)
	}
	if got := result.String(); got != "event: message\r\ndata: {\"api_key\":\"***\"}\r\n\r\n" {
		t.Fatalf("redacted SSE = %q", got)
	}
}

func TestRedactOnePassUnknownFormatPassthrough(t *testing.T) {
	input := []byte(" opaque custom format token=secret; result: success \n")
	result := RedactOnePass(input, RedactOptions{})
	if result.Format != FormatUnknown {
		t.Fatalf("format = %v, want unknown", result.Format)
	}
	if got, want := result.String(), string(input); got != want {
		t.Fatalf("unknown payload = %q, want %q", got, want)
	}
}

func BenchmarkRedactOnePassIdentifiedFormats(b *testing.B) {
	inputs := []struct {
		name string
		body []byte
	}{
		{name: "json", body: []byte(`{"text":"` + strings.Repeat("ordinary response text ", 128) + `","password":"secret"}`)},
		{name: "sse", body: []byte("event: message\ndata: {\"text\":\"" + strings.Repeat("ordinary response text ", 128) + "\",\"token\":\"secret\"}\n\n")},
		{name: "unknown", body: []byte(strings.Repeat("opaque custom format; result=success ", 128))},
	}
	for _, input := range inputs {
		b.Run(input.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(input.body)))
			for b.Loop() {
				_ = RedactOnePass(input.body, RedactOptions{})
			}
		})
	}
}
