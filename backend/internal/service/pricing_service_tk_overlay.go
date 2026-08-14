package service

// TK complete global pricing registry.
//
// The historical overlay file and symbol names are retained to minimize conflict
// with upstream-shaped code. Their semantics are no longer fill-only: the active
// registry snapshot owns every global price dimension and executable price policy.
// Provider/LiteLLM documents are comparison sensors only and are replaced at the
// existing parser choke point before data reaches billing or catalog presentation.
//
// A protected-main runtime envelope atomically replaces the embedded registry.
// Invalid envelopes keep the last-known-good snapshot. Scoped
// channel_model_pricing rows remain a higher-precedence commercial override, not a
// second global owner. See docs/approved/pricing-registry-hot-reload.md.

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

//go:embed tk_pricing_overlay.json
var tkPricingOverlayRaw []byte

type tkPricingOverlayExecutableConfig struct {
	OfficialListBaseTax *tkOfficialListBaseTaxPolicy `json:"official_list_base_tax"`
	DeepSeekPeakValley  *tkDeepSeekPeakValleyPolicy  `json:"deepseek_peak_valley"`
	WebSearchPrice      *float64                     `json:"web_search_price_per_call"`
}

type tkPricingRegistrySnapshotMetadata struct {
	SchemaVersion  int    `json:"schema_version"`
	SourceCommit   string `json:"source_commit"`
	RegistrySHA256 string `json:"registry_sha256"`
}

type tkPricingRegistryRuntimeEnvelope struct {
	Snapshot           tkPricingRegistrySnapshotMetadata `json:"_snapshot"`
	RegistryGzipBase64 string                            `json:"_registry_gzip_base64"`
}

type tkPricingOverlaySnapshot struct {
	Models             map[string]*LiteLLMModelPricing
	BaseTax            tkOfficialListBaseTaxPolicy
	DeepSeekPeakValley *tkDeepSeekPeakValleyPolicy
	WebSearchPrice     float64
	Metadata           tkPricingRegistrySnapshotMetadata
}

type tkPricingOverlayDocument struct {
	Models             map[string]*LiteLLMModelPricing
	BaseTax            *tkOfficialListBaseTaxPolicy
	DeepSeekPeakValley *tkDeepSeekPeakValleyPolicy
	WebSearchPrice     *float64
}

// tkOverlayEffective is the live immutable complete-registry snapshot. A valid
// runtime envelope replaces the embedded artifact exactly; invalid input leaves
// the last-known-good pointer untouched.
var (
	tkOverlayMu        sync.RWMutex
	tkOverlayEffective *tkPricingOverlaySnapshot
)

