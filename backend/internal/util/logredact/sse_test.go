package logredact

import (
	"encoding/json"
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

func TestRedactSSEStructuredDataRedactsEmbeddedAssignmentKey(t *testing.T) {
	input := "data: {\"password=hidden\":\"ordinary\",\"content\":\"hello\"}\n\n"
	got := RedactSSE(input)
	want := "data: {\"content\":\"hello\",\"password=***\":\"ordinary\"}\n\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRedactSSERawFramingCredentialsPreserveStructuredPayload(t *testing.T) {
	input := "data: {\"password=hidden\":\"ordinary\"}\nid: password=hidden\n\n"
	want := "data: {\"password=***\":\"ordinary\"}\nid: password=***\n\n"
	if got := RedactSSE(input); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	payload := strings.TrimPrefix(strings.SplitN(want, "\n", 2)[0], "data: ")
	if !json.Valid([]byte(payload)) {
		t.Fatalf("expected structured payload to remain valid JSON: %q", want)
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

func TestRedactSSEEscapedCredentialsAndFraming(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"escaped token", `data: {"text":"\u0042earer hidden"}` + "\n\n", "data: {\"text\":\"Bearer ***\"}\n\n"},
		{"escaped key", `data: {"to\u006ben":"hidden"}` + "\n\n", "data: {\"token\":\"***\"}\n\n"},
		{"surrogate key", `data: {"\ud83d\ude00_password":"hidden"}` + "\n\n", "data: {\"😀_password\":\"***\"}\n\n"},
		{"key whitespace", "data: {\" token \":\"hidden\"}\n\n", "data: {\" token \":\"***\"}\n\n"},
		{"mixed newlines", "event: message\r\ndata: {\"token\":\"hidden\"}\n\n", "event: message\r\ndata: {\"token\":\"***\"}\n\n"},
		{"metadata whitespace", " : keep  \nevent: message  \ndata: {\"token\":\"hidden\"}\n\n", " : keep  \nevent: message  \ndata: {\"token\":\"***\"}\n\n"},
		{"multiline data", " data: header  \ndata: {\ndata: \"token\":\"hidden\"}\n", " data: header  \ndata: {\ndata: \"token\":\"***\"}\n"},
		{"CR frames", "data: {\"token\":\"hidden\"}\r\rdata: [DONE]\r\r", "data: {\"token\":\"***\"}\r\rdata: [DONE]\r\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactSSE(tc.input); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRedactSSERawPrivateKeyAcrossEvents(t *testing.T) {
	for _, input := range []string{
		"data: -----BEGIN PRIVATE KEY-----\n\nhidden\n-----END PRIVATE KEY-----\n\n",
		"event: -----BEGIN PRIVATE KEY-----\nhidden\ndata: {}\n\n",
		"event: Bearer\n\nhidden\n\n",
		"data: token=\n\nhidden\n\n",
	} {
		if got := RedactSSE(input); strings.Contains(got, "hidden") {
			t.Fatalf("private key leaked: %q", got)
		}
	}
}

func FuzzRedactSSEStructuredEquivalence(f *testing.F) {
	for _, seed := range [][2]string{
		{"text", "Bearer hidden"}, {" token ", "hidden"},
		{"custom_password", "hidden"}, {"text", "ordinary prose"},
		{"password=hidden", "ordinary"}, {"text", "token=abc,\"password\":\"a b\""},
		{"text", "-----BEGIN PRIVATE KEY-----\nhidden"},
	} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, key, value string) {
		raw, err := json.Marshal(map[string]string{key: value})
		if err != nil {
			t.Fatal(err)
		}
		want := "data: " + RedactJSON(raw) + "\n\n"
		if got := RedactSSE("data: " + string(raw) + "\n\n"); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}
