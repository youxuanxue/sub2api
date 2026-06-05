package service

// TokenKey: the empirically-servable claude + gpt model sets, shared by the
// public /pricing catalog (isPublicCatalogModelSupported) and the per-user
// "Your Menu" unrestricted-account fallback (supportedCatalogModelIDsForPlatform).
//
// Problem: both surfaces used to advertise models that TokenKey cannot
// actually serve — the public catalog rendered the whole litellm mirror (22
// anthropic + 123 openai entries, most retired), and Your Menu fell back to
// the canonical openai.DefaultModels / claude.DefaultModels advertised lists.
// Customers picked dead models and hit 400/404/502.
//
// Rule (operator directive): keep ONLY model IDs that PASSED a live prod
// probe (returned a real 200). This is purely empirical — canonical /
// advertised status is irrelevant; a model that the upstream rejects is
// dropped even if it is in DefaultModels, and a servable model is kept even
// if it is not.
//
// Provenance (2026-06-05 live prod probes):
//   - anthropic: Claude-Code-shaped POST /v1/messages through the edge-us7
//     relay (account #54 key). Kept the IDs that returned 200; dropped
//     deprecated-gate 400s, upstream-rejected 502s, and dated snapshots whose
//     non-dated form also serves.
//   - openai: POST /v1/chat/completions (+ a full-shape /v1/responses retry
//     for the codex family) through prod with the GPT-line key. Kept the IDs
//     that returned 200; dropped chat/responses rejections (gpt-5.2,
//     gpt-5.3-codex, codex-mini-latest all 502'd on the proper shape; gpt-4*,
//     gpt-3.5*, gpt-4o*, audio/realtime/tts/transcribe rejected) and
//     gpt-image-* (502 auth / 503 no-account through the tested group — not
//     servable on a probeable path).
//
// Maintenance: this is an empirical snapshot, refreshed by re-running the
// probe (ops/observability/run-probe.sh against prod) when the served fleet
// changes. Gemini / antigravity are NOT in these sets — they were not probed,
// so Your Menu keeps using their canonical lists for now (see
// platformDefaultModelIDs); the public catalog leaves every non-claude/gpt
// vendor untouched.

// supportedAnthropicCatalogModels — claude IDs confirmed servable (2026-06-05).
var supportedAnthropicCatalogModels = map[string]struct{}{
	"claude-opus-4-8":   {},
	"claude-opus-4-7":   {},
	"claude-opus-4-6":   {},
	"claude-opus-4-5":   {},
	"claude-opus-4-1":   {},
	"claude-sonnet-4-6": {},
	"claude-sonnet-4-5": {},
	"claude-haiku-4-5":  {},
}

// supportedOpenAICatalogModels — gpt IDs confirmed servable (2026-06-05).
var supportedOpenAICatalogModels = map[string]struct{}{
	"gpt-5.5":             {},
	"gpt-5.5-pro":         {},
	"gpt-5.4":             {},
	"gpt-5.4-pro":         {},
	"gpt-5.4-mini":        {},
	"gpt-5.3-codex-spark": {},
	"gpt-5.1":             {},
	"gpt-5.1-chat-latest": {},
	"gpt-5":               {},
	"gpt-5-pro":           {},
	"gpt-5-mini":          {},
	"gpt-5-nano":          {},
	"gpt-5-chat":          {},
	"gpt-5-chat-latest":   {},
	"gpt-5-search-api":    {},
}

// isPublicCatalogModelSupported reports whether a catalog row is kept in the
// public /pricing response. Anthropic and OpenAI rows are gated by the
// empirical allowlists above; every other vendor passes through unchanged
// (the operator only asked to curate the claude + gpt families). Vendor →
// platform classification reuses inferPlatformFromVendor so azure_openai and
// vertex_ai-style provider strings map consistently with the availability
// decoration path.
func isPublicCatalogModelSupported(vendor, modelID string) bool {
	switch inferPlatformFromVendor(vendor) {
	case PlatformAnthropic:
		_, ok := supportedAnthropicCatalogModels[modelID]
		return ok
	case PlatformOpenAI:
		_, ok := supportedOpenAICatalogModels[modelID]
		return ok
	default:
		return true
	}
}

// FilterPublicCatalogToServable returns a shallow copy of resp with the
// claude + gpt rows narrowed to the empirically-servable allowlists; every
// other vendor's rows pass through unchanged.
//
// This is the PRESENTATION-layer filter for GET /api/v1/public/pricing ONLY.
// It deliberately does NOT live inside BuildPublicCatalog / buildCatalogFromBytes,
// because that shared parse also backs IsModelPriced (billing-capability +
// gateway /v1/models filtering) and the Your-Menu metadata join — those need
// the full priced set, not the curated display set. Conflating "priced" with
// "servable display" would silently drop gateway-advertised models and break
// IsModelPriced's contract.
//
// nil-safe: a nil resp returns nil; rows are filtered in place into a new
// slice so the caller's cached BuildPublicCatalog pointer is never mutated.
func FilterPublicCatalogToServable(resp *PublicCatalogResponse) *PublicCatalogResponse {
	if resp == nil {
		return nil
	}
	out := *resp // shallow copy: Object/UpdatedAt carried, Data replaced
	filtered := make([]PublicCatalogModel, 0, len(resp.Data))
	for _, m := range resp.Data {
		if isPublicCatalogModelSupported(m.Vendor, m.ModelID) {
			filtered = append(filtered, m)
		}
	}
	out.Data = filtered
	return &out
}

// supportedCatalogModelIDsForPlatform returns the empirically-servable model
// IDs for a platform, or nil for platforms without an empirical set (gemini,
// antigravity, newapi, …). Used by the Your-Menu unrestricted-account
// fallback so that surface advertises the same servable set as the public
// catalog. The returned slice is freshly built each call (callers may sort).
func supportedCatalogModelIDsForPlatform(platform string) []string {
	var src map[string]struct{}
	switch platform {
	case PlatformAnthropic:
		src = supportedAnthropicCatalogModels
	case PlatformOpenAI:
		src = supportedOpenAICatalogModels
	default:
		return nil
	}
	out := make([]string, 0, len(src))
	for id := range src {
		out = append(out, id)
	}
	return out
}