// parseTKOverlayDocument parses one complete registry artifact into model prices
// plus executable configuration.
func parseTKOverlayDocument(data []byte) (*tkPricingOverlayDocument, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	doc := &tkPricingOverlayDocument{Models: make(map[string]*LiteLLMModelPricing, len(raw))}
	if rawConfig, ok := raw["_config"]; ok {
		var config tkPricingOverlayExecutableConfig
		decoder := json.NewDecoder(bytes.NewReader(rawConfig))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&config); err != nil {
			return nil, fmt.Errorf("parse overlay _config: %w", err)
		}
		if config.OfficialListBaseTax == nil {
			return nil, fmt.Errorf("overlay _config.official_list_base_tax is required")
		}
		if err := config.OfficialListBaseTax.validate(); err != nil {
			return nil, err
		}
		policy := *config.OfficialListBaseTax
		doc.BaseTax = &policy
		if config.DeepSeekPeakValley != nil {
			if err := config.DeepSeekPeakValley.validate(); err != nil {
				return nil, fmt.Errorf("overlay _config.deepseek_peak_valley: %w", err)
			}
			peakPolicy := *config.DeepSeekPeakValley
			doc.DeepSeekPeakValley = &peakPolicy
		}
		if config.WebSearchPrice == nil || !tkFiniteNonNegative(*config.WebSearchPrice) {
			return nil, fmt.Errorf("overlay _config.web_search_price_per_call must be finite and >= 0")
		}
		webSearchPrice := *config.WebSearchPrice
		doc.WebSearchPrice = &webSearchPrice
	}

	for name, rawEntry := range raw {
		if strings.HasPrefix(name, "_") {
			continue
		}
		if name != strings.ToLower(strings.TrimSpace(name)) || strings.Contains(name, "/") {
			return nil, fmt.Errorf("overlay model owner %q must be normalized lowercase and bare", name)
		}
		var e LiteLLMRawEntry
		if err := json.Unmarshal(rawEntry, &e); err != nil {
			return nil, fmt.Errorf("parse overlay model %s: %w", name, err)
		}
		p := &LiteLLMModelPricing{
			LiteLLMProvider:         e.LiteLLMProvider,
			Mode:                    e.Mode,
			SupportsPromptCaching:   e.SupportsPromptCaching,
			SupportsServiceTier:     e.SupportsServiceTier,
			MaxInputTokens:          e.MaxInputTokens,
			MaxOutputTokens:         e.MaxOutputTokens,
			SupportsVision:          e.SupportsVision,
			SupportsToolChoice:      e.SupportsToolChoice,
			SupportsFunctionCalling: e.SupportsFunctionCalling,
			SupportsReasoning:       e.SupportsReasoning,
			SupportsResponseSchema:  e.SupportsResponseSchema,
			SupportsPDFInput:        e.SupportsPDFInput,
			SupportsWebSearch:       e.SupportsWebSearch,
			TokenPricingAbsent:      e.InputCostPerToken == nil && e.OutputCostPerToken == nil,
			ExplicitFree:            e.ExplicitFree,
		}
		if e.OutputCostPerImage != nil {
			p.OutputCostPerImage = *e.OutputCostPerImage
		}
		if e.OutputCostPerImageToken != nil {
			p.OutputCostPerImageToken = *e.OutputCostPerImageToken
		}
		if e.InputCostPerImageToken != nil {
			p.InputCostPerImageToken = *e.InputCostPerImageToken
		}
		if e.ImagePrice1K != nil {
			p.ImagePrice1K = *e.ImagePrice1K
		}
		if e.ImagePrice2K != nil {
			p.ImagePrice2K = *e.ImagePrice2K
		}
		if e.ImagePrice4K != nil {
			p.ImagePrice4K = *e.ImagePrice4K
		}
		if e.OutputCostPerSecond != nil {
			p.OutputCostPerSecond = *e.OutputCostPerSecond
		}
		if e.InputCostPerToken != nil {
			p.InputCostPerToken = *e.InputCostPerToken
		}
		if e.InputCostPerTokenPriority != nil {
			p.InputCostPerTokenPriority = *e.InputCostPerTokenPriority
		}
		if e.OutputCostPerToken != nil {
			p.OutputCostPerToken = *e.OutputCostPerToken
		}
		if e.OutputCostPerTokenPriority != nil {
			p.OutputCostPerTokenPriority = *e.OutputCostPerTokenPriority
		}
		if e.ThinkingOutputCostPerToken != nil {
			p.ThinkingOutputCostPerToken = *e.ThinkingOutputCostPerToken
		}
		if e.CacheCreationInputTokenCost != nil {
			p.CacheCreationInputTokenCost = *e.CacheCreationInputTokenCost
		}
		if e.CacheCreationInputTokenCostPriority != nil {
			p.CacheCreationInputTokenCostPriority = *e.CacheCreationInputTokenCostPriority
		}
		if e.CacheCreationInputTokenCostAbove1hr != nil {
			p.CacheCreationInputTokenCostAbove1hr = *e.CacheCreationInputTokenCostAbove1hr
		}
		if e.CacheReadInputTokenCost != nil {
			p.CacheReadInputTokenCost = *e.CacheReadInputTokenCost
		}
		if e.CacheReadInputTokenCostPriority != nil {
			p.CacheReadInputTokenCostPriority = *e.CacheReadInputTokenCostPriority
		}
		if e.LongContextInputTokenThreshold != nil {
			p.LongContextInputTokenThreshold = *e.LongContextInputTokenThreshold
		}
		if e.LongContextInputCostMultiplier != nil {
			p.LongContextInputCostMultiplier = *e.LongContextInputCostMultiplier
		}
		if e.LongContextOutputCostMultiplier != nil {
			p.LongContextOutputCostMultiplier = *e.LongContextOutputCostMultiplier
		}
		if e.LongContextThresholdInclusive != nil {
			p.LongContextThresholdInclusive = *e.LongContextThresholdInclusive
		}
		tkNormalizeAbove272KPricing(p, e)
		// TK: input-token interval (tiered) pricing. LiteLLMRawEntry has no
		// "intervals" field (it is TK-overlay-only), so parse the raw entry a
		// second time into a TK-local shape. An entry's flat input/output cost
		// stays as the out-of-range fallback (BasePricing); the intervals drive
		// whole-request tier billing via ResolvedPricing.Intervals.
		var ext struct {
			Intervals []tkOverlayRawInterval `json:"intervals"`
		}
		if err := json.Unmarshal(rawEntry, &ext); err == nil && len(ext.Intervals) > 0 {
			p.Intervals = tkBuildOverlayIntervals(ext.Intervals)
		}
		var videoExt struct {
			VideoPriceTiers        []tkOverlayRawVideoTier `json:"video_price_tiers"`
			DefaultVideoResolution string                  `json:"default_video_resolution"`
		}
		if err := json.Unmarshal(rawEntry, &videoExt); err != nil {
			return nil, fmt.Errorf("parse overlay model %s video tiers: %w", name, err)
		}
		if videoExt.VideoPriceTiers != nil {
			tiers, defaultResolution, err := tkValidateAndBuildOverlayVideoTiers(
				videoExt.VideoPriceTiers,
				videoExt.DefaultVideoResolution,
				p.OutputCostPerSecond,
			)
			if err != nil {
				return nil, fmt.Errorf("overlay model %s video tiers: %w", name, err)
			}
			p.VideoPriceTiers = tiers
			p.DefaultVideoResolution = defaultResolution
		} else if strings.TrimSpace(videoExt.DefaultVideoResolution) != "" {
			return nil, fmt.Errorf("overlay model %s has default_video_resolution without video_price_tiers", name)
		}
		if err := validateTKPricingRegistryOwner(name, p); err != nil {
			return nil, err
		}
		doc.Models[name] = p
	}
	return doc, nil
}

