package service

import (
	"context"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// TokenKey native catalog/menu empirical projections. These sets own display
// membership only; they do not own pricing, request legality, account mapping or
// runtime readiness. See pricing-serving-single-source-of-truth.md for the
// delivery composition and pricing-availability-source-of-truth.md for evidence
// and structurally-gone semantics.
//
// supportedCatalogModelIDsForPlatform consumes the platform-local sets. The
// public Claude surface additionally projects Kiro IDs through
// supportedClaudeCatalogModels because the public catalog has no Kiro vendor.
//
// ops/pricing/refresh-servable-allowlist.py rewrites only the marker-delimited
// anthropic/openai/gemini blocks. Keep those anchors intact. Antigravity and Grok
// remain reviewed projections because their specialized probes are not automatic
// splice inputs. Point-in-time account, fleet and probe evidence belongs in the
// evidence ledger or Git history, not beside these load-bearing sets.

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

// supportedOpenAIAinzyRelayCatalogModels is the compiled mapping floor for the
// api.ainzy.net/v1 relay scope. It is separate from native OpenAI membership.
var supportedOpenAIAinzyRelayCatalogModels = map[string]struct{}{
	"gpt-5.4":      {},
	"gpt-5.4-mini": {},
	"gpt-5.5":      {},
}

// supportedOpenAITokenseaRelayCatalogModels is the upstream-listed universe for
// the agent.tokensea.ai OpenAI relay scope. The compiled account floor applies
// the relay's public CatalogPolicy predicate; membership here alone does not add
// public catalog/menu rows.
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

// supportedAnthropicTokenseaRelayCatalogModels contains the Claude short-name
// aliases for the Anthropic-shaped tokensea relay scope. Wire mappings stay in
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

// supportedGeminiCatalogModels is the reviewed Gemini/Vertex catalog projection.
// An empty set preserves the existing passthrough/canonical fallback.
var supportedGeminiCatalogModels = map[string]struct{}{
	// servable-allowlist:begin gemini
	"gemini-2.5-flash":              {},
	"gemini-2.5-flash-lite":         {},
	"gemini-2.5-pro":                {},
	"gemini-3.5-flash-lite":         {},
	"gemini-3.6-flash":              {},
	"gemini-3.7-flash":              {},
	"gemini-3.8-flash":              {},
	"imagen-4.0-fast-generate-001":  {},
	"imagen-4.0-generate-001":       {},
	"imagen-4.0-ultra-generate-001": {},
	"veo-3.1-generate-001":          {},
	"gemini-embedding-001":          {},
	// servable-allowlist:end gemini
}

// supportedAntigravityCatalogModels is the reviewed Antigravity client/wire
// projection. It is maintained from the specialized Antigravity evidence path;
// an empty set preserves the existing passthrough/canonical fallback.
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

// supportedGrokCatalogModels is the reviewed native Grok catalog/menu
// projection. Grok accounts may have empty model_mapping, so this set supplies
// the platform-local menu owner. It is not rewritten by the automatic refresh;
// public membership still requires CatalogPolicy and structurally-gone review.
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
