package trajectory

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/ent"
)

func TestWireShapeForRecord(t *testing.T) {
	cases := []struct {
		name     string
		platform string
		endpoint string
		want     WireShape
	}{
		// anthropic + kiro + antigravity /v1 all relay the /v1/messages shape.
		{"anthropic messages", "anthropic", "/v1/messages", WireAnthropicMessages},
		{"kiro messages", "kiro", "/v1/messages", WireAnthropicMessages},
		{"antigravity v1 normalizes to messages", "antigravity", "/v1/messages", WireAnthropicMessages},
		// openai-compat: chat vs responses disambiguated purely by endpoint.
		{"openai chat", "openai", "/v1/chat/completions", WireOpenAIChat},
		{"openai responses", "openai", "/v1/responses", WireOpenAIResponses},
		{"grok chat", "grok", "/v1/chat/completions", WireOpenAIChat},
		// newapi splits per record: a ch41 gemini record vs a chat record under
		// the SAME platform value — proves endpoint is the primary discriminator.
		{"newapi chat record", "newapi", "/v1/chat/completions", WireOpenAIChat},
		{"newapi gemini (ch41) record", "newapi", "/v1beta/models", WireGemini},
		// gemini + antigravity /v1beta speak the Gemini contents[] shape.
		{"gemini generateContent", "gemini", "/v1beta/models", WireGemini},
		{"antigravity v1beta normalizes to gemini", "antigravity", "/v1beta/models", WireGemini},
		// non-conversation endpoints are not projectable (skip, not garbage).
		{"openai embeddings", "openai", "/v1/embeddings", WireUnknown},
		{"openai images", "openai", "/v1/images/generations", WireUnknown},
		{"openai video", "openai", "/v1/video/generations", WireUnknown},
		// platform fallback for single-shape platforms with an odd/empty endpoint.
		{"anthropic empty endpoint falls back", "anthropic", "", WireAnthropicMessages},
		{"gemini empty endpoint falls back", "gemini", "", WireGemini},
		// openai with no endpoint marker is ambiguous (chat vs responses) → skip.
		{"openai empty endpoint is unknown", "openai", "", WireUnknown},
		{"unknown platform empty endpoint", "mystery", "", WireUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WireShapeForRecord(&ent.QARecord{Platform: tc.platform, InboundEndpoint: tc.endpoint})
			if got != tc.want {
				t.Errorf("WireShapeForRecord(%q,%q) = %q, want %q", tc.platform, tc.endpoint, got, tc.want)
			}
		})
	}
}

func TestWireShapeForRecord_NilSafe(t *testing.T) {
	if got := WireShapeForRecord(nil); got != WireUnknown {
		t.Errorf("nil record = %q, want Unknown", got)
	}
}

// recordsContinue / RequestContinues: a wire-shape change is a hard session
// boundary even when the message arrays would otherwise look continuous.
func TestRequestContinues_ShapeBoundary(t *testing.T) {
	mk := func(platform, endpoint, body string) SourceRecord {
		blob := &EvidenceBlob{}
		blob.Request.Body = mustBody(t, body)
		return SourceRecord{
			Record: &ent.QARecord{Platform: platform, InboundEndpoint: endpoint},
			Blob:   blob,
		}
	}
	anth1 := mk("anthropic", "/v1/messages", `{"messages":[{"role":"user","content":"a"}]}`)
	anth2 := mk("anthropic", "/v1/messages", `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`)
	gem := mk("gemini", "/v1beta/models", `{"contents":[{"role":"user","parts":[{"text":"a"}]}]}`)

	if !RequestContinues(anth1, anth2) {
		t.Errorf("same-shape prefix extension should continue")
	}
	if RequestContinues(anth1, gem) {
		t.Errorf("anthropic→gemini shape change must be a hard boundary")
	}
	if RequestContinues(gem, anth2) {
		t.Errorf("gemini→anthropic shape change must be a hard boundary")
	}
}
