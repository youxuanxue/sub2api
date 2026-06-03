//go:build unit

package service

import (
	"bytes"
	"encoding/json"
	"testing"
	"unicode/utf8"
)

// tkBS is a single backslash byte (0x5C), constructed without any literal
// escape token so this source file never contains a `\u`+hex sequence that
// upstream tooling could fold into the actual rune before the test runs.
var tkBS = string(rune(92))

// tkU builds a JSON unicode escape "\uHHHH" at runtime (6 bytes).
func tkU(hex string) string { return tkBS + "u" + hex }

// tkUFFFD is the escaped U+FFFD replacement character the sanitizer substitutes
// for a lone surrogate escape.
var tkUFFFD = tkU("fffd")

// TestTkSanitizeRequestBodyUTF8_LoneSurrogateEscapes exhaustively covers the
// JSON \uHHHH surrogate-escape repair: lone high, lone low, end-of-body,
// high-followed-by-non-low, uppercase hex, and multiple occurrences.
func TestTkSanitizeRequestBodyUTF8_LoneSurrogateEscapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "lone high surrogate before closing quote",
			in:   `{"messages":[{"role":"user","content":"oops ` + tkU("d83d") + `"}]}`,
			want: `{"messages":[{"role":"user","content":"oops ` + tkUFFFD + `"}]}`,
		},
		{
			name: "lone low surrogate",
			in:   `{"text":"` + tkU("dc00") + ` tail"}`,
			want: `{"text":"` + tkUFFFD + ` tail"}`,
		},
		{
			name: "high surrogate followed by a non-low BMP escape",
			in:   `{"text":"` + tkU("d83d") + `A"}`,
			want: `{"text":"` + tkUFFFD + `A"}`,
		},
		{
			name: "high surrogate followed by another high surrogate",
			in:   `{"text":"` + tkU("d83d") + tkU("d83d") + `x"}`,
			want: `{"text":"` + tkUFFFD + tkUFFFD + `x"}`,
		},
		{
			name: "lone high surrogate at end of body (no room for pair)",
			in:   `{"text":"` + tkU("d83d"),
			want: `{"text":"` + tkUFFFD,
		},
		{
			name: "uppercase hex lone surrogate",
			in:   `{"text":"` + tkU("D83D") + `!"}`,
			want: `{"text":"` + tkUFFFD + `!"}`,
		},
		{
			name: "two lone surrogates in one body",
			in:   `{"a":"` + tkU("d800") + `","b":"` + tkU("dfff") + `"}`,
			want: `{"a":"` + tkUFFFD + `","b":"` + tkUFFFD + `"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := TkSanitizeRequestBodyUTF8([]byte(tc.in))
			if !changed {
				t.Fatalf("changed = false, want true")
			}
			if string(got) != tc.want {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
			if !utf8.Valid(got) {
				t.Fatalf("output not valid UTF-8: %q", got)
			}
		})
	}
}

// TestTkSanitizeRequestBodyUTF8_PreservesValidContent verifies that no
// legitimate body is mutated: valid surrogate pairs (escaped and raw),
// non-surrogate BMP escapes, ordinary escapes, escaped backslashes, raw
// multibyte UTF-8, and plain ASCII all pass through with changed=false and
// byte-identical output.
func TestTkSanitizeRequestBodyUTF8_PreservesValidContent(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"valid surrogate pair as escape (lowercase)", `{"text":"` + tkU("d83d") + tkU("de00") + `"}`},
		{"valid surrogate pair as escape (uppercase)", `{"text":"` + tkU("D83D") + tkU("DE00") + `"}`},
		{"raw utf8 emoji bytes", `{"text":"hi 😀 there"}`},
		{"raw utf8 CJK", `{"text":"你好，世界"}`},
		{"ordinary escapes only", `{"text":"line1` + tkBS + `nline2` + tkBS + `t` + tkBS + `"quoted` + tkBS + `" tail"}`},
		{"escaped backslash before u (literal, not an escape)", `{"text":"path ` + tkBS + tkBS + `udead is literal"}`},
		{"non-surrogate bmp escapes", `{"text":"` + tkU("0041") + tkU("00e9") + tkU("ffff") + `"}`},
		{"plain ascii", `{"model":"claude","max_tokens":100}`},
		{"empty body", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := []byte(tc.in)
			got, changed := TkSanitizeRequestBodyUTF8(in)
			if changed {
				t.Fatalf("changed = true for valid input; got %q", got)
			}
			if !bytes.Equal(got, in) {
				t.Fatalf("body mutated: got %q, want %q", got, in)
			}
		})
	}
}

// TestTkSanitizeRequestBodyUTF8_RawInvalidBytes covers Stage 2: maximal runs of
// invalid raw UTF-8 bytes (e.g. a truncated multi-byte rune) are replaced with
// the U+FFFD UTF-8 sequence, and the result is valid UTF-8.
func TestTkSanitizeRequestBodyUTF8_RawInvalidBytes(t *testing.T) {
	// 0xFF is never a valid UTF-8 byte; 0xE4 0xBD is a truncated 3-byte rune.
	in := append([]byte(`{"text":"bad `), 0xFF, 0xE4, 0xBD)
	in = append(in, []byte(`"}`)...)

	got, changed := TkSanitizeRequestBodyUTF8(in)
	if !changed {
		t.Fatalf("expected changed=true for invalid UTF-8 input")
	}
	if !utf8.Valid(got) {
		t.Fatalf("sanitized output is still invalid UTF-8: %q", got)
	}
	if !bytes.Contains(got, tkReplacementRune) {
		t.Fatalf("expected U+FFFD replacement in output, got %q", got)
	}
}

// TestTkSanitizeRequestBodyUTF8_CombinedFailures covers a body that has BOTH a
// lone surrogate escape AND a raw invalid byte — both stages must apply.
func TestTkSanitizeRequestBodyUTF8_CombinedFailures(t *testing.T) {
	in := append([]byte(`{"a":"`+tkU("d83d")+`","b":"`), 0xFF)
	in = append(in, []byte(`"}`)...)

	got, changed := TkSanitizeRequestBodyUTF8(in)
	if !changed {
		t.Fatalf("expected changed=true")
	}
	if !utf8.Valid(got) {
		t.Fatalf("output not valid UTF-8: %q", got)
	}
	if bytes.Contains(got, []byte(tkU("d83d"))) {
		t.Fatalf("lone surrogate escape survived: %q", got)
	}
	if !bytes.Contains(got, []byte(tkUFFFD)) {
		t.Fatalf("surrogate escape was not replaced with %s: %q", tkUFFFD, got)
	}
	if !bytes.Contains(got, tkReplacementRune) {
		t.Fatalf("raw invalid byte was not replaced with U+FFFD: %q", got)
	}
}

// TestTkSanitizeRequestBodyUTF8_Idempotent verifies sanitize(sanitize(x)) is a
// fixed point: a second pass over already-sanitized output is a no-op.
func TestTkSanitizeRequestBodyUTF8_Idempotent(t *testing.T) {
	in := append([]byte(`{"a":"`+tkU("d83d")+`","b":"x`), 0xFF)
	in = append(in, []byte(`"}`)...)

	first, changed := TkSanitizeRequestBodyUTF8(in)
	if !changed {
		t.Fatalf("expected first pass to change the body")
	}
	second, changedAgain := TkSanitizeRequestBodyUTF8(first)
	if changedAgain {
		t.Fatalf("second pass mutated already-clean body: %q -> %q", first, second)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("not idempotent: %q != %q", first, second)
	}
}

// TestTkSanitizeRequestBodyUTF8_RepairedBodyParses confirms that a realistic
// Anthropic request body carrying a lone surrogate becomes parseable JSON whose
// decoded string values contain no unpaired surrogate after repair.
func TestTkSanitizeRequestBodyUTF8_RepairedBodyParses(t *testing.T) {
	in := []byte(`{"model":"claude-opus-4-8","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"text","text":"screenshot ` + tkU("d83d") + ` dropped"}]}]}`)

	got, changed := TkSanitizeRequestBodyUTF8(in)
	if !changed {
		t.Fatalf("expected changed=true")
	}

	var parsed struct {
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("sanitized body failed to parse: %v\nbody=%q", err, got)
	}
	text := parsed.Messages[0].Content[0].Text
	for _, r := range text {
		if r >= 0xD800 && r <= 0xDFFF {
			t.Fatalf("decoded text still contains a surrogate code point: %q", text)
		}
	}
}

// TestTkSanitizeRequestBody_WrapperPassthrough verifies the logging wrapper
// returns the unmodified slice for clean input and a repaired slice otherwise.
func TestTkSanitizeRequestBody_WrapperPassthrough(t *testing.T) {
	clean := []byte(`{"text":"hi 😀"}`)
	if got := TkSanitizeRequestBody(clean, nil); !bytes.Equal(got, clean) {
		t.Fatalf("wrapper mutated clean body: %q", got)
	}

	dirty := []byte(`{"text":"` + tkU("d83d") + `"}`)
	got := TkSanitizeRequestBody(dirty, nil)
	if bytes.Contains(got, []byte(tkU("d83d"))) {
		t.Fatalf("wrapper did not repair lone surrogate: %q", got)
	}
}
