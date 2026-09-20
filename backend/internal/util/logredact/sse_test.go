package logredact

import (
	"strings"
	"testing"
)

func TestRedactSSEStructuredDataPreservesFraming(t *testing.T) {
	input := "event: Bearer event-secret\ndata: {\"token\":\"secret\",\"content\":\"hello\"}\n\n"
	want := "event: Bearer ***\ndata: {\"content\":\"hello\",\"token\":\"***\"}\n\n"
	if got := RedactSSE(input); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRedactSSEStructuredDataRedactsSensitiveSuffixKeys(t *testing.T) {
	input := "data: {\"customer_password\":\"secret\",\"content\":\"hello\"}\n\n"
	got := RedactSSE(input)
	if strings.Contains(got, "secret") {
		t.Fatalf("sensitive suffix key leaked: %q", got)
	}
	if !strings.Contains(got, `"customer_password":"***"`) {
		t.Fatalf("expected suffix key redaction, got %q", got)
	}
}

func TestRedactSSEPreservesCRLFFraming(t *testing.T) {
	input := "event: message\r\ndata: {\"token\":\"secret\"}\r\n\r\n"
	want := "event: message\r\ndata: {\"token\":\"***\"}\r\n\r\n"
	if got := RedactSSE(input); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRedactSSEUnknownAndPartialEventsKeepKnownTokenCoverage(t *testing.T) {
	inputs := []string{
		"data: {\"token\":\"secret\"\n\n",
		"data: {\n data: \"token\":\"secret\"}\n\n",
	}
	for _, input := range inputs {
		got := RedactSSE(input)
		if strings.Contains(got, "secret") {
			t.Fatalf("known sensitive assignment leaked for %q: %q", input, got)
		}
	}
}

func TestRedactSSEMultilineDataDoesNotRewriteFraming(t *testing.T) {
	input := "event: message\ndata: {\"content\":\n"
	input += "data: \"ordinary\"}\n\n"
	got := RedactSSE(input)
	if !strings.Contains(got, "event: message\n") || !strings.Contains(got, "data:") {
		t.Fatalf("SSE framing changed: %q", got)
	}
}

func FuzzRedactSSENoPanic(f *testing.F) {
	for _, seed := range []string{
		"",
		"data: {}\n\n",
		"data: {\"token\":\"secret\"\n\n",
		"event: message\r\ndata: [DONE]\r\n\r\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		_ = RedactSSE(input)
	})
}