const tkPricingRegistryMaxBytes = 8 << 20

func tkFiniteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func tkPositive(value float64) bool {
	return tkFiniteNonNegative(value) && value > 0
}

func tkNormalizeAbove272KPricing(p *LiteLLMModelPricing, raw LiteLLMRawEntry) {
	if p == nil || p.LongContextInputTokenThreshold > 0 {
		return
	}
	if raw.InputCostPerTokenAbove272K == nil || raw.OutputCostPerTokenAbove272K == nil ||
		!tkPositive(p.InputCostPerToken) || !tkPositive(p.OutputCostPerToken) ||
		!tkPositive(*raw.InputCostPerTokenAbove272K) || !tkPositive(*raw.OutputCostPerTokenAbove272K) {
		return
	}
	p.LongContextInputTokenThreshold = 272000
	p.LongContextInputCostMultiplier = *raw.InputCostPerTokenAbove272K / p.InputCostPerToken
	p.LongContextOutputCostMultiplier = *raw.OutputCostPerTokenAbove272K / p.OutputCostPerToken
}

func validateTKPricingRegistryOwner(model string, p *LiteLLMModelPricing) error {
	if p == nil {
		return fmt.Errorf("registry model %s is null", model)
	}
	values := []float64{
		p.InputCostPerToken, p.InputCostPerTokenPriority, p.OutputCostPerToken,
		p.OutputCostPerTokenPriority, p.ThinkingOutputCostPerToken,
		p.CacheCreationInputTokenCost, p.CacheCreationInputTokenCostPriority,
		p.CacheCreationInputTokenCostAbove1hr, p.CacheReadInputTokenCost,
		p.CacheReadInputTokenCostPriority, p.LongContextInputCostMultiplier,
		p.LongContextOutputCostMultiplier, p.OutputCostPerImage,
		p.OutputCostPerImageToken, p.InputCostPerImageToken, p.ImagePrice1K,
		p.ImagePrice2K, p.ImagePrice4K, p.OutputCostPerSecond,
	}
	for _, value := range values {
		if !tkFiniteNonNegative(value) {
			return fmt.Errorf("registry model %s has negative or non-finite price", model)
		}
	}
	if p.LongContextInputTokenThreshold < 0 {
		return fmt.Errorf("registry model %s has negative long-context threshold", model)
	}
	for i := range p.Intervals {
		iv := p.Intervals[i]
		if iv.MinTokens < 0 || (iv.MaxTokens != nil && *iv.MaxTokens <= iv.MinTokens) {
			return fmt.Errorf("registry model %s has invalid interval %d bounds", model, i)
		}
		for _, price := range []*float64{iv.InputPrice, iv.OutputPrice, iv.CacheReadPrice, iv.CacheWritePrice} {
			if price != nil && !tkFiniteNonNegative(*price) {
				return fmt.Errorf("registry model %s has invalid interval %d price", model, i)
			}
		}
	}

	if p.ExplicitFree {
		for _, value := range values {
			if value != 0 {
				return fmt.Errorf("registry model %s is explicit_free but carries a non-zero runtime price", model)
			}
		}
		return nil
	}
	tokenPriced := tkPositive(p.InputCostPerToken) && tkPositive(p.OutputCostPerToken)
	intervalPriced := false
	for i := range p.Intervals {
		iv := p.Intervals[i]
		if iv.InputPrice != nil && iv.OutputPrice != nil && tkPositive(*iv.InputPrice) && tkPositive(*iv.OutputPrice) {
			intervalPriced = true
			break
		}
	}
	switch p.Mode {
	case "chat", "completion", "responses", "realtime", "audio_transcription", "audio_speech":
		if !tokenPriced && !intervalPriced {
			return fmt.Errorf("registry model %s mode=%s lacks token settlement prices", model, p.Mode)
		}
	case "embedding":
		if !tkPositive(p.InputCostPerToken) {
			return fmt.Errorf("registry model %s mode=embedding lacks input price", model)
		}
	case "image_generation":
		if !tkPositive(p.OutputCostPerImage) && !tkPositive(p.OutputCostPerImageToken) {
			return fmt.Errorf("registry model %s mode=image_generation lacks per-image or image-token price", model)
		}
	case "video_generation":
		if !tkPositive(p.OutputCostPerSecond) {
			return fmt.Errorf("registry model %s mode=video_generation lacks per-second price", model)
		}
	default:
		return fmt.Errorf("registry model %s has unsupported mode %q", model, p.Mode)
	}
	return nil
}

