package service

// TokenKey: public model + pricing catalog (US-028 / docs/approved/user-cold-start.md §2 v1).
//
// Scope (v1 MVP): a flat list of model_id + vendor + pricing(USD per 1k tokens) +
// context_window + max_output_tokens + capabilities[]. The richer aggregations
// (groups[], endpoints[], vendors/platforms top-level, ?group_id filter) are
// deferred to a follow-up PR per the design v1 deferred section, because they
// require an Ent schema migration (visible_in_catalog on Group).
//
// Why a separate service rather than reusing PricingService directly?
//   - PricingService.LiteLLMModelPricing intentionally drops fields like
//     max_input_tokens / supports_vision because billing only needs prices.
//     Expanding that struct to carry catalog-only metadata would couple billing
//     to an unrelated concern.
//   - Catalog has its own caching cadence (mtime-based) and its own DTO shape;
//     keeping it isolated minimizes upstream merge conflicts (rule §5).
//
// Source resolution:
//   1) cfg.Pricing.DataDir/model_pricing.json (live data refreshed by PricingService)
//   2) cfg.Pricing.FallbackFile (bundled at backend/resources/model-pricing/...)
//   3) Empty list (never 500) — see US-028 AC-005.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// PublicCatalogResponse is the top-level shape for GET /api/v1/public/pricing.
type PublicCatalogResponse struct {
	Object    string               `json:"object"`
	Data      []PublicCatalogModel `json:"data"`
	UpdatedAt time.Time            `json:"updated_at"`
}

// PublicCatalogModel is one entry in the public catalog. Field-level omitempty
// is used for context_window / max_output_tokens / capabilities so partial
// metadata still produces a clean response.
type PublicCatalogModel struct {
	ModelID         string               `json:"model_id"`
	Vendor          string               `json:"vendor,omitempty"`
	Pricing         PublicCatalogPricing `json:"pricing"`
	ContextWindow   int                  `json:"context_window,omitempty"`
	MaxOutputTokens int                  `json:"max_output_tokens,omitempty"`
	Capabilities    []string             `json:"capabilities"`
	// Availability is injected post-build by DecorateAndPruneByAvailability when
	// the PricingAvailabilityService is wired (Phase 2 / Phase 3). nil = not
	// yet available / feature flag off. Clients that pre-date this field see
	// no change (omitempty).
	Availability *PublicCatalogAvailability `json:"availability,omitempty"`
}

// PublicCatalogAvailability is the per-(platform, model) verified-availability
// sub-object injected into /pricing responses. Populated from model_availability
// table; see docs/approved/pricing-availability-source-of-truth.md.
type PublicCatalogAvailability struct {
	// Status is the canonical 4-value verdict:
	//   ok          — verified within StaleAfter AND success rate >=95%
	//   stale       — verified but >StaleAfter ago, OR success rate 80-95%
	//   unreachable — model_not_found or rate < 80%
	//   untested    — no samples yet (gray dot in UI)
	Status         string     `json:"status"`
	LastVerifiedAt *time.Time `json:"last_verified_at,omitempty"`
	LastCheckedAt  *time.Time `json:"last_checked_at,omitempty"`
	SampleCount24h int        `json:"sample_count_24h"`
	SuccessRate24h float64    `json:"success_rate_24h"`
	// LastFailureKind is the reason for the last failure (empty when ok).
	// Values match FailureKind* constants in pricing_availability_service_tk.go.
	LastFailureKind string `json:"last_failure_kind,omitempty"`
}

