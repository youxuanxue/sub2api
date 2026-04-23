package service

// TokenKey: public model + pricing catalog (US-027 / docs/approved/user-cold-start.md §2 v1).
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
//   3) Empty list (never 500) — see US-027 AC-005.

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
	Object    string                `json:"object"`
	Data      []PublicCatalogModel  `json:"data"`
	UpdatedAt time.Time             `json:"updated_at"`
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
}

// PublicCatalogPricing exposes prices in USD per 1k tokens. Currency is always
// "USD" (matches users.balance unit). Per-1k is chosen over per-token to keep
// the magnitudes human-readable; precision is preserved via float64.
type PublicCatalogPricing struct {
	Currency          string  `json:"currency"`
	InputPer1KTokens  float64 `json:"input_per_1k_tokens"`
	OutputPer1KTokens float64 `json:"output_per_1k_tokens"`
	CacheReadPer1K    float64 `json:"cache_read_per_1k,omitempty"`
	CacheWritePer1K   float64 `json:"cache_write_per_1k,omitempty"`
}

// catalogRichEntry mirrors the litellm-shape JSON fields needed for the public
// catalog. Fields beyond what PricingService's billing flow uses (max_*,
// supports_*, deprecation_date) are kept here so we don't pollute the billing
// data structures.
type catalogRichEntry struct {
	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
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
// 500, per US-027 AC-005.
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
		if e.InputCostPerToken == nil && e.OutputCostPerToken == nil {
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

func catalogModelFromEntry(name string, e *catalogRichEntry) PublicCatalogModel {
	return PublicCatalogModel{
		ModelID: name,
		Vendor:  e.LiteLLMProvider,
		Pricing: PublicCatalogPricing{
			Currency:          "USD",
			InputPer1KTokens:  perTokenTo1K(e.InputCostPerToken),
			OutputPer1KTokens: perTokenTo1K(e.OutputCostPerToken),
			CacheReadPer1K:    perTokenTo1K(e.CacheReadInputTokenCost),
			CacheWritePer1K:   perTokenTo1K(e.CacheCreationInputTokenCost),
		},
		ContextWindow:   e.MaxInputTokens,
		MaxOutputTokens: e.MaxOutputTokens,
		Capabilities:    catalogCapabilities(e),
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
