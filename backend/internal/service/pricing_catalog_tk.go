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
//   - The file-source parser supplies compatibility metadata, while the complete
//     registry snapshot always owns displayed price dimensions. This keeps
//     billing and display on the same atomic price decision.
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
	"strings"
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
	// TK media/embedding units. BillingMode is "token" (default, omitted), "embedding"
	// (vector / embeddings endpoint), "image" (per-generated-image) or "video" (per-second). The per-image / per-second
	// field is meaningful only when BillingMode says it is a media catalog row:
	// some chat rows carry image-related price fields for multimodal inputs.
	BillingMode             string  `json:"billing_mode,omitempty"`
	OutputCostPerImage      float64 `json:"output_cost_per_image,omitempty"`
	InputCostPerImageToken  float64 `json:"input_cost_per_image_token,omitempty"`
	OutputCostPerImageToken float64 `json:"output_cost_per_image_token,omitempty"`
	ImagePrice1K            float64 `json:"image_price_1k,omitempty"`
	ImagePrice2K            float64 `json:"image_price_2k,omitempty"`
	ImagePrice4K            float64 `json:"image_price_4k,omitempty"`
	OutputCostPerSecond     float64 `json:"output_cost_per_second,omitempty"`
	// VideoPriceTiers surfaces official resolution×audio (and Grok image-input) ladders.
	// OutputCostPerSecond carries the minimum tier for legacy clients; tier-aware UIs
	// should render the full ladder. Omitted for flat-priced legacy rows.
	VideoPriceTiers []PublicCatalogVideoTier `json:"video_price_tiers,omitempty"`
	// Tiers, when non-empty, is the input-token interval (阶梯) pricing for models
	// whose unit price varies by request input length (registry `intervals` —
	// VolcEngine doubao-seed-*, DeepSeek, Qwen-plus/coder, GLM-4.7 tiered SKUs).
	// The flat Input/OutputPer1KTokens fields above carry the FIRST (lowest) tier
	// so pre-tier clients still render a sane base price; tier-aware clients (and
	// the admin CSV export) render the full ladder. Per 1k tokens, USD. Until this
	// field shipped the public /pricing endpoint silently flattened these models to
	// their first-tier price only. Omitted for flat-priced models.
	Tiers []PublicCatalogTier `json:"tiers,omitempty"`
	// PeakValley, when present, documents provider time-of-day peak pricing on top
	// of the flat fields above (currently DeepSeek direct API). Billing applies
	// PeakMultiplier during the listed windows in Timezone; flat prices are
	// off-peak (谷时).
	PeakValley *PublicCatalogPeakValley `json:"peak_valley,omitempty"`
}

// PublicCatalogPeakValley surfaces off-peak vs peak list prices for models with
// upstream time-of-day multipliers. Peak* fields are flat × PeakMultiplier.
type PublicCatalogPeakValley struct {
	Timezone          string   `json:"timezone"`
	Windows           []string `json:"windows"`
	PeakMultiplier    float64  `json:"peak_multiplier"`
	InputPer1KTokens  float64  `json:"input_per_1k_tokens"`
	OutputPer1KTokens float64  `json:"output_per_1k_tokens"`
	CacheReadPer1K    float64  `json:"cache_read_per_1k,omitempty"`
}

// PublicCatalogTier is one context-length bracket of a tiered (阶梯) price.
//
// The bracket is left-open, right-closed — (MinTokens, MaxTokens] — matching
// FindMatchingInterval in channel.go, which is the billing behaviour clients must
// describe: a request of exactly MaxTokens bills in THIS bracket, not the next.
// MaxTokens == nil is the open-ended top tier.
//
// The bracket is matched against the whole request context (input + cache-write +
// cache-read tokens; see calculateTokenCost), not input tokens alone.
//
// Prices are USD per 1k tokens (registry intervals are stored per-token → ×1000 to
// match the rest of the catalog).
type PublicCatalogTier struct {
	MinTokens         int     `json:"min_tokens"`
	MaxTokens         *int    `json:"max_tokens,omitempty"`
	InputPer1KTokens  float64 `json:"input_per_1k_tokens"`
	OutputPer1KTokens float64 `json:"output_per_1k_tokens"`
	CacheReadPer1K    float64 `json:"cache_read_per_1k,omitempty"`
}