func buildTKPricingRegistrySnapshot(registryBytes []byte, metadata tkPricingRegistrySnapshotMetadata) (*tkPricingOverlaySnapshot, error) {
	doc, err := parseTKOverlayDocument(registryBytes)
	if err != nil {
		return nil, err
	}
	if len(doc.Models) == 0 || doc.BaseTax == nil || doc.WebSearchPrice == nil {
		return nil, fmt.Errorf("complete registry requires models, base-tax policy, and web-search price")
	}
	snapshot := &tkPricingOverlaySnapshot{
		Models:         doc.Models,
		BaseTax:        *doc.BaseTax,
		WebSearchPrice: *doc.WebSearchPrice,
		Metadata:       metadata,
	}
	if doc.DeepSeekPeakValley != nil {
		policy := *doc.DeepSeekPeakValley
		snapshot.DeepSeekPeakValley = &policy
	}
	return snapshot, nil
}

func decodeTKPricingRegistryEnvelope(envelopeBytes []byte) ([]byte, tkPricingRegistrySnapshotMetadata, error) {
	var envelope tkPricingRegistryRuntimeEnvelope
	decoder := json.NewDecoder(bytes.NewReader(envelopeBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, tkPricingRegistrySnapshotMetadata{}, fmt.Errorf("decode registry envelope: %w", err)
	}
	if envelope.Snapshot.SchemaVersion != 1 {
		return nil, envelope.Snapshot, fmt.Errorf("unsupported registry schema_version %d", envelope.Snapshot.SchemaVersion)
	}
	commit := envelope.Snapshot.SourceCommit
	if (len(commit) != 40 && len(commit) != 64) || commit != strings.ToLower(commit) {
		return nil, envelope.Snapshot, fmt.Errorf("registry source_commit must be a full lowercase git object id")
	}
	if _, err := hex.DecodeString(commit); err != nil {
		return nil, envelope.Snapshot, fmt.Errorf("registry source_commit is not hexadecimal: %w", err)
	}
	digest := envelope.Snapshot.RegistrySHA256
	if len(digest) != sha256.Size*2 || digest != strings.ToLower(digest) {
		return nil, envelope.Snapshot, fmt.Errorf("registry_sha256 must be lowercase SHA-256 hex")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return nil, envelope.Snapshot, fmt.Errorf("registry_sha256 is not hexadecimal: %w", err)
	}
	compressed, err := base64.StdEncoding.DecodeString(envelope.RegistryGzipBase64)
	if err != nil {
		return nil, envelope.Snapshot, fmt.Errorf("decode registry gzip base64: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, envelope.Snapshot, fmt.Errorf("open registry gzip: %w", err)
	}
	decompressed, readErr := io.ReadAll(io.LimitReader(zr, tkPricingRegistryMaxBytes+1))
	closeErr := zr.Close()
	if readErr != nil || closeErr != nil {
		return nil, envelope.Snapshot, fmt.Errorf("decompress registry: read=%v close=%v", readErr, closeErr)
	}
	if len(decompressed) == 0 || len(decompressed) > tkPricingRegistryMaxBytes {
		return nil, envelope.Snapshot, fmt.Errorf("decompressed registry size %d is outside allowed range", len(decompressed))
	}
	actual := sha256.Sum256(decompressed)
	if hex.EncodeToString(actual[:]) != digest {
		return nil, envelope.Snapshot, fmt.Errorf("registry digest mismatch")
	}
	return decompressed, envelope.Snapshot, nil
}

func buildTKPricingOverlaySnapshot(runtimeBytes []byte) (*tkPricingOverlaySnapshot, error) {
	registryBytes := tkPricingOverlayRaw
	sum := sha256.Sum256(registryBytes)
	metadata := tkPricingRegistrySnapshotMetadata{
		SchemaVersion:  1,
		SourceCommit:   "embedded",
		RegistrySHA256: hex.EncodeToString(sum[:]),
	}
	if len(bytes.TrimSpace(runtimeBytes)) > 0 {
		var err error
		registryBytes, metadata, err = decodeTKPricingRegistryEnvelope(runtimeBytes)
		if err != nil {
			return nil, fmt.Errorf("parse runtime complete registry: %w", err)
		}
	}
	return buildTKPricingRegistrySnapshot(registryBytes, metadata)
}

// rebuildTKOverlayUnion keeps its historical name to minimize conflict surface,
// but now performs an exact complete-registry replacement, never a union.
//
// Safety invariants:
//   - Nil/empty runtimeBytes selects the complete embedded registry.
//   - A valid runtime envelope replaces that registry exactly and atomically.
//   - Invalid input keeps the previous immutable snapshot; before the first valid
//     runtime load, the validated embedded registry establishes the LKG.
func rebuildTKOverlayUnion(runtimeBytes []byte) {
	snapshot, err := buildTKPricingOverlaySnapshot(runtimeBytes)
	if err != nil {
		tkOverlayMu.RLock()
		hasCurrent := tkOverlayEffective != nil
		tkOverlayMu.RUnlock()
		if !hasCurrent {
			floor, floorErr := buildTKPricingOverlaySnapshot(nil)
			if floorErr == nil {
				tkOverlayMu.Lock()
				if tkOverlayEffective == nil {
					tkOverlayEffective = floor
				}
				tkOverlayMu.Unlock()
			}
		}
		// Invalid runtime keeps the previous immutable snapshot. If this is the
		// first load, the embedded complete registry above establishes the LKG.
		logger.LegacyPrintf("service.pricing", "[Pricing] registry snapshot build failed (keeping current effective map): %v", err)
		return
	}
	tkOverlayMu.Lock()
	tkOverlayEffective = snapshot
	tkOverlayMu.Unlock()
}

func loadTKPricingOverlaySnapshot() *tkPricingOverlaySnapshot {
	tkOverlayMu.RLock()
	snapshot := tkOverlayEffective
	tkOverlayMu.RUnlock()
	if snapshot != nil {
		return snapshot
	}
	rebuildTKOverlayUnion(nil)
	tkOverlayMu.RLock()
	defer tkOverlayMu.RUnlock()
	return tkOverlayEffective
}

// loadTKPricingOverlay returns the live complete registry.
// First call before any explicit rebuild lazily builds the embedded complete
// registry, so runtime publication is optional for process startup.
func loadTKPricingOverlay() map[string]*LiteLLMModelPricing {
	snapshot := loadTKPricingOverlaySnapshot()
	if snapshot == nil {
		return nil
	}
	return snapshot.Models
}

// applyTKPricingOverlay preserves the upstream parser/sync call sites while
// preventing provider documents from participating in runtime billing.
func applyTKPricingOverlay(result map[string]*LiteLLMModelPricing) {
	if result == nil {
		return
	}
	clear(result)
	for name, pricing := range loadTKPricingOverlay() {
		result[name] = pricing
	}
}

// tkIsEffectivelyUnpriced reports whether a pricing entry carries no billable
// price at all: every cost field is zero. Provider sensors use 0.0 for "cost
// unknown" (not "free"), so such an entry must not become a registry owner, and
// billing must not treat it as a successful pricing lookup. Entries priced only
// per-image / per-second (imagen, veo) have zero token costs but a non-zero
// media cost field, so they correctly count as priced.
func tkIsEffectivelyUnpriced(p *LiteLLMModelPricing) bool {
	if p == nil {
		return true
	}
	if p.ExplicitFree {
		return false
	}
	// Interval (tiered) pricing is a price even if the flat base fields were left
	// zero; never treat a tiered registry owner as an unknown placeholder.
	if len(p.Intervals) > 0 {
		return false
	}
	if len(p.VideoPriceTiers) > 0 {
		return false
	}
	return p.InputCostPerToken == 0 &&
		p.InputCostPerTokenPriority == 0 &&
		p.OutputCostPerToken == 0 &&
		p.OutputCostPerTokenPriority == 0 &&
		p.CacheCreationInputTokenCost == 0 &&
		p.CacheCreationInputTokenCostAbove1hr == 0 &&
		p.CacheReadInputTokenCost == 0 &&
		p.CacheReadInputTokenCostPriority == 0 &&
		p.OutputCostPerImage == 0 &&
		p.OutputCostPerImageToken == 0 &&
		p.InputCostPerImageToken == 0 &&
		p.ImagePrice1K == 0 &&
		p.ImagePrice2K == 0 &&
		p.ImagePrice4K == 0 &&
		p.OutputCostPerSecond == 0
}

func tkRegistryWebSearchPricePerCall() float64 {
	snapshot := loadTKPricingOverlaySnapshot()
	if snapshot == nil {
		return 0
	}
	return snapshot.WebSearchPrice
}

// tkOverlayRawInterval is the JSON shape of one registry owner's
// "intervals" array. Boundaries follow FindMatchingInterval (channel.go):
// MinTokens is EXCLUSIVE, MaxTokens INCLUSIVE (nil = unbounded), keyed on the
// request's input context tokens (InputTokens + CacheReadTokens) — exactly the
// DashScope "0<Token<=256K" tier semantics. Costs are USD per single token.
type tkOverlayRawInterval struct {
	MinTokens                   int      `json:"min_tokens"`
	MaxTokens                   *int     `json:"max_tokens"`
	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
}

// tkOverlayRawVideoTier is the JSON shape of one registry video_price_tiers[] row.
type tkOverlayRawVideoTier struct {
	Resolution                   string   `json:"resolution"`
	OutputCostPerSecond          *float64 `json:"output_cost_per_second"`
	OutputCostPerSecondSilent    *float64 `json:"output_cost_per_second_silent"`
	InputImageSurchargePerSecond *float64 `json:"input_image_surcharge_per_second"`
	DefaultForModel              bool     `json:"default_for_model"`
}

// tkBuildOverlayIntervals converts the parsed registry intervals into the shared
// PricingInterval shape the billing engine already consumes (FindMatchingInterval
// + tkOverlayIntervalOntoBasePricing). SortOrder preserves the JSON order.
func tkBuildOverlayIntervals(raw []tkOverlayRawInterval) []PricingInterval {
	out := make([]PricingInterval, 0, len(raw))
	for i := range raw {
		r := raw[i]
		out = append(out, PricingInterval{
			MinTokens:       r.MinTokens,
			MaxTokens:       r.MaxTokens,
			InputPrice:      r.InputCostPerToken,
			OutputPrice:     r.OutputCostPerToken,
			CacheReadPrice:  r.CacheReadInputTokenCost,
			CacheWritePrice: r.CacheCreationInputTokenCost,
			SortOrder:       i,
		})
	}
	return out
}
