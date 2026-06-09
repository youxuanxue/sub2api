package qa

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/tidwall/gjson"
)

func TestIsAnthropicThinkingOptIn(t *testing.T) {
	cases := []struct {
		platform    string
		dialogSynth bool
		want        bool
	}{
		{"anthropic", true, true},
		{"Anthropic", true, true},
		{" anthropic ", true, true},
		{"anthropic", false, false}, // 非 opt-in
		{"openai", true, false},     // 非 Anthropic
		{"gemini", true, false},
		{"", true, false},
	}
	for _, c := range cases {
		if got := isAnthropicThinkingOptIn(c.platform, c.dialogSynth); got != c.want {
			t.Errorf("isAnthropicThinkingOptIn(%q,%v)=%v want %v", c.platform, c.dialogSynth, got, c.want)
		}
	}
}

// 默认脱敏必须仍把 signature 抹成 ***（回归护栏）。
func TestRedactJSON_StillRedactsSignatureByDefault(t *testing.T) {
	body := `{"content":[{"type":"thinking","thinking":"reason","signature":"SIG_SECRET_abc"}]}`
	red := logredact.RedactJSON([]byte(body))
	if strings.Contains(red, "SIG_SECRET_abc") {
		t.Fatalf("default redaction leaked signature: %s", red)
	}
	if gjson.Get(red, "content.0.signature").String() != "***" {
		t.Fatalf("expected *** signature, got: %s", red)
	}
}

func TestRestoreThinkingSignatures_Response(t *testing.T) {
	original := `{"id":"msg_01","content":[` +
		`{"type":"thinking","thinking":"step","signature":"REAL_SIG_123"},` +
		`{"type":"text","text":"hi"},` +
		`{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"ls"}}` +
		`]}`
	redacted := logredact.RedactJSON([]byte(original))
	if gjson.Get(redacted, "content.0.signature").String() != "***" {
		t.Fatalf("precondition: signature should be redacted first")
	}
	out := restoreThinkingSignatures(redacted, []byte(original))
	if got := gjson.Get(out, "content.0.signature").String(); got != "REAL_SIG_123" {
		t.Errorf("thinking signature not restored: got %q", got)
	}
	// 非 thinking 块、其它字段不受影响。
	if got := gjson.Get(out, "content.1.text").String(); got != "hi" {
		t.Errorf("text block mangled: %q", got)
	}
}

// 关键安全约束：thinking 块外的 signature 不得被回填（仍保持 ***）。
func TestRestoreThinkingSignatures_OnlyThinkingBlocks(t *testing.T) {
	original := `{"signature":"TOPLEVEL_CRED","content":[` +
		`{"type":"text","text":"x","signature":"TEXTBLOCK_CRED"},` +
		`{"type":"thinking","thinking":"y","signature":"THINK_OK"}` +
		`]}`
	redacted := logredact.RedactJSON([]byte(original))
	out := restoreThinkingSignatures(redacted, []byte(original))
	if got := gjson.Get(out, "signature").String(); got != "***" {
		t.Errorf("top-level credential signature leaked: %q", got)
	}
	if got := gjson.Get(out, "content.0.signature").String(); got != "***" {
		t.Errorf("non-thinking block signature leaked: %q", got)
	}
	if got := gjson.Get(out, "content.1.signature").String(); got != "THINK_OK" {
		t.Errorf("thinking signature not restored: %q", got)
	}
}

func TestRestoreThinkingSignatures_RequestMessages(t *testing.T) {
	original := `{"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"q"}]},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"HIST_SIG"}]}` +
		`]}`
	redacted := logredact.RedactJSON([]byte(original))
	out := restoreThinkingSignatures(redacted, []byte(original))
	if got := gjson.Get(out, "messages.1.content.0.signature").String(); got != "HIST_SIG" {
		t.Errorf("history thinking signature not restored: %q", got)
	}
}

func TestRestoreThinkingSignatureInChunk(t *testing.T) {
	chunk := []byte("event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"STREAM_SIG_xyz"}}` +
		"\n\n")
	redacted := logredact.RedactText(string(chunk))
	if strings.Contains(redacted, "STREAM_SIG_xyz") {
		t.Fatalf("precondition: chunk signature should be redacted first: %s", redacted)
	}
	out := restoreThinkingSignatureInChunk(redacted, chunk)
	if !strings.Contains(out, "STREAM_SIG_xyz") {
		t.Errorf("stream signature not restored: %s", out)
	}
}

// 非 signature_delta 的 chunk 不应被改动。
func TestRestoreThinkingSignatureInChunk_NoOpForOtherDeltas(t *testing.T) {
	chunk := []byte("event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` +
		"\n\n")
	redacted := logredact.RedactText(string(chunk))
	out := restoreThinkingSignatureInChunk(redacted, chunk)
	if out != redacted {
		t.Errorf("non-signature chunk altered:\n got: %s\nwant: %s", out, redacted)
	}
}