// PublicCatalogPricing exposes prices in USD per 1k tokens. Currency is always
// "USD" (matches users.balance unit). Per-1k is chosen over per-token to keep
// the magnitudes human-readable; precision is preserved via float64.
type PublicCatalogPricing struct {
	Currency          string  `json:"currency"`
	InputPer1KTokens  float64 `json:"input_per_1k_tokens"`
	OutputPer1KTokens float64 `json:"output_per_1k_tokens"`
	// ThinkingOutputPer1KTokens, when > 0, is the higher output price charged in
	// thinking mode for the SAME model id (Alibaba DashScope qwen3-8b/14b/32b).
	// Lets the client show "非思考 / 思考" output prices transparently. Omitted for
	// models with no thinking-mode premium. OutputPer1KTokens stays the non-thinking
	// rate; for these models enable_thinking defaults to true, so thinking is the
	// default-mode price (see computeTokenBreakdown).
	ThinkingOutputPer1KTokens float64 `json:"thinking_output_per_1k_tokens,omitempty"`
	CacheReadPer1K            float64 `json:"cache_read_per_1k,omitempty"`
	CacheWritePer1K           float64 `json:"cache_write_per_1k,omitempty"`
	// TK media units. BillingMode is "token" (default, omitted), "image"
	// (per-generated-image) or "video" (per-second). The per-image / per-second
	// field is meaningful only when BillingMode says it is a media catalog row:
	// some chat rows carry image-related price fields for multimodal inputs.
	BillingMode         string  `json:"billing_mode,omitempty"`
	OutputCostPerImage  float64 `json:"output_cost_per_image,omitempty"`
	OutputCostPerSecond float64 `json:"output_cost_per_second,omitempty"`
	// Tiers, when non-empty, is the input-token interval (阶梯) pricing for models
	// whose unit price varies by request input length (overlay `intervals` —
	// VolcEngine doubao-seed-*, DeepSeek, Qwen-plus/coder, GLM-4.7 tiered SKUs).
	// The flat Input/OutputPer1KTokens fields above carry the FIRST (lowest) tier
	// so pre-tier clients still render a sane base price; tier-aware clients (and
	// the admin CSV export) render the full ladder. Per 1k tokens, USD. Until this
	// field shipped the public /pricing endpoint silently flattened these models to
	// their first-tier price only — the ladder lived only in the compiled-in
	// tk_pricing_overlay.json. Omitted for flat-priced models.
	Tiers []PublicCatalogTier `json:"tiers,omitempty"`
}

// PublicCatalogTier is one input-token bracket of a tiered (阶梯) price. MinTokens
// is inclusive, MaxTokens exclusive; MaxTokens == nil is the open-ended top tier.
// Prices are USD per 1k tokens (overlay intervals are stored per-token → ×1000 to
// match the rest of the catalog).
type PublicCatalogTier struct {
	MinTokens         int     `json:"min_tokens"`
	MaxTokens         *int    `json:"max_tokens,omitempty"`
	InputPer1KTokens  float64 `json:"input_per_1k_tokens"`
	OutputPer1KTokens float64 `json:"output_per_1k_tokens"`
	CacheReadPer1K    float64 `json:"cache_read_per_1k,omitempty"`
}

