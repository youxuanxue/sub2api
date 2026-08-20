package service

import (
	"context"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// TokenKey: the empirically-servable native-platform model sets. Native account
// mapping and unrestricted-account menu fallbacks consume these platform-local
// sets through supportedCatalogModelIDsForPlatform. The public Claude catalog
// additionally projects Kiro-served IDs onto its anthropic vendor surface via
// supportedClaudeCatalogModels because Kiro mirror stubs live in Claude groups.
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
// Provenance:
//   - anthropic: Claude-Code-shaped POST /v1/messages through the edge-us7
//     relay (account #54 key). Kept the IDs that returned 200; dropped
//     deprecated-gate 400s, upstream-rejected 502s, and dated snapshots whose
//     non-dated form also serves.
//   - openai: 2026-07-10 SSOT audit probes (prod OAuth accounts + account 76
//     Ainzy relay checks). Native ChatGPT-OAuth servable set is the curated
//     floor in ops/pricing/examples/openai-oauth-proven.json plus the
//     GPT-pro1-verified GPT-5.6 family. Compatibility GPT-5 spellings such as
//     gpt-5.4-high, codex-mini-latest, and gpt-5-chat-latest remain priced for
//     billing but hidden from /pricing and /models; clients may still request
//     them and are routed through CanonicalizeOpenAICompatRoutingModel to a
//     served floor id. Retired ids such as gpt-5.2 take the deprecated-model 400
//     path instead; non-display aliases such as gpt-5.3-codex route to their
//     canonical supported target. codex-auto-review is empirically servable on
//     prod ChatGPT OAuth accounts (2026-07-28 upstream probe) and is allowlisted.
//
//   - gemini/Vertex (2026-06-09 live probe of the us6 google group, account 3
//     catch-all, hit the app internally to bypass the edge Caddy): kept the IDs
//     that returned 200 — gemini-2.5-flash/-flash-lite/-pro and imagen-4.0
//     fast/generate/ultra. Dropped as not-currently-
//     servable through that Vertex project/region: gemini-2.0-* and gemini-3.x
//     chat (uniform 502 while 2.5 served — a project/region availability
//     signal, not transient), and the gemini-*-image generateContent models (500
//     via /v1/images/generations — those ride the chat endpoint, not the images
//     predict surface; re-probe via chat to add them). veo-3.1-generate-001 was
//     added after the 2026-07-04 post-#1198 paid gate returned direct/universal
//     200 queued video for the Vertex group.
//   - gemini-embedding-001 (2026-08-07 direct upstream probe on prod Vertex
//     accounts 47/57/58/59/74): :predict returned 200 with embedding values.
//
// Maintenance: this is an empirical snapshot, refreshed by re-running the
// probe (ops/observability/run-probe.sh) when the served fleet changes. An
// empty gemini set is still treated as passthrough (the code guards len==0),
// so clearing it reverts to canonical.
//
//   - antigravity (2026-06-13 empirical probe: /v1internal:fetchAvailableModels
//     catalog + per-model generateContent replay of edge-us3 account 3; refreshed
//     2026-06-23 via prod /antigravity/v1beta/...:generateContent): kept the
//     gemini wire ids that returned a real 200. PR #1265 / 2026-07-07 then
//     confirmed the live Antigravity Claude subset is exactly
//     claude-sonnet-4-6 and claude-opus-4-6-thinking (with bare
//     claude-opus-4-6 mapped to the thinking wire id). gpt-oss remains off
//     antigravity. gemini-2.5-pro remained inconclusive on 2026-06-23
//     generateContent + streamGenerateContent retry (000 timeout), so it stays
//     out until a real 200 — re-confirmed inconclusive on the 2026-06-27 prod
//     ANTIGRAVITY_CHAT_MODELS probe (000 timeout again), so it stays out.
//   - antigravity 2026-06-27 (prod ANTIGRAVITY_CHAT_MODELS probe via
//     ops/pricing/probe-servable-models.sh): gemini-3.5-flash returned a real prod
//     200 (was previously not servable) → added. It prices via the bundled litellm
//     mirror (gemini-3.5-flash present: in $1.5/M, out $9/M), so no overlay entry
//     and no served_at_fallback.
//   - 2026-06-27 image models: the gemini-*-image set (gemini-2.5-flash-image,
//     gemini-3-pro-image, gemini-3.1-flash-image, gemini-3.1-flash-image-preview)
//     probed a real 200 through the ANTIGRAVITY account pool and bills per-image via
//     CalculateImageCost. Per the operator rule "a model that tests servable through a
//     group's accounts joins that group's servable list", they are listed under
//     supportedAntigravityCatalogModels (the group that actually serves them). They
//     are NOT added to supportedGeminiCatalogModels because the gemini/Vertex accounts
//     (constrained 7-key mapping) do not serve them.

// The anthropic/openai/gemini maps below are regenerated by
// ops/pricing/refresh-servable-allowlist.py from a live probe. The
// `servable-allowlist:begin/end <platform>` markers are the splice anchors the
// generator rewrites between — keep them intact, and hand-edits inside those
// three blocks will be overwritten on the next refresh. Last claude probe:
// 2026-06-05. Last native openai audit replay: 2026-07-10. The antigravity block is
// hand-maintained from the empirical probe above: the refresh tool's platforms
// tuple is anthropic/openai/gemini and its
// GEMINI_EXCLUDE_RE drops antigravity from the google catch-all, so it never
// rewrites the antigravity anchors. Adding an antigravity probe family to that
// tool is a follow-up.

// supportedAnthropicCatalogModels — claude IDs confirmed servable.
var supportedAnthropicCatalogModels = map[string]struct{}{
	// servable-allowlist:begin anthropic
	"claude-fable-5":    {},
	"claude-haiku-4-5":  {},
	"claude-opus-4-1":   {},
	"claude-opus-4-5":   {},
	"claude-opus-4-6":   {},
	"claude-opus-4-7":   {},
	"claude-opus-4-8":   {},
	"claude-sonnet-4-5": {},
	"claude-sonnet-4-6": {},
	"claude-sonnet-5":   {},
	// servable-allowlist:end anthropic
}

// supportedClaudeCatalogModels is the public Claude surface: native Anthropic
// models plus models served by Kiro edge accounts through anthropic kiro-us*
// mirror stubs in prod Claude groups. It is derived from the two serving owners;
// vendor remains anthropic because the public catalog has no Kiro vendor split.
var supportedClaudeCatalogModels = func() map[string]struct{} {
	out := make(map[string]struct{}, len(supportedAnthropicCatalogModels)+len(supportedKiroCatalogModels))
	for id := range supportedAnthropicCatalogModels {
		out[id] = struct{}{}
	}
	for id := range supportedKiroCatalogModels {
		out[id] = struct{}{}
	}
	return out
}()

// supportedOpenAICatalogModels — gpt IDs confirmed servable.
var supportedOpenAICatalogModels = map[string]struct{}{
	// servable-allowlist:begin openai
	"codex-auto-review":   {},
	"gpt-5.3-codex-spark": {},
	"gpt-5.4":             {},
	"gpt-5.4-mini":        {},
	"gpt-5.5":             {},
	"gpt-5.6":             {},
	"gpt-5.6-luna":        {},
	"gpt-5.6-sol":         {},
	"gpt-5.6-terra":       {},
	// servable-allowlist:end openai
}

// supportedOpenAIAinzyRelayCatalogModels — gpt IDs kept in the compiled
// model_mapping floor for prod account 76 (api.ainzy.net/v1). Mirrors the
// operator-applied runtime mapping so deploys do not re-narrow the account
// back to a stale floor. gpt-5.3-codex-spark excluded: 2026-07-28 prod probe
// showed api.ainzy.net returns 404 for spark; OAuth/edge accounts serve it.
var supportedOpenAIAinzyRelayCatalogModels = map[string]struct{}{
	"gpt-5.4":      {},
	"gpt-5.4-mini": {},
	"gpt-5.5":      {},
}

// supportedOpenAITokenseaRelayCatalogModels — upstream GET /v1/models owner for
// prod account 92 (agent.tokensea.ai OpenAI relay) on 2026-08-20. The compiled
// account floor is this set intersected with public priced+displayable+servable
// SSOT; listing here does not add public catalog/menu rows.
var supportedOpenAITokenseaRelayCatalogModels = map[string]struct{}{
	"byteplus/dreamina-seedance-2-0-260128":      {},
	"byteplus/dreamina-seedance-2-0-fast-260128": {},
	"byteplus/dreamina-seedance-2-0-mini-260615": {},
	"claude-fable-5":                 {},
	"claude-haiku-4-5-20251001":      {},
	"claude-opus-4-5-20251101":       {},
	"claude-opus-4-6":                {},
	"claude-opus-4-7":                {},
	"claude-opus-4-8":                {},
	"claude-opus-5":                  {},
	"claude-sonnet-4-6":              {},
	"claude-sonnet-5":                {},
	"deepseek-v3.2":                  {},
	"deepseek-v4-flash":              {},
	"deepseek-v4-pro":                {},
	"doubao-seedance-2-0-250428":     {},
	"doubao-seedance-2.0":            {},
	"gemini-3-pro-image":             {},
	"gemini-3-pro-image-preview":     {},
	"gemini-3.1-flash-image":         {},
	"gemini-3.1-flash-image-preview": {},
	"gemini-omni-flash-preview":      {},
	"generate_video_seedance_v2_0":   {},
	"glm-5":                          {},
	"glm-5.1":                        {},
	"glm-5.2":                        {},
	"gpt-4o-2024-05-13":              {},
	"gpt-5.4":                        {},
	"gpt-5.4-mini":                   {},
	"gpt-5.5":                        {},
	"gpt-5.6-luna":                   {},
	"gpt-5.6-sol":                    {},
	"gpt-5.6-terra":                  {},
	"gpt-image-2":                    {},
	"kimi-k2.5":                      {},
	"kimi-k2.6":                      {},
	"kimi-k2.7-code":                 {},
	"kimi-k3":                        {},
	"kimi/kimi-k3":                   {},
	"minimax-m2.7":                   {},
	"qwen3.6-plus":                   {},
	"qwen3.7-max":                    {},
	"qwen3.7-plus":                   {},
	"us.anthropic.claude-opus-4-7":   {},
	"us.anthropic.claude-opus-5":     {},
	"us.anthropic.claude-sonnet-4-6": {},
	"us.anthropic.claude-sonnet-5":   {},
}

// supportedAnthropicTokenseaRelayCatalogModels — Claude short-name aliases for
// prod account 93. The live floor is tokenseaRelayCorePublicFloorIDs (same
// SSOT set as account 92) plus these shorts; wire IDs stay in
// anthropicTokenseaRelayModelMappingFloor.
var supportedAnthropicTokenseaRelayCatalogModels = map[string]struct{}{
	"claude-fable-5":            {},
	"claude-haiku-4-5":          {},
	"claude-haiku-4-5-20251001": {},
	"claude-opus-4-5":           {},
	"claude-opus-4-5-20251101":  {},
	"claude-opus-4-6":           {},
	"claude-opus-4-7":           {},
	"claude-opus-4-8":           {},
	"claude-opus-5":             {},
	"claude-sonnet-4-6":         {},
	"claude-sonnet-5":           {},
}

// CloudWise relay model families: openai_cloudwise_relay_tk.go (openAICloudwiseRelayAllowedModelPrefixes).

// supportedGeminiCatalogModels — gemini/Vertex IDs confirmed servable through
// the prod Vertex pool. Gemini 3.5 Flash Lite and 3.6 Flash were confirmed on
// accounts 47/57/58/59/74 via direct global generateContent on 2026-07-22.
// Gemini 3.7 Flash was confirmed on accounts 47/57/59 via the same global
// generateContent path on 2026-08-19 (us-central1 returns 404).
// While EMPTY the catalog/menu gates fall through to passthrough/canonical (no
// regression).
var supportedGeminiCatalogModels = map[string]struct{}{
	// servable-allowlist:begin gemini
	"gemini-2.5-flash":              {},
	"gemini-2.5-flash-lite":         {},
	"gemini-2.5-pro":                {},
	"gemini-3.5-flash-lite":         {},
	"gemini-3.6-flash":              {},
	"gemini-3.7-flash":              {},
	"imagen-4.0-fast-generate-001":  {},
	"imagen-4.0-generate-001":       {},
	"imagen-4.0-ultra-generate-001": {},
	"veo-3.1-generate-001":          {},
	"gemini-embedding-001":          {},
	// servable-allowlist:end gemini
}

// supportedAntigravityCatalogModels — antigravity wire/client ids confirmed
// servable (Gemini 2026-06 probes + PR #1265 Antigravity Claude live subset).
// Gemini 3.6 Flash was confirmed on us3 account 3 and us4 account 5 through the
// gemini-3.6-flash-tiered wire id on 2026-07-22. Gemini 3.7 Flash was listed by
// fetchAvailableModels on us3/us4/us6 on 2026-08-19 as
// gemini-3.7-flash-{low,medium,high} (no -tiered wire id). Gemini 3.5 Flash Lite remained
// upstream-unsupported and is intentionally Vertex-only. Hand-maintained (see
// header). While EMPTY the catalog/menu gates fall through to
// passthrough/canonical (no regression).
var supportedAntigravityCatalogModels = map[string]struct{}{
	// servable-allowlist:begin antigravity
	"claude-opus-4-6":                {},
	"claude-opus-4-6-thinking":       {},
	"claude-sonnet-4-6":              {},
	"gemini-2.5-flash":               {},
	"gemini-2.5-flash-image":         {},
	"gemini-2.5-flash-lite":          {},
	"gemini-2.5-flash-thinking":      {},
	"gemini-3-flash":                 {},
	"gemini-3-flash-agent":           {},
	"gemini-3-pro-image":             {},
	"gemini-3.1-flash-image":         {},
	"gemini-3.1-flash-image-preview": {},
	"gemini-3.1-pro-low":             {},
	"gemini-3.5-flash":               {},
	"gemini-3.5-flash-extra-low":     {},
	"gemini-3.5-flash-low":           {},
	"gemini-3.6-flash":               {},
	"gemini-3.7-flash":               {},
	"gemini-pro-agent":               {},
	// servable-allowlist:end antigravity
}

// supportedGrokCatalogModels — xAI / Grok (seventh platform) wire ids that
// TokenKey serves AND prices. Grok is a native OAuth-relay platform: its
// accounts are unrestricted (empty credentials.model_mapping) and carry no
// channel, so before this set both the channel stage and the whitelist stage
// of the per-user menu produced nothing and a grok group showed an EMPTY
// "分组目录" (incident 2026-06-20). The set is the SAME grok IDs the public
// /pricing catalog surfaces: grok chat ids whose official xAI price is in
// tk_pricing_overlay.json and whose native-grok live probe returned 200. Grok
// 4.6 was added after the 2026-08-13 direct xAI upstream probe on edge-us4
// account 6 returned 200; grok-latest stayed mapped to upstream grok-4.3 in the
// same probe window, and the bare grok / grok-4.6-latest aliases returned
// model-not-found upstream, so only the official grok-4.6 id is displayed. The
// grok-imagine paid media ids joined this set after the 2026-07-04 post-#1198
// paid gate returned direct/universal 200 for image and video. Note that
// `/v1/videos/generations` is the xAI-native alias and returns request_id
// rather than the TokenKey OpenAI-video task shape.
//
// Hand-maintained like the antigravity arm (the refresh tool's probe tuple is
// anthropic/openai/gemini and does not cover grok yet). Display policy follows
// the repo SSOT rule: upstream-official ids/aliases on the provider model page
// that are priced and probe-servable are public-listed. Legacy retirement
// redirects (grok-4-fast-reasoning) stay priced-only.
var supportedGrokCatalogModels = map[string]struct{}{
	// servable-allowlist:begin grok
	"grok-4.20-0309-non-reasoning": {},
	"grok-4.20-0309-reasoning":     {},
	"grok-4.3":                     {},
	"grok-4.3-latest":              {},
	"grok-4.5":                     {},
	"grok-4.5-latest":              {},
	"grok-4.6":                     {},
	"grok-build-0.1":               {},
	"grok-build-latest":            {},
	"grok-code-fast":               {},
	"grok-code-fast-1":             {},
	"grok-code-fast-1-0825":        {},
	"grok-imagine-image":           {},
	"grok-imagine-image-quality":   {},
	"grok-imagine-video":           {},
	"grok-latest":                  {},
	// servable-allowlist:end grok
}

// isPublicCatalogModelSupported reports whether a catalog row is kept in the
// public /pricing response. Native platform rows are gated by the empirical
// allowlists above; curated newapi long-tail rows use tk_served_models.json
// display=true; unknown vendors are hidden until a universal platform mapping
// exists. Vendor → platform classification reuses inferPlatformFromVendor so
// azure_openai and vertex_ai-style provider strings map consistently with the
// availability decoration path.
func isPublicCatalogModelSupported(vendor, modelID string) bool {
	// Fifth-platform newapi long-tail: only manifest-listed models may appear on
	// /pricing when their manifest display bit is true. Unlisted newapi long-tail
	// residue is excluded from BuildPublicCatalog overlay fill and from
	// IsModelPriced membership; hidden-but-listed rows may remain priced and
	// explicitly servable, but are not advertised.
	if isNewAPILongTailCatalogVendor(vendor) {
		return isTkCuratedNewAPICatalogRowDisplayed(vendor, modelID)
	}
	switch inferPlatformFromVendor(vendor) {
	case PlatformAnthropic:
		_, ok := supportedClaudeCatalogModels[modelID]
		return ok
	case PlatformOpenAI:
		_, ok := supportedOpenAICatalogModels[modelID]
		return ok
	case PlatformGemini:
		// Empty set => not yet probed => passthrough (no regression). Once the
		// refresh tool populates it, the gate activates like claude/gpt.
		if len(supportedGeminiCatalogModels) == 0 {
			return true
		}
		_, ok := supportedGeminiCatalogModels[modelID]
		return ok
	case PlatformAntigravity:
		// Empty set => not yet probed => passthrough (no regression). Populated
		// here from empirical probes (Gemini + PR #1265 live Claude subset;
		// gpt-oss remains off antigravity).
		if len(supportedAntigravityCatalogModels) == 0 {
			return true
		}
		_, ok := supportedAntigravityCatalogModels[modelID]
		return ok
	case PlatformGrok:
		// Reached only because inferPlatformFromVendor maps the "xai" vendor to
		// grok. Empty set => passthrough (no regression). Populated with the
		// priced grok overlay set so public /pricing and the per-user menu gate
		// on the same source.
		if len(supportedGrokCatalogModels) == 0 {
			return true
		}
		_, ok := supportedGrokCatalogModels[modelID]
		return ok
	default:
		return false
	}
}

// FilterPublicCatalogToServable returns a shallow copy of resp with the
// native rows narrowed to their empirically-servable allowlists and the newapi
// long-tail rows narrowed to display=true in tk_served_models.json. Unknown
// vendors are hidden until a universal platform mapping exists.
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
		// m is a by-value copy of the row — re-tagging its vendor here is
		// presentation-only and never mutates the caller's cached BuildPublicCatalog.
		m.Vendor = presentationVendorForServable(m.ModelID, m.Vendor)
		if isPublicCatalogModelSupported(m.Vendor, m.ModelID) {
			filtered = append(filtered, m)
		}
	}
	out.Data = filtered
	return &out
}