// catalogRichEntry mirrors the litellm-shape JSON fields needed for the public
// catalog's compatibility file-source parser. Active-registry rows project the
// same metadata from LiteLLMModelPricing so price and display share one snapshot.
type catalogRichEntry struct {
	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	ThinkingOutputCostPerToken  *float64 `json:"thinking_output_cost_per_token"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	OutputCostPerImage          *float64 `json:"output_cost_per_image"`
	OutputCostPerImageToken     *float64 `json:"output_cost_per_image_token"`
	InputCostPerImageToken      *float64 `json:"input_cost_per_image_token"`
	ImagePrice1K                *float64 `json:"image_price_1k"`
	ImagePrice2K                *float64 `json:"image_price_2k"`
	ImagePrice4K                *float64 `json:"image_price_4k"`
	OutputCostPerSecond         *float64 `json:"output_cost_per_second"`
	LiteLLMProvider             string   `json:"litellm_provider"`
	Mode                        string   `json:"mode"`
	MaxInputTokens              int      `json:"max_input_tokens"`
	MaxOutputTokens             int      `json:"max_output_tokens"`
	SupportsVision              bool     `json:"supports_vision"`
	SupportsToolChoice          bool     `json:"supports_tool_choice"`
	SupportsFunctionCalling     bool     `json:"supports_function_calling"`
	SupportsPromptCaching       bool     `json:"supports_prompt_caching"`
	SupportsReasoning           bool     `json:"supports_reasoning"`
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
	cachedTk *tkPricingOverlaySnapshot
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
	s.cachedTk = nil
	s.mu.Unlock()
}

// InvalidateCache drops the cached catalog so the next BuildPublicCatalog
// re-parses + re-applies the active registry. The cache keys on the compatibility
// source file's mtime, so a registry hot change would otherwise serve stale prices.
// The runtime registry
// reload (pricing_service_tk_overlay_runtime.go) calls this after a swap. Nil-safe.
func (s *PricingCatalogService) InvalidateCache() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cached = nil
	s.cachedMt = time.Time{}
	s.cachedTk = nil
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

	for {
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

		registrySnapshot := loadTKPricingOverlaySnapshot()
		s.mu.RLock()
		cached := s.cached
		cachedMt := s.cachedMt
		cachedTk := s.cachedTk
		s.mu.RUnlock()
		if cached != nil && cachedTk == registrySnapshot && !modTime.IsZero() && modTime.Equal(cachedMt) {
			return cached
		}

		resp := buildCatalogFromBytes(data, modTime)
		// Enrich only a healthy (non-degraded) catalog: a garbage/empty source yields
		// an empty list and must STAY empty (AC-005 degraded→empty / 200-not-500
		// contract) rather than surfacing a partial registry-only catalog.
		if len(resp.Data) > 0 {
			applyCatalogRegistrySnapshot(resp, registrySnapshot)
		}

		if s.storeCatalogIfSnapshotCurrent(resp, modTime, registrySnapshot) {
			return resp
		}
	}
}

func (s *PricingCatalogService) storeCatalogIfSnapshotCurrent(resp *PublicCatalogResponse, modTime time.Time, snapshot *tkPricingOverlaySnapshot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if loadTKPricingOverlaySnapshot() != snapshot {
		return false
	}
	s.cached = resp
	s.cachedMt = modTime
	s.cachedTk = snapshot
	return true
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

// applyCatalogOverlayPricingFromSnapshot projects the active complete registry onto the
// public catalog. The file source may still supply compatibility rows, but it is
// sensor evidence only: even a non-zero external price cannot override the
// registry. Channel pricing remains a separately scoped higher-priority tier in
// the per-user menu and billing resolver.
func applyCatalogOverlayPricingFromSnapshot(resp *PublicCatalogResponse, snapshot *tkPricingOverlaySnapshot) {
	if resp == nil {
		return
	}
	if snapshot == nil {
		return
	}
	overlay := snapshot.Models
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
		isMedia := p.OutputCostPerImage > 0 || p.OutputCostPerImageToken > 0 || p.OutputCostPerSecond > 0
		if p.InputCostPerToken == 0 && p.OutputCostPerToken == 0 && !isMedia && !p.ExplicitFree {
			continue
		}
		projected := catalogModelFromRegistry(name, p)
		if idx, ok := seen[name]; ok {
			projected.Availability = resp.Data[idx].Availability
			resp.Data[idx] = projected
			continue
		}
		resp.Data = append(resp.Data, projected)
		appended = true
	}
	if appended {
		sort.Slice(resp.Data, func(i, j int) bool {
			return resp.Data[i].ModelID < resp.Data[j].ModelID
		})
	}
}

func catalogModelFromRegistry(name string, p *LiteLLMModelPricing) PublicCatalogModel {
	in, out := p.InputCostPerToken, p.OutputCostPerToken
	cacheRead, cacheWrite := p.CacheReadInputTokenCost, p.CacheCreationInputTokenCost
	e := catalogRichEntry{
		InputCostPerToken:           &in,
		OutputCostPerToken:          &out,
		CacheReadInputTokenCost:     &cacheRead,
		CacheCreationInputTokenCost: &cacheWrite,
		LiteLLMProvider:             p.LiteLLMProvider,
		Mode:                        p.Mode,
		MaxInputTokens:              p.MaxInputTokens,
		MaxOutputTokens:             p.MaxOutputTokens,
		SupportsVision:              p.SupportsVision,
		SupportsToolChoice:          p.SupportsToolChoice,
		SupportsFunctionCalling:     p.SupportsFunctionCalling,
		SupportsPromptCaching:       p.SupportsPromptCaching,
		SupportsReasoning:           p.SupportsReasoning,
		SupportsResponseSchema:      p.SupportsResponseSchema,
		SupportsPDFInput:            p.SupportsPDFInput,
		SupportsWebSearch:           p.SupportsWebSearch,
	}
	if p.ThinkingOutputCostPerToken > 0 {
		v := p.ThinkingOutputCostPerToken
		e.ThinkingOutputCostPerToken = &v
	}
	if p.OutputCostPerImage > 0 {
		v := p.OutputCostPerImage
		e.OutputCostPerImage = &v
	}
	if p.OutputCostPerImageToken > 0 {
		v := p.OutputCostPerImageToken
		e.OutputCostPerImageToken = &v
	}
	if p.InputCostPerImageToken > 0 {
		v := p.InputCostPerImageToken
		e.InputCostPerImageToken = &v
	}
	if p.ImagePrice1K > 0 {
		v := p.ImagePrice1K
		e.ImagePrice1K = &v
	}
	if p.ImagePrice2K > 0 {
		v := p.ImagePrice2K
		e.ImagePrice2K = &v
	}
	if p.ImagePrice4K > 0 {
		v := p.ImagePrice4K
		e.ImagePrice4K = &v
	}
	if p.OutputCostPerSecond > 0 {
		v := p.OutputCostPerSecond
		e.OutputCostPerSecond = &v
	}
	return catalogModelFromEntry(name, &e)
}

// attachCatalogOverlayTiersFromSnapshot surfaces registry-defined input-token interval (阶梯)
// pricing on the public catalog. Runs after applyCatalogOverlayPricingFromSnapshot so it sees
// every model (file-sourced and overlay-filled). The flat Input/OutputPer1KTokens
// fields stay the base (first) tier for pre-tier clients; this fills the full
// ladder on Pricing.Tiers for tier-aware clients and the admin CSV export. Overlay
// Registry interval prices are per-token → ×1000 to match the catalog's per-1k unit.
// Purely additive (never mutates flat prices), idempotent, nil-safe.
func attachCatalogOverlayTiersFromSnapshot(resp *PublicCatalogResponse, snapshot *tkPricingOverlaySnapshot) {
	if resp == nil || len(resp.Data) == 0 {
		return
	}
	if snapshot == nil {
		return
	}
	overlay := snapshot.Models
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

func applyCatalogOfficialListBaseTaxFromSnapshot(resp *PublicCatalogResponse, snapshot *tkPricingOverlaySnapshot) {
	if resp == nil {
		return
	}
	if snapshot == nil {
		return
	}
	for i := range resp.Data {
		tkApplyBaseTaxToPublicCatalogPricingWithPolicy(resp.Data[i].Vendor, &resp.Data[i].Pricing, snapshot.BaseTax)
	}
}

func attachCatalogDeepSeekPeakValleyFromSnapshot(resp *PublicCatalogResponse, snapshot *tkPricingOverlaySnapshot) {
	if snapshot == nil {
		return
	}
	policy := snapshot.DeepSeekPeakValley
	if resp == nil || policy == nil || policy.PeakMultiplier <= 1 {
		return
	}
	windows := make([]string, 0, len(policy.Windows))
	for _, w := range policy.Windows {
		if w.Start != "" && w.End != "" {
			windows = append(windows, w.Start+"-"+w.End)
		}
	}
	tz := strings.TrimSpace(policy.Timezone)
	if tz == "" {
		tz = "Asia/Shanghai"
	}
	for i := range resp.Data {
		modelID := resp.Data[i].ModelID
		if !tkDeepSeekPeakValleyAppliesWithPolicy(policy, modelID, PricingSourceLiteLLM) {
			continue
		}
		p := &resp.Data[i].Pricing
		if p.InputPer1KTokens == 0 && p.OutputPer1KTokens == 0 {
			continue
		}
		peak := PublicCatalogPeakValley{
			Timezone:          tz,
			Windows:           windows,
			PeakMultiplier:    policy.PeakMultiplier,
			InputPer1KTokens:  p.InputPer1KTokens * policy.PeakMultiplier,
			OutputPer1KTokens: p.OutputPer1KTokens * policy.PeakMultiplier,
		}
		if p.CacheReadPer1K > 0 {
			peak.CacheReadPer1K = p.CacheReadPer1K * policy.PeakMultiplier
		}
		p.PeakValley = &peak
	}
}

func applyCatalogRegistrySnapshot(resp *PublicCatalogResponse, snapshot *tkPricingOverlaySnapshot) {
	applyCatalogOverlayPricingFromSnapshot(resp, snapshot)
	attachCatalogOverlayTiersFromSnapshot(resp, snapshot)
	applyCatalogOfficialListBaseTaxFromSnapshot(resp, snapshot)
	attachCatalogVideoPriceTiersFromSnapshot(resp, snapshot)
	attachCatalogDeepSeekPeakValleyFromSnapshot(resp, snapshot)
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
	if e.Mode == "embedding" {
		pricing.BillingMode = "embedding"
	}
	switch catalogMediaBillingMode(e) {
	case "video":
		pricing.BillingMode = "video"
		pricing.OutputCostPerSecond = *e.OutputCostPerSecond
	case "image":
		pricing.BillingMode = "image"
		if e.OutputCostPerImage != nil {
			pricing.OutputCostPerImage = *e.OutputCostPerImage
		}
		if e.InputCostPerImageToken != nil {
			pricing.InputCostPerImageToken = *e.InputCostPerImageToken
		}
		if e.OutputCostPerImageToken != nil {
			pricing.OutputCostPerImageToken = *e.OutputCostPerImageToken
		}
		if e.ImagePrice1K != nil {
			pricing.ImagePrice1K = *e.ImagePrice1K
		}
		if e.ImagePrice2K != nil {
			pricing.ImagePrice2K = *e.ImagePrice2K
		}
		if e.ImagePrice4K != nil {
			pricing.ImagePrice4K = *e.ImagePrice4K
		}
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
	case ((e.OutputCostPerImage != nil && *e.OutputCostPerImage > 0) ||
		(e.OutputCostPerImageToken != nil && *e.OutputCostPerImageToken > 0)) &&
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
	if e.SupportsReasoning {
		caps = append(caps, "reasoning")
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
