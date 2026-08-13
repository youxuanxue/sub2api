package newapi

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

func TestIsXRTokenBaseURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		channelType int
		base        string
		want        bool
	}{
		{"canonical root", newapiconstant.ChannelTypeDoubaoVideo, XRTokenBaseURL, true},
		{"trailing slash", newapiconstant.ChannelTypeDoubaoVideo, XRTokenBaseURL + "/", true},
		// XRToken's own SDK docs tell users to configure ".../v1"; admins paste
		// it verbatim, so it must resolve to the same account.
		{"trailing v1", newapiconstant.ChannelTypeDoubaoVideo, XRTokenBaseURL + "/v1", true},
		{"trailing v1 slash", newapiconstant.ChannelTypeDoubaoVideo, XRTokenBaseURL + "/v1/", true},
		{"uppercase host", newapiconstant.ChannelTypeDoubaoVideo, "https://API.XRToken.net", true},
		{"padded", newapiconstant.ChannelTypeDoubaoVideo, "  " + XRTokenBaseURL + "  ", true},

		{"empty", newapiconstant.ChannelTypeDoubaoVideo, "", false},
		{"ark host", newapiconstant.ChannelTypeDoubaoVideo, "https://ark.cn-beijing.volces.com", false},
		// Overseas host is deliberately NOT the mainland sentinel.
		{"overseas host", newapiconstant.ChannelTypeDoubaoVideo, "https://api.xrtoken.ai", false},
		// Guard against a suffix-match bug letting a lookalike host through.
		{"lookalike suffix", newapiconstant.ChannelTypeDoubaoVideo, "https://evil-api.xrtoken.net.example.com", false},

		// ch45 exposes chat/embeddings/images paths XRToken does not serve in
		// Ark shape, so the same host on ch45 must not be treated as XRToken.
		{"ch45 same host", newapiconstant.ChannelTypeVolcEngine, XRTokenBaseURL, false},
		{"ch1 same host", newapiconstant.ChannelTypeOpenAI, XRTokenBaseURL, false},
		{"channel zero", 0, XRTokenBaseURL, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsXRTokenBaseURL(tc.channelType, tc.base); got != tc.want {
				t.Fatalf("IsXRTokenBaseURL(%d, %q) = %v, want %v", tc.channelType, tc.base, got, tc.want)
			}
		})
	}
}

// The wrapper appends "/v1/contents/generations/tasks", so normalization MUST
// strip a stored "/v1" or the upstream URL doubles it. Non-XRToken input passes
// through unchanged so callers can use this as a general normalizer.
func TestNormalizeXRTokenBaseURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{XRTokenBaseURL, XRTokenBaseURL},
		{XRTokenBaseURL + "/", XRTokenBaseURL},
		{XRTokenBaseURL + "/v1", XRTokenBaseURL},
		{XRTokenBaseURL + "/v1/", XRTokenBaseURL},
		{"", ""},
		{"https://ark.cn-beijing.volces.com", "https://ark.cn-beijing.volces.com"},
		{"https://ark.cn-beijing.volces.com/", "https://ark.cn-beijing.volces.com"},
	}
	for _, tc := range cases {
		if got := NormalizeXRTokenBaseURL(tc.in); got != tc.want {
			t.Fatalf("NormalizeXRTokenBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestXRTokenUpstreamVideoModel pins the vendor-namespace rule and, more
// importantly, its idempotence.
//
// The rule itself was verified against the live public catalog (GET
// https://api.xrtoken.net/v1/models, no auth required): all 60 published ids
// carry a vendor prefix and every `type:"video"` id is `volcengine/<ark-id>`.
//
// Idempotence is the load-bearing half. Account 96 was originally provisioned
// with a hand-written PREFIXED model_mapping (that was the documented shape
// before the prefix moved into the adaptor), so a live account can still hand us
// an already-namespaced id. Prefixing that again would produce
// `volcengine/volcengine/...` and 404 — a regression that would only surface in
// production, on exactly the account this feature exists for.
func TestXRTokenUpstreamVideoModel(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		// Bare Ark ids gain the namespace.
		"doubao-seedance-2-5-260628":      "volcengine/doubao-seedance-2-5-260628",
		"doubao-seedance-2.0-mini":        "volcengine/doubao-seedance-2.0-mini",
		"doubao-seedance-1-5-pro-251215":  "volcengine/doubao-seedance-1-5-pro-251215",
		"doubao-seedance-2-0-260128":      "volcengine/doubao-seedance-2-0-260128",
		"doubao-seedance-2-0-fast-260128": "volcengine/doubao-seedance-2-0-fast-260128",
		// Already namespaced — unchanged (idempotent).
		"volcengine/doubao-seedance-2-5-260628": "volcengine/doubao-seedance-2-5-260628",
		// Any other vendor namespace is left alone rather than double-prefixed.
		"ctyun/glm-5.2": "ctyun/glm-5.2",
		// Degenerate input stays degenerate instead of becoming a bare prefix.
		"":    "",
		"   ": "",
	}
	for in, want := range cases {
		if got := XRTokenUpstreamVideoModel(in); got != want {
			t.Fatalf("XRTokenUpstreamVideoModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestXRTokenUpstreamVideoModel_IsIdempotentUnderRepetition states the
// invariant directly: applying the rule twice equals applying it once. A future
// refactor that switches to unconditional concatenation passes the table above
// only if it also drops the already-prefixed rows; this closes that gap.
func TestXRTokenUpstreamVideoModel_IsIdempotentUnderRepetition(t *testing.T) {
	t.Parallel()
	for _, id := range []string{
		"doubao-seedance-2-5-260628",
		"volcengine/doubao-seedance-2-5-260628",
		"doubao-seedance-2.0-mini",
	} {
		once := XRTokenUpstreamVideoModel(id)
		twice := XRTokenUpstreamVideoModel(once)
		if once != twice {
			t.Fatalf("not idempotent for %q: once=%q twice=%q", id, once, twice)
		}
	}
}