// presentationVendorForServable re-tags antigravity-served wire ids that the
// upstream price mirror carries under a gemini/vertex vendor. The mirror routes
// names like gemini-3.5-flash / gemini-3-pro-image / gemini-*-image to the
// PlatformGemini gate, whose allowlist (the constrained Vertex 7-key set) lacks
// them — so the public catalog silently drops them even though antigravity serves
// them and the overlay/source prices them (#1029/#1030 follow-up: same class as
// the gpt-5.6 display gap, on a different surface). A model that is in the
// antigravity allowlist but NOT the gemini allowlist is antigravity-EXCLUSIVE:
// re-attribute it to the antigravity vendor so it passes the antigravity gate and
// displays under the correct (antigravity-served) vendor. Dual-listed ids (e.g.
// gemini-2.5-flash, in BOTH sets) are genuinely Vertex-served too and keep the
// gemini vendor. Presentation-only by construction (the caller copies rows by
// value), so IsModelPriced / the Your-Menu metadata join are untouched.
// ("public catalog" = the catalog behind GET /api/v1/public/pricing; the UI labels
// that view 「所有分组 / All groups」 since #1037 — symbol names stay publicCatalog.)
func presentationVendorForServable(modelID, vendor string) string {
	if _, ag := supportedAntigravityCatalogModels[modelID]; !ag {
		return vendor
	}
	if _, gem := supportedGeminiCatalogModels[modelID]; gem {
		return vendor // dual-listed: genuinely Vertex-served, keep gemini vendor
	}
	if inferPlatformFromVendor(vendor) == PlatformGemini {
		return PlatformAntigravity
	}
	return vendor
}

