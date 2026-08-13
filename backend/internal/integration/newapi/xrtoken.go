package newapi

import (
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// XRTokenBaseURL is the ARK-compatible XRToken API root (mainland China).
//
// XRToken (https://xrtoken.net) resells VolcEngine Ark video models behind an
// ARK-compatible surface: per its /docs/zh/ark-compatibility page the request
// and response bodies are byte-identical to official Ark, and ONLY the base URL
// and auth header differ. The one wire difference that matters to us is the
// path middle segment — Ark serves
//
//	{base}/api/v3/contents/generations/tasks
//
// while XRToken serves
//
//	{base}/v1/contents/generations/tasks
//
// Because new-api's doubao TaskAdaptor hardcodes the `/api/v3/` segment (see
// relay/channel/task/doubao/adaptor.go BuildRequestURL + FetchTask), no
// base_url value can reach XRToken's shape — the difference is in the middle,
// not the prefix. The bridge therefore wraps the upstream adaptor and overrides
// exactly those two methods; see internal/relay/bridge/video_relay_tk_xrtoken.go.
//
// This predicate is the single source of truth for "is this account XRToken?",
// following the same channel_type + sentinel base_url pattern already used by
// IsVolcEngineAgentPlanBaseURL (ch45 + Agent Plan base) and IsQianfanBaseURL
// (ch46 + Qianfan base). Adding a variant here does NOT require a new
// channel_type: ch54 (DoubaoVideo) is a video-only channel — it is absent from
// new-api's ChannelType2APIType, so there is no chat adaptor to wrap, only the
// task adaptor registered via relay.GetTaskAdaptor("54").
const XRTokenBaseURL = "https://api.xrtoken.net"

// IsXRTokenBaseURL reports whether channelType/base resolve to the XRToken
// ARK-compatible endpoint.
//
// Scoped to ChannelTypeDoubaoVideo (54) on purpose: that is the video-only Ark
// task channel TokenKey provisions XRToken accounts on. A ch45 account pointed
// at the same host would additionally expose chat/embeddings/images paths that
// XRToken does NOT serve in Ark shape (verified: /api/v3/chat/completions and
// /api/v3/images/generations both 404 there), so we deliberately do not treat
// ch45 + this host as XRToken.
//
// Accepts the bare root as well as a trailing `/v1`, because that is the base
// XRToken's own SDK docs tell users to configure ("https://api.xrtoken.net/v1")
// and admins paste it verbatim. Keeping it acceptable here — rather than only
// in the UI — means the resolved base never grows a doubled `/v1/v1/...` path.
func IsXRTokenBaseURL(channelType int, base string) bool {
	if channelType != newapiconstant.ChannelTypeDoubaoVideo {
		return false
	}
	return NormalizeXRTokenBaseURL(base) == XRTokenBaseURL
}

// NormalizeXRTokenBaseURL collapses the accepted XRToken base spellings to the
// canonical root (no trailing slash, no trailing `/v1`). Returns the trimmed
// input unchanged when it is not an XRToken host, so callers can use it as a
// pass-through normalizer.
//
// The `/v1` strip is what keeps BuildRequestURL honest: the wrapper appends
// `/v1/contents/generations/tasks`, so a stored base of
// "https://api.xrtoken.net/v1" must not yield "/v1/v1/contents/...".
// NormalizeArkChannelBaseURL only strips `/api/v3*` and plan-style suffixes, so
// it cannot do this for us.
func NormalizeXRTokenBaseURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return base
	}
	trimmed := strings.TrimSuffix(strings.ToLower(base), "/v1")
	trimmed = strings.TrimRight(trimmed, "/")
	if trimmed == XRTokenBaseURL {
		return XRTokenBaseURL
	}
	return base
}
