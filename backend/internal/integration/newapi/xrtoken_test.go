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