// catalogRichEntry mirrors the litellm-shape JSON fields needed for the public
// catalog. Fields beyond what PricingService's billing flow uses (max_*,
// supports_*, deprecation_date) are kept here so we don't pollute the billing
// data structures.
type catalogRichEntry struct {
	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	ThinkingOutputCostPerToken  *float64 `json:"thinking_output_cost_per_token"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	OutputCostPerImage          *float64 `json:"output_cost_per_image"`
	OutputCostPerSecond         *float64 `json:"output_cost_per_second"`
	LiteLLMProvider             string   `json:"litellm_provider"`
	Mode                        string   `json:"mode"`
	MaxInputTokens              int      `json:"max_input_tokens"`
	MaxOutputTokens             int      `json:"max_output_tokens"`
	SupportsVision              bool     `json:"supports_vision"`
	SupportsToolChoice          bool     `json:"supports_tool_choice"`
	SupportsFunctionCalling     bool     `json:"supports_function_calling"`
	SupportsPromptCaching       bool     `json:"supports_prompt_caching"`
	SupportsResponseSchema      bool     `json:"supports_response_schema"`
	SupportsPDFInput            bool     `json:"supports_pdf_input"`
	SupportsWebSearch           bool     `json:"supports_web_search"`
}

// CatalogSource returns the raw pricing JSON bytes plus the modification time
// of the underlying file (or zero when unknown). Returning ok=false signals an
// empty/degraded source — the catalog will be an empty list (200 OK), never a
// 500, per US-028 AC-005.
type CatalogSource func() (data []byte, modTime time.Time, ok bool)

// PricingCatalogService produces the public catalog DTO and caches the result
// keyed by source mtime. Safe for concurrent use.
type PricingCatalogService struct {
	source CatalogSource

	mu       sync.RWMutex
	cached   *PublicCatalogResponse
	cachedMt time.Time
}

// NewPricingCatalogService wires the default source: live data file in
// cfg.Pricing.DataDir, falling back to the bundled fallback file. cfg may be
// nil — the source then degrades to "no data", and BuildPublicCatalog returns
// an empty list (which is the correct behavior per AC-005).
func NewPricingCatalogService(cfg *config.Config) *PricingCatalogService {
	return &PricingCatalogService{source: defaultCatalogSource(cfg)}
}

// SetSourceForTesting overrides the source provider. This is the seam tests
// use to inject fixture pricing JSON without touching the filesystem.
func (s *PricingCatalogService) SetSourceForTesting(src CatalogSource) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.source = src
	s.cached = nil
	s.cachedMt = time.Time{}
	s.mu.Unlock()
}

// InvalidateCache drops the cached catalog so the next BuildPublicCatalog
// re-parses + re-applies the overlay. The cache keys on the source file's mtime
// (model_pricing.json), so a TK pricing-overlay HOT change — which does not touch
// that file — would otherwise serve stale prices forever. The runtime overlay
// reload (pricing_service_tk_overlay_runtime.go) calls this after a swap. Nil-safe.
func (s *PricingCatalogService) InvalidateCache() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cached = nil
	s.cachedMt = time.Time{}
	s.mu.Unlock()
}

// BuildPublicCatalog returns the catalog DTO. Callers must not mutate the
// returned response — it may be shared across requests via the internal cache.
//
// Behavior:
//   - source unavailable / unreadable / empty bytes → returns empty list (never error).
//   - source mtime unchanged since last build → returns cached response.
//   - source mtime advanced or first call → re-parse, cache, return.
func (s *PricingCatalogService) BuildPublicCatalog(ctx context.Context) *PublicCatalogResponse {
	if s == nil {
		return emptyPublicCatalog(time.Now().UTC())
	}
	_ = ctx

	s.mu.RLock()
	src := s.source
	s.mu.RUnlock()

	if src == nil {
		return emptyPublicCatalog(time.Now().UTC())
	}

	data, modTime, ok := src()
	if !ok || len(data) == 0 {
		return emptyPublicCatalog(time.Now().UTC())
	}

	s.mu.RLock()
	cached := s.cached
	cachedMt := s.cachedMt
	s.mu.RUnlock()
	if cached != nil && !modTime.IsZero() && modTime.Equal(cachedMt) {
		return cached
	}

	resp := buildCatalogFromBytes(data, modTime)
	// Enrich only a healthy (non-degraded) catalog: a garbage/empty source yields
	// an empty list and must STAY empty (AC-005 degraded→empty / 200-not-500
	// contract) rather than surfacing a partial overlay-only catalog.
	if len(resp.Data) > 0 {
		applyCatalogOverlayPricing(resp)
		attachCatalogOverlayTiers(resp)
	}

	s.mu.Lock()
	s.cached = resp
	s.cachedMt = modTime
	s.mu.Unlock()

	return resp
}

func emptyPublicCatalog(updatedAt time.Time) *PublicCatalogResponse {
	return &PublicCatalogResponse{
		Object:    "list",
		Data:      []PublicCatalogModel{},
		UpdatedAt: updatedAt,
	}
}

// buildCatalogFromBytes is the pure parsing function — exported via package
// boundaries only for testing in pricing_catalog_tk_test.go. Robust to JSON
// malformations: an unparseable top-level returns empty; per-entry parse
// failures are skipped silently.
func buildCatalogFromBytes(data []byte, modTime time.Time) *PublicCatalogResponse {
	updatedAt := modTime
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	} else {
		updatedAt = updatedAt.UTC()
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return emptyPublicCatalog(updatedAt)
	}

	models := make([]PublicCatalogModel, 0, len(raw))
	for name, rawEntry := range raw {
		if name == "" || name == "sample_spec" {
			continue
		}
		var e catalogRichEntry
		if err := json.Unmarshal(rawEntry, &e); err != nil {
			continue
		}
		// Keep token-priced entries AND true media entries (per-image / per-second).
		// Media has no token price, so the original token-only guard dropped the
		// entire imagen-*/veo-*/seedream/seedance family. Chat rows may also
		// carry image-related price fields; those must not surface as empty
		// catalog rows unless they have token prices.
		if e.InputCostPerToken == nil && e.OutputCostPerToken == nil && catalogMediaBillingMode(&e) == "" {
			continue
		}
		models = append(models, catalogModelFromEntry(name, &e))
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ModelID < models[j].ModelID
	})

	return &PublicCatalogResponse{
		Object:    "list",
		Data:      models,
		UpdatedAt: updatedAt,
	}
}

// applyCatalogOverlayPricing fill-only-merges TK-overlay-priced models the file
// source lacks into the public catalog, so overlay-only models (deepseek-v4-pro,
// doubao-*, …) surface with their prices in the public catalog
// AND Your-Menu (me_pricing_catalog reads BuildPublicCatalog as metaByID).
//
// The runtime price file is a TRIMMED litellm mirror; models litellm lacks are
// priced ONLY in tk_pricing_overlay.json, which until now fed billing
// (GetModelPricing applies it) but NOT this display path — hence empty/missing
// price rows for the entire VolcEngine fifth-platform batch + deepseek-v4-pro.
//
// Fill mirrors the billing priority (model_pricing_resolver: channel DB >
// litellm mirror > TK overlay) with the same absent-or-zero semantics as the
// billing path (applyTKPricingOverlay / tkIsEffectivelyUnpriced): a name whose
// file-source row carries a real non-zero price is left untouched, while an
// all-zero placeholder row (litellm "cost unknown", e.g. deepseek-v3-2-251201)
// gets its DISPLAYED price replaced by the overlay value — otherwise the
// catalog would show $0 for a model billing actually charges. Channel pricing
// stays a strictly higher tier handled upstream (me menu Stage 1 / billing
// resolver), so the overlay only ever fills the litellm tier.
//
// Token-priced entries and true media entries merge. Per-image / per-second
// overlay rows (imagen-*/veo-*/grok-imagine-*/seedream/seedance) carry no token
// price, but they are catalog rows for Studio and must surface with their media
// billing unit.
func applyCatalogOverlayPricing(resp *PublicCatalogResponse) {
	if resp == nil {
		return
	}
	overlay := loadTKPricingOverlay()
	if len(overlay) == 0 {
		return
	}
	seen := make(map[string]int, len(resp.Data))
	for i := range resp.Data {
		seen[resp.Data[i].ModelID] = i
	}
	names := make([]string, 0, len(overlay))
	for name := range overlay {
		names = append(names, name)
	}
	sort.Strings(names)

	appended := false
	for _, name := range names {
		p := overlay[name]
		if p == nil {
			continue
		}
		if isNewAPILongTailCatalogVendor(p.LiteLLMProvider) && !isTkCuratedNewAPIModelListed(name) {
			continue
		}
		isMedia := p.OutputCostPerImage > 0 || p.OutputCostPerSecond > 0
		if p.InputCostPerToken == 0 && p.OutputCostPerToken == 0 && !isMedia {
			continue
		}
		if idx, ok := seen[name]; ok {
			// 文件源已有该行：仅当它是全零占位（litellm "cost unknown"，与计费
			// 侧 tkIsEffectivelyUnpriced 同语义）时用 overlay 价覆盖展示，保持
			// 展示=计费；行内 context window 等元数据保留文件源的值。真实非零
			// 文件价永不覆盖。
			row := &resp.Data[idx]
			if row.Pricing.InputPer1KTokens != 0 || row.Pricing.OutputPer1KTokens != 0 ||
				row.Pricing.CacheReadPer1K != 0 || row.Pricing.CacheWritePer1K != 0 {
				continue
			}
			row.Pricing.InputPer1KTokens = p.InputCostPerToken * 1000
			row.Pricing.OutputPer1KTokens = p.OutputCostPerToken * 1000
			row.Pricing.ThinkingOutputPer1KTokens = p.ThinkingOutputCostPerToken * 1000
			row.Pricing.CacheReadPer1K = p.CacheReadInputTokenCost * 1000
			row.Pricing.CacheWritePer1K = p.CacheCreationInputTokenCost * 1000
			continue
		}
		in, out := p.InputCostPerToken, p.OutputCostPerToken
		cacheRead, cacheWrite := p.CacheReadInputTokenCost, p.CacheCreationInputTokenCost
		e := catalogRichEntry{
			InputCostPerToken:           &in,
			OutputCostPerToken:          &out,
			CacheReadInputTokenCost:     &cacheRead,
			CacheCreationInputTokenCost: &cacheWrite,
			LiteLLMProvider:             p.LiteLLMProvider,
			Mode:                        p.Mode,
			SupportsPromptCaching:       p.SupportsPromptCaching,
		}
		// Thinking-mode output price (qwen3 dense): surface it so the public
		// catalog can show both 非思考/思考 output rates for the one model id.
		if p.ThinkingOutputCostPerToken > 0 {
			tout := p.ThinkingOutputCostPerToken
			e.ThinkingOutputCostPerToken = &tout
		}
		// Media overlay entries (imagen-*/veo-*/seedream/seedance) carry the
		// per-image / per-second price the trimmed litellm mirror drops — pass
		// it through so the public catalog can render the media unit.
		if p.OutputCostPerImage > 0 {
			img := p.OutputCostPerImage
			e.OutputCostPerImage = &img
		}
		if p.OutputCostPerSecond > 0 {
			sec := p.OutputCostPerSecond
			e.OutputCostPerSecond = &sec
		}
		resp.Data = append(resp.Data, catalogModelFromEntry(name, &e))
		appended = true
	}
	if appended {
		sort.Slice(resp.Data, func(i, j int) bool {
			return resp.Data[i].ModelID < resp.Data[j].ModelID
		})
	}
}

// attachCatalogOverlayTiers surfaces overlay-defined input-token interval (阶梯)
// pricing on the public catalog. Runs AFTER applyCatalogOverlayPricing so it sees
// every model (file-sourced and overlay-filled). The flat Input/OutputPer1KTokens
// fields stay the base (first) tier for pre-tier clients; this fills the full
// ladder on Pricing.Tiers for tier-aware clients and the admin CSV export. Overlay
// interval prices are per-token → ×1000 to match the catalog's per-1k unit.
// Purely additive (never mutates flat prices), idempotent, nil-safe.
func attachCatalogOverlayTiers(resp *PublicCatalogResponse) {
	if resp == nil || len(resp.Data) == 0 {
		return
	}
	overlay := loadTKPricingOverlay()
	if len(overlay) == 0 {
		return
	}
	for i := range resp.Data {
		p := overlay[resp.Data[i].ModelID]
		if p == nil || len(p.Intervals) == 0 {
			continue
		}
		tiers := make([]PublicCatalogTier, 0, len(p.Intervals))
		for j := range p.Intervals {
			iv := p.Intervals[j]
			tier := PublicCatalogTier{MinTokens: iv.MinTokens, MaxTokens: iv.MaxTokens}
			if iv.InputPrice != nil {
				tier.InputPer1KTokens = *iv.InputPrice * 1000
			}
			if iv.OutputPrice != nil {
				tier.OutputPer1KTokens = *iv.OutputPrice * 1000
			}
			if iv.CacheReadPrice != nil {
				tier.CacheReadPer1K = *iv.CacheReadPrice * 1000
			}
			tiers = append(tiers, tier)
		}
		resp.Data[i].Pricing.Tiers = tiers
	}
}

func catalogModelFromEntry(name string, e *catalogRichEntry) PublicCatalogModel {
	pricing := PublicCatalogPricing{
		Currency:                  "USD",
		InputPer1KTokens:          perTokenTo1K(e.InputCostPerToken),
		OutputPer1KTokens:         perTokenTo1K(e.OutputCostPerToken),
		ThinkingOutputPer1KTokens: perTokenTo1K(e.ThinkingOutputCostPerToken),
		CacheReadPer1K:            perTokenTo1K(e.CacheReadInputTokenCost),
		CacheWritePer1K:           perTokenTo1K(e.CacheCreationInputTokenCost),
	}
	// Media billing mode is catalog membership truth for Studio. Trust explicit
	// media modes, and keep a conservative fallback only for pure media rows
	// whose mirrors forgot `mode`. Do not infer media from a per-image field on
	// token-priced chat rows (Gemini chat rows can carry image-related costs).
	switch catalogMediaBillingMode(e) {
	case "video":
		pricing.BillingMode = "video"
		pricing.OutputCostPerSecond = *e.OutputCostPerSecond
	case "image":
		pricing.BillingMode = "image"
		pricing.OutputCostPerImage = *e.OutputCostPerImage
	}
	return PublicCatalogModel{
		ModelID:         name,
		Vendor:          e.LiteLLMProvider,
		Pricing:         pricing,
		ContextWindow:   e.MaxInputTokens,
		MaxOutputTokens: e.MaxOutputTokens,
		Capabilities:    catalogCapabilities(e),
	}
}

func catalogMediaBillingMode(e *catalogRichEntry) string {
	if e == nil {
		return ""
	}
	hasTokenPrice := e.InputCostPerToken != nil || e.OutputCostPerToken != nil
	pureMediaWithoutMode := e.Mode == "" && !hasTokenPrice
	switch {
	case e.OutputCostPerSecond != nil && *e.OutputCostPerSecond > 0 &&
		(e.Mode == "video_generation" || pureMediaWithoutMode):
		return "video"
	case e.OutputCostPerImage != nil && *e.OutputCostPerImage > 0 &&
		(e.Mode == "image_generation" || pureMediaWithoutMode):
		return "image"
	default:
		return ""
	}
}

func perTokenTo1K(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v * 1000
}

// catalogCapabilities maps the litellm supports_* booleans to short, stable
// capability tags consumable by external tools (e.g. All API Hub).
// The slice is always non-nil to keep JSON serialization stable as `[]`.
func catalogCapabilities(e *catalogRichEntry) []string {
	caps := make([]string, 0, 7)
	if e.SupportsVision {
		caps = append(caps, "vision")
	}
	if e.SupportsToolChoice || e.SupportsFunctionCalling {
		caps = append(caps, "tool_use")
	}
	if e.SupportsPromptCaching {
		caps = append(caps, "prompt_caching")
	}
	if e.SupportsResponseSchema {
		caps = append(caps, "response_schema")
	}
	if e.SupportsPDFInput {
		caps = append(caps, "pdf_input")
	}
	if e.SupportsWebSearch {
		caps = append(caps, "web_search")
	}
	return caps
}

// defaultCatalogSource returns a CatalogSource that resolves the live data
// file first, then the bundled fallback. cfg may be nil during unusual
// bootstrap; in that case the source returns ok=false (empty catalog).
func defaultCatalogSource(cfg *config.Config) CatalogSource {
	return func() ([]byte, time.Time, bool) {
		if cfg == nil {
			return nil, time.Time{}, false
		}
		candidates := make([]string, 0, 2)
		if cfg.Pricing.DataDir != "" {
			candidates = append(candidates, filepath.Join(cfg.Pricing.DataDir, "model_pricing.json"))
		}
		if cfg.Pricing.FallbackFile != "" {
			candidates = append(candidates, cfg.Pricing.FallbackFile)
		}
		for _, p := range candidates {
			body, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			var modTime time.Time
			if info, statErr := os.Stat(p); statErr == nil {
				modTime = info.ModTime()
			}
			return body, modTime, true
		}
		return nil, time.Time{}, false
	}
}