// supportedCatalogModelIDsForPlatform returns the empirically-servable model
// IDs for a platform, or nil for platforms whose empirical set is empty
// (unprobed) or absent (newapi, …). Used by the Your-Menu unrestricted-account
// fallback so that surface advertises the same servable set as the public
// catalog. The returned slice is freshly built each call (callers may sort).
//
// anthropic/openai/gemini/antigravity/kiro/grok have curated sets. Antigravity
// account-mapped menus still use credentials.model_mapping, but the gateway
// /antigravity/models and admin selector both consume the curated set through
// tkServableCandidateIDs/servableIDs, so a probed-but-unpriced id cannot leak
// into client-visible defaults.
func supportedCatalogModelIDsForPlatform(platform string) []string {
	var src map[string]struct{}
	switch platform {
	case PlatformAnthropic:
		src = supportedAnthropicCatalogModels
	case PlatformOpenAI:
		src = supportedOpenAICatalogModels
	case PlatformGemini:
		// Empty (unprobed) => nil so the caller keeps the canonical fallback;
		// a populated set advertises only the empirically-servable gemini IDs.
		if len(supportedGeminiCatalogModels) == 0 {
			return nil
		}
		src = supportedGeminiCatalogModels
	case PlatformAntigravity:
		if len(supportedAntigravityCatalogModels) == 0 {
			return nil
		}
		src = supportedAntigravityCatalogModels
	case PlatformGrok:
		// Grok is a native OAuth platform with no canonical DefaultModels list;
		// its served set IS its priced overlay set. Empty => nil so the caller
		// keeps its no-canonical fallback.
		if len(supportedGrokCatalogModels) == 0 {
			return nil
		}
		src = supportedGrokCatalogModels
	case PlatformKiro:
		src = supportedKiroCatalogModels
	default:
		return nil
	}
	out := make([]string, 0, len(src))
	for id := range src {
		out = append(out, id)
	}
	return out
}

// VertexNewAPIChannelServableModelIDs returns TokenKey's empirically verified
// Gemini/Vertex wire IDs for newapi channel_type 41 (Vertex SA bridge). Admin
// UIs use this as the preset model_mapping list — delegated to
// AccountModelMappingPresetIDs (single SSOT).
func VertexNewAPIChannelServableModelIDs() []string {
	return AccountModelMappingPresetIDs(context.Background(), PlatformNewAPI, newapiconstant.ChannelTypeVertexAi, nil)
}
