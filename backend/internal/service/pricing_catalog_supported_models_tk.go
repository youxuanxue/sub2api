package service

// TokenKey: public-catalog support filter for the anthropic-claude and
// openai-gpt families.
//
// Problem: GET /api/v1/public/pricing renders every priced model from the
// upstream litellm mirror (resources/model-pricing/...), which carries 22
// anthropic + 123 openai entries — most long retired or never served by
// TokenKey (claude-3-*, claude-4-*-20250514, gpt-3.5-*, gpt-4*, gpt-4o-*,
// audio/realtime/tts/transcribe, …). Customers picked dead models off that
// page and hit 400/404.
//
// Fix: for the two families the operator asked to prune, keep only the model
// IDs that are CURRENTLY SERVABLE THROUGH TOKENKEY, established empirically
// (not from the litellm list). Every other vendor (gemini, …) passes through
// untouched — this filter never narrows a platform the operator did not ask
// to curate.
//
// Provenance of the allowlists below (2026-06-05 live probe through prod):
//   - anthropic: real Claude-Code-shaped POST /v1/messages through the
//     edge-us7 relay (account #54 key). 200 => kept; 400 deprecated-gate /
//     "no available accounts" (no schedulable account claims the id) => dropped.
//   - openai: POST /v1/chat/completions (and /v1/responses for codex) through
//     prod with the GPT专线 key. 200 => kept; upstream "model not supported
//     when using Codex with a ChatGPT account" / 404 => dropped.
//
// De-duplication rule applied to the empirical 200-set (operator decision):
// when both a non-dated form and its YYYYMMDD snapshot serve, keep only the
// non-dated form; drop "-thinking" pricing pseudo-entries. The result is the
// minimal set of distinct, selectable, currently-servable model IDs.
//
// Maintenance: this is an empirical snapshot, refreshed by re-running the
// probe (see ops/observability/run-probe.sh against prod) when the served
// fleet changes. It is deliberately separate from claude.DefaultModels /
// openai.DefaultModels because the operator chose to keep servable models
// beyond the canonical advertised set (e.g. gpt-5-mini, claude-opus-4-1).

// supportedAnthropicCatalogModels is the set of claude IDs kept in the public
// catalog. See file header for provenance. 2026-06-05 probe.
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

// supportedOpenAICatalogModels is the set of gpt IDs kept in the public
// catalog. See file header for provenance. 2026-06-05 probe. Image models
// (gpt-image-*) are not chat/responses-probeable; they are kept on the
// canonical openai.DefaultModels basis (recently added, actively served).
var supportedOpenAICatalogModels = map[string]struct{}{
	"gpt-5.5":             {},
	"gpt-5.5-pro":         {},
	"gpt-5.4":             {},
	"gpt-5.4-pro":         {},
	"gpt-5.4-mini":        {},
	"gpt-5.3-codex":       {},
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
	"gpt-image-1":         {},
	"gpt-image-1.5":       {},
	"gpt-image-2":         {},
}

// isPublicCatalogModelSupported reports whether a catalog row should be kept
// in the public /pricing response. Anthropic and OpenAI rows are gated by the
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
