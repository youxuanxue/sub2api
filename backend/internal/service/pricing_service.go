package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"go.uber.org/zap"
)

var (
	openAIModelDatePattern = regexp.MustCompile(`-\d{8}$`)
	openAIModelBasePattern = regexp.MustCompile(`^(gpt-\d+(?:\.\d+)?)(?:-|$)`)
)

// LiteLLMModelPricing is the legacy wire-compatible schema used by registry
// owner rows. The name describes JSON field compatibility, not a runtime source:
// production data is loaded only from the embedded registry snapshot.
type LiteLLMModelPricing struct {
	InputCostPerToken          float64 `json:"input_cost_per_token"`
	InputCostPerTokenPriority  float64 `json:"input_cost_per_token_priority"`
	OutputCostPerToken         float64 `json:"output_cost_per_token"`
	OutputCostPerTokenPriority float64 `json:"output_cost_per_token_priority"`
	// ThinkingOutputCostPerToken is a registry-only field (provider snapshots have no such
	// concept): the higher output price the provider charges when the request runs
	// in thinking mode. Mirrors Alibaba DashScope's two-rate table for one model id
	// (qwen3-8b/14b/32b: same id, 非思考 vs 思考 output列). Billing selects it over
	// OutputCostPerToken when the request's enable_thinking is active — see
	// computeTokenBreakdown. Zero = no thinking-mode premium modeled (the default
	// for every non-Qwen model).
	ThinkingOutputCostPerToken          float64 `json:"thinking_output_cost_per_token"`
	CacheCreationInputTokenCost         float64 `json:"cache_creation_input_token_cost"`
	CacheCreationInputTokenCostPriority float64 `json:"cache_creation_input_token_cost_priority"`
	CacheCreationInputTokenCostAbove1hr float64 `json:"cache_creation_input_token_cost_above_1hr"`
	CacheReadInputTokenCost             float64 `json:"cache_read_input_token_cost"`
	CacheReadInputTokenCostPriority     float64 `json:"cache_read_input_token_cost_priority"`
	LongContextInputTokenThreshold      int     `json:"long_context_input_token_threshold,omitempty"`
	LongContextInputCostMultiplier      float64 `json:"long_context_input_cost_multiplier,omitempty"`
	LongContextOutputCostMultiplier     float64 `json:"long_context_output_cost_multiplier,omitempty"`
	SupportsServiceTier                 bool    `json:"supports_service_tier"`
	LiteLLMProvider                     string  `json:"litellm_provider"`
	Mode                                string  `json:"mode"`
	SupportsPromptCaching               bool    `json:"supports_prompt_caching"`
	// Registry-only catalog metadata travels with the same immutable registry
	// snapshot as pricing so billing and display cannot parse different facts.
	MaxInputTokens          int     `json:"max_input_tokens"`
	MaxOutputTokens         int     `json:"max_output_tokens"`
	SupportsVision          bool    `json:"supports_vision"`
	SupportsToolChoice      bool    `json:"supports_tool_choice"`
	SupportsFunctionCalling bool    `json:"supports_function_calling"`
	SupportsReasoning       bool    `json:"supports_reasoning"`
	SupportsResponseSchema  bool    `json:"supports_response_schema"`
	SupportsPDFInput        bool    `json:"supports_pdf_input"`
	SupportsWebSearch       bool    `json:"supports_web_search"`
	OutputCostPerImage      float64 `json:"output_cost_per_image"`       // 图片生成模型每张图片价格
	OutputCostPerImageToken float64 `json:"output_cost_per_image_token"` // 图片输出 token 价格
	InputCostPerImageToken  float64 `json:"input_cost_per_image_token"`  // 图片输入 token 价格
	ImagePrice1K            float64 `json:"image_price_1k,omitempty"`    // registry image tier price
	ImagePrice2K            float64 `json:"image_price_2k,omitempty"`
	ImagePrice4K            float64 `json:"image_price_4k,omitempty"`
	OutputCostPerSecond     float64 `json:"output_cost_per_second"` // 视频生成模型每秒价格（veo 等）

	// Intervals 输入-token 区间分档定价（registry 专用，见 tk_pricing_overlay.json
	// 的 "intervals"）。provider snapshots 无此概念。空 = 扁平定价。
	// 解析见 pricing_service_tk_overlay.go，接进 ResolvedPricing.Intervals 见
	// model_pricing_resolver_tk_overlay_intervals.go。
	Intervals []PricingInterval `json:"-"`

	// VideoPriceTiers 视频 resolution×audio 阶梯（registry 专用，见
	// tk_pricing_overlay.json "video_price_tiers"）。Pre-tax USD/s；base tax at read.
	VideoPriceTiers []PricingVideoTier `json:"-"`

	// DefaultVideoResolution 客户端未指定 resolution 时的默认档位（overlay SSOT）。
	DefaultVideoResolution string `json:"-"`

	// TokenPricingAbsent 表示源数据中 input/output token 价格均缺失（仅有图片价）。
	// 此类条目只可用于图片计费，token 计费必须解析 registry alias 或 fail-closed，
	// 否则 token 流量会被按 $0 计费。零值（false）表示条目具备 token 价格。
	TokenPricingAbsent bool `json:"-"`
	// ExplicitFree distinguishes a deliberate zero-price product row from an
	// unknown/unpriced placeholder. It is registry metadata, never inferred
	// from numeric zeroes.
	ExplicitFree bool `json:"-"`
}

// LiteLLMRawEntry parses the legacy-compatible JSON shape used by offline
// provider imports and registry parser tests. Runtime still owns one registry.
type LiteLLMRawEntry struct {
	InputCostPerToken                   *float64 `json:"input_cost_per_token"`
	InputCostPerTokenPriority           *float64 `json:"input_cost_per_token_priority"`
	OutputCostPerToken                  *float64 `json:"output_cost_per_token"`
	OutputCostPerTokenPriority          *float64 `json:"output_cost_per_token_priority"`
	ThinkingOutputCostPerToken          *float64 `json:"thinking_output_cost_per_token"`
	CacheCreationInputTokenCost         *float64 `json:"cache_creation_input_token_cost"`
	CacheCreationInputTokenCostPriority *float64 `json:"cache_creation_input_token_cost_priority"`
	CacheCreationInputTokenCostAbove1hr *float64 `json:"cache_creation_input_token_cost_above_1hr"`
	CacheReadInputTokenCost             *float64 `json:"cache_read_input_token_cost"`
	CacheReadInputTokenCostPriority     *float64 `json:"cache_read_input_token_cost_priority"`
	LongContextInputTokenThreshold      *int     `json:"long_context_input_token_threshold"`
	LongContextInputCostMultiplier      *float64 `json:"long_context_input_cost_multiplier"`
	LongContextOutputCostMultiplier     *float64 `json:"long_context_output_cost_multiplier"`
	// Some imported registry snapshots use LiteLLM's descriptive 272K field
	// names. Keep the importer tolerant, then normalize them into the runtime
	// long-context fields below so billing never depends on a second schema.
	InputCostPerTokenAbove272K       *float64 `json:"input_cost_per_token_above_272k_tokens"`
	OutputCostPerTokenAbove272K      *float64 `json:"output_cost_per_token_above_272k_tokens"`
	CacheReadInputTokenCostAbove272K *float64 `json:"cache_read_input_token_cost_above_272k_tokens"`
	SupportsServiceTier              bool     `json:"supports_service_tier"`
	LiteLLMProvider                  string   `json:"litellm_provider"`
	Mode                             string   `json:"mode"`
	SupportsPromptCaching            bool     `json:"supports_prompt_caching"`
	MaxInputTokens                   int      `json:"max_input_tokens"`
	MaxOutputTokens                  int      `json:"max_output_tokens"`
	SupportsVision                   bool     `json:"supports_vision"`
	SupportsToolChoice               bool     `json:"supports_tool_choice"`
	SupportsFunctionCalling          bool     `json:"supports_function_calling"`
	SupportsReasoning                bool     `json:"supports_reasoning"`
	SupportsResponseSchema           bool     `json:"supports_response_schema"`
	SupportsPDFInput                 bool     `json:"supports_pdf_input"`
	SupportsWebSearch                bool     `json:"supports_web_search"`
	OutputCostPerImage               *float64 `json:"output_cost_per_image"`
	OutputCostPerImageToken          *float64 `json:"output_cost_per_image_token"`
	InputCostPerImageToken           *float64 `json:"input_cost_per_image_token"`
	ImagePrice1K                     *float64 `json:"image_price_1k"`
	ImagePrice2K                     *float64 `json:"image_price_2k"`
	ImagePrice4K                     *float64 `json:"image_price_4k"`
	OutputCostPerSecond              *float64 `json:"output_cost_per_second"`
	ExplicitFree                     bool     `json:"explicit_free"`
}

// PricingService 动态价格服务
type PricingService struct {
	mu          sync.RWMutex
	pricingData map[string]*LiteLLMModelPricing
	lastUpdated time.Time
	localHash   string

	// 停止信号
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewPricingService 创建价格服务
func NewPricingService() *PricingService {
	s := &PricingService{
		pricingData: make(map[string]*LiteLLMModelPricing),
		stopCh:      make(chan struct{}),
	}
	return s
}

// Initialize 初始化价格服务
func (s *PricingService) Initialize() error {
	if s == nil {
		return fmt.Errorf("pricing service is nil")
	}
	// The embedded TK registry is the only global runtime pricing fact source.
	// DataDir and provider/LiteLLM import files are deliberately not consulted.
	if err := s.loadRegistryPricingData(); err != nil {
		return fmt.Errorf("failed to load pricing registry: %w", err)
	}

	// 启动定时更新
	s.startUpdateScheduler()

	logger.LegacyPrintf("service.pricing", "[Pricing] Service initialized with %d models", len(s.pricingData))
	return nil
}

// Stop 停止价格服务
func (s *PricingService) Stop() {
	close(s.stopCh)
	s.wg.Wait()
	logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Service stopped")
}

// startUpdateScheduler 启动定时更新调度器
func (s *PricingService) startUpdateScheduler() {
	if s == nil {
		return
	}
	// Registry changes are deployment-bound. There is no remote polling or
	// settings reload path that could become a second global pricing owner.
	logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Registry scheduler disabled; changes require deployment")
}

// parsePricingData 解析价格数据（处理各种格式）
func (s *PricingService) parsePricingData(body []byte) (map[string]*LiteLLMModelPricing, error) {
	// 首先解析为 map[string]json.RawMessage
	var rawData map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawData); err != nil {
		return nil, fmt.Errorf("parse raw JSON: %w", err)
	}

	result := make(map[string]*LiteLLMModelPricing)
	skipped := 0

	for modelName, rawEntry := range rawData {
		// 跳过 sample_spec 等文档条目
		if modelName == "sample_spec" {
			continue
		}

		// 尝试解析每个条目
		var entry LiteLLMRawEntry
		if err := json.Unmarshal(rawEntry, &entry); err != nil {
			skipped++
			continue
		}

		// 只保留有有效价格的条目。除 token 价外，也保留纯图片(每图/每图token)与
		// 纯视频(每秒)模型 —— 否则 imagen-4.0-* / veo-* 这类无 token 价的条目会被整体丢弃，
		// 导致下游按裸名匹配时根本找不到（命中错误兜底价）。
		if entry.InputCostPerToken == nil && entry.OutputCostPerToken == nil && entry.InputCostPerImageToken == nil &&
			entry.OutputCostPerImage == nil && entry.OutputCostPerImageToken == nil &&
			entry.OutputCostPerSecond == nil {
			continue
		}

		pricing := &LiteLLMModelPricing{
			LiteLLMProvider:       entry.LiteLLMProvider,
			Mode:                  entry.Mode,
			SupportsPromptCaching: entry.SupportsPromptCaching,
			SupportsServiceTier:   entry.SupportsServiceTier,
			TokenPricingAbsent:    entry.InputCostPerToken == nil && entry.OutputCostPerToken == nil && entry.InputCostPerImageToken == nil,
			ExplicitFree:          entry.ExplicitFree,
			// Authority is a registry policy, never a claim supplied by an
			// offline provider import.
		}

		if entry.InputCostPerToken != nil {
			pricing.InputCostPerToken = *entry.InputCostPerToken
		}
		if entry.InputCostPerTokenPriority != nil {
			pricing.InputCostPerTokenPriority = *entry.InputCostPerTokenPriority
		}
		if entry.OutputCostPerToken != nil {
			pricing.OutputCostPerToken = *entry.OutputCostPerToken
		}
		if entry.OutputCostPerTokenPriority != nil {
			pricing.OutputCostPerTokenPriority = *entry.OutputCostPerTokenPriority
		}
		if entry.ThinkingOutputCostPerToken != nil {
			pricing.ThinkingOutputCostPerToken = *entry.ThinkingOutputCostPerToken
		}
		if entry.CacheCreationInputTokenCost != nil {
			pricing.CacheCreationInputTokenCost = *entry.CacheCreationInputTokenCost
		}
		if entry.CacheCreationInputTokenCostPriority != nil {
			pricing.CacheCreationInputTokenCostPriority = *entry.CacheCreationInputTokenCostPriority
		}
		if entry.CacheCreationInputTokenCostAbove1hr != nil {
			pricing.CacheCreationInputTokenCostAbove1hr = *entry.CacheCreationInputTokenCostAbove1hr
		}
		if entry.CacheReadInputTokenCost != nil {
			pricing.CacheReadInputTokenCost = *entry.CacheReadInputTokenCost
		}
		if entry.CacheReadInputTokenCostPriority != nil {
			pricing.CacheReadInputTokenCostPriority = *entry.CacheReadInputTokenCostPriority
		}
		if entry.LongContextInputCostMultiplier != nil {
			pricing.LongContextInputCostMultiplier = *entry.LongContextInputCostMultiplier
		}
		if entry.LongContextOutputCostMultiplier != nil {
			pricing.LongContextOutputCostMultiplier = *entry.LongContextOutputCostMultiplier
		}
		if entry.LongContextInputTokenThreshold != nil {
			pricing.LongContextInputTokenThreshold = *entry.LongContextInputTokenThreshold
		} else if entry.InputCostPerTokenAbove272K != nil || entry.OutputCostPerTokenAbove272K != nil || entry.CacheReadInputTokenCostAbove272K != nil {
			pricing.LongContextInputTokenThreshold = 272000
		}
		if pricing.LongContextInputCostMultiplier == 0 && entry.InputCostPerToken != nil && entry.InputCostPerTokenAbove272K != nil && *entry.InputCostPerToken > 0 {
			pricing.LongContextInputCostMultiplier = *entry.InputCostPerTokenAbove272K / *entry.InputCostPerToken
		}
		if pricing.LongContextOutputCostMultiplier == 0 && entry.OutputCostPerToken != nil && entry.OutputCostPerTokenAbove272K != nil && *entry.OutputCostPerToken > 0 {
			pricing.LongContextOutputCostMultiplier = *entry.OutputCostPerTokenAbove272K / *entry.OutputCostPerToken
		}
		if entry.OutputCostPerImage != nil {
			pricing.OutputCostPerImage = *entry.OutputCostPerImage
		}
		if entry.OutputCostPerImageToken != nil {
			pricing.OutputCostPerImageToken = *entry.OutputCostPerImageToken
		}
		if entry.InputCostPerImageToken != nil {
			pricing.InputCostPerImageToken = *entry.InputCostPerImageToken
		}
		if entry.ImagePrice1K != nil {
			pricing.ImagePrice1K = *entry.ImagePrice1K
		}
		if entry.ImagePrice2K != nil {
			pricing.ImagePrice2K = *entry.ImagePrice2K
		}
		if entry.ImagePrice4K != nil {
			pricing.ImagePrice4K = *entry.ImagePrice4K
		}
		if entry.OutputCostPerSecond != nil {
			pricing.OutputCostPerSecond = *entry.OutputCostPerSecond
		}

		result[modelName] = pricing
	}

	if skipped > 0 {
		logger.LegacyPrintf("service.pricing", "[Pricing] Skipped %d invalid entries", skipped)
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no valid pricing entries found")
	}

	// parsePricingData remains available for offline provider import/tests. Runtime
	// loading never calls it for an external snapshot; it loads the embedded
	// registry snapshot directly (see loadRegistryPricingData).
	applyTKPricingOverlay(result)

	return result, nil
}

// loadRegistryPricingData atomically publishes the embedded registry.
// This is the only runtime loader. The flexible parser above remains solely for
// offline provider import tooling and focused parser tests.
func (s *PricingService) loadRegistryPricingData() error {
	snapshot := loadTKPricingOverlaySnapshot()
	if snapshot == nil || len(snapshot.Models) == 0 {
		return fmt.Errorf("pricing registry is empty")
	}
	data := make(map[string]*LiteLLMModelPricing, len(snapshot.Models))
	for name, pricing := range snapshot.Models {
		if pricing == nil {
			continue
		}
		copy := *pricing
		data[strings.ToLower(strings.TrimSpace(name))] = &copy
	}
	if len(data) == 0 {
		return fmt.Errorf("pricing registry has no usable rows")
	}
	registryHash := sha256.Sum256(tkPricingOverlayRaw)
	s.mu.Lock()
	s.pricingData = data
	s.localHash = hex.EncodeToString(registryHash[:])
	s.lastUpdated = time.Now()
	s.mu.Unlock()
	logger.LegacyPrintf("service.pricing", "[Pricing] Loaded %d models from TK pricing registry", len(data))
	return nil
}

// GetModelPricing 获取模型价格（带模糊匹配）
func (s *PricingService) GetModelPricing(modelName string) *LiteLLMModelPricing {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if modelName == "" {
		return nil
	}

	// 标准化模型名称（同时兼容 "models/xxx"、VertexAI 资源名等前缀）
	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	lookupCandidates := s.buildModelLookupCandidates(modelLower)

	// 1. 精确匹配
	for _, candidate := range lookupCandidates {
		if candidate == "" {
			continue
		}
		if pricing, ok := s.pricingData[candidate]; ok {
			return tkPresentLiteLLMModelPricing(pricing)
		}
	}

	// 2. 处理常见的模型名称变体
	// claude-opus-4-5-20251101 -> claude-opus-4.5-20251101
	for _, candidate := range lookupCandidates {
		normalized := strings.ReplaceAll(candidate, "-4-5-", "-4.5-")
		if pricing, ok := s.pricingData[normalized]; ok {
			return tkPresentLiteLLMModelPricing(pricing)
		}
	}

	// 3. 尝试模糊匹配（去掉版本号后缀）
	// claude-opus-4-5-20251101 -> claude-opus-4.5
	baseName := s.extractBaseName(lookupCandidates[0])
	for key, pricing := range s.pricingData {
		keyBase := s.extractBaseName(strings.ToLower(key))
		if keyBase == baseName {
			return tkPresentLiteLLMModelPricing(pricing)
		}
	}

	// 4. 基于模型系列匹配（Claude）
	if pricing := s.resolveRegistryFamilyAlias(lookupCandidates[0]); pricing != nil {
		return tkPresentLiteLLMModelPricing(pricing)
	}

	// 5. OpenAI registry alias policy
	if strings.HasPrefix(lookupCandidates[0], "gpt-") {
		return tkPresentLiteLLMModelPricing(s.resolveOpenAIRegistryAlias(lookupCandidates[0]))
	}

	// 6. Provider-prefixed registry aliases are a final compatibility lookup. The
	// canonical bare registry owner is preferred above; this path only supports
	// imported provider naming forms and never consults an external snapshot.
	if pricing := s.resolveRegistryProviderAlias(lookupCandidates[0]); pricing != nil {
		return tkPresentLiteLLMModelPricing(pricing)
	}

	return nil
}

// resolveRegistryProviderAlias 用裸模型名匹配 registry 中 "<provider>/.../<model>" 形态的 key
// （按最后一段精确相等，兼容 "gemini/x" 与 "aiml/google/x" 这类多段前缀），命中多个时取最高价。
// 仅扫描含 "/" 的 key（裸 key 已在精确匹配阶段尝试过），避免 alias 误配裸名条目。
func (s *PricingService) resolveRegistryProviderAlias(bareModel string) *LiteLLMModelPricing {
	bareModel = strings.ToLower(strings.TrimSpace(bareModel))
	if bareModel == "" {
		return nil
	}
	var best *LiteLLMModelPricing
	var bestCost float64
	for key, pricing := range s.pricingData {
		if pricing == nil || !strings.Contains(key, "/") {
			continue
		}
		if lastSegment(strings.ToLower(key)) != bareModel {
			continue
		}
		if cost := comparablePricingCost(pricing); best == nil || cost > bestCost {
			best = pricing
			bestCost = cost
		}
	}
	return best
}

// comparablePricingCost 取一个可比单价，仅用于第 6 步兜底里同名多 provider 变体的挑选。
// 此处取最高价是「无法确定实际承接 provider 时的保守猜测」，不是计价语义的主张——主路径
// （TK overlay）已固化按实际承接 provider（Vertex ch41）的价。优先级：每图 > 每秒(视频) > 每输出 token > 每输入 token。
func comparablePricingCost(p *LiteLLMModelPricing) float64 {
	if p == nil {
		return 0
	}
	if p.OutputCostPerImage > 0 {
		return p.OutputCostPerImage
	}
	if p.OutputCostPerSecond > 0 {
		return p.OutputCostPerSecond
	}
	if p.OutputCostPerToken > 0 {
		return p.OutputCostPerToken
	}
	return p.InputCostPerToken
}

func (s *PricingService) buildModelLookupCandidates(modelLower string) []string {
	rawCandidates := []string{
		modelLower,
		strings.TrimPrefix(modelLower, "models/"),
		lastSegment(modelLower),
		lastSegment(strings.TrimPrefix(modelLower, "models/")),
	}
	normalized := normalizeModelNameForPricing(modelLower)

	// A tier-specific entry should take precedence when the pricing catalog gains
	// one later. Today Antigravity's Gemini 3.6 Flash tiers share the base rate,
	// so the normalized base remains the final alias after the exact candidates.
	candidates := rawCandidates
	if normalizeGeminiThinkingTierAlias(lastSegment(modelLower)) != lastSegment(modelLower) {
		candidates = append(candidates, normalized)
	} else {
		// Prefer canonical model names for all other aliases (including models/xxx).
		candidates = append([]string{normalized}, candidates...)
	}

	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	if len(out) == 0 {
		return []string{modelLower}
	}
	return out
}

func normalizeModelNameForPricing(model string) string {
	// Common Gemini/VertexAI forms:
	// - models/gemini-2.0-flash-exp
	// - publishers/google/models/gemini-2.5-pro
	// - projects/.../locations/.../publishers/google/models/gemini-2.5-pro
	model = strings.TrimSpace(model)
	model = strings.TrimLeft(model, "/")
	model = strings.TrimPrefix(model, "models/")
	model = strings.TrimPrefix(model, "publishers/google/models/")

	if idx := strings.LastIndex(model, "/publishers/google/models/"); idx != -1 {
		model = model[idx+len("/publishers/google/models/"):]
	}
	if idx := strings.LastIndex(model, "/models/"); idx != -1 {
		model = model[idx+len("/models/"):]
	}

	model = strings.TrimLeft(model, "/")
	if canonical := canonicalizeOpenAIModelAliasSpelling(model); canonical != "" {
		if canonical == "gpt-5.6" {
			return "gpt-5.6-sol"
		}
		if suffix, ok := strings.CutPrefix(canonical, "gpt-5.6-"); ok && (suffix == "max" || isKnownCodexModelSuffix(suffix)) {
			return "gpt-5.6-sol"
		}
		return canonical
	}
	if canonical := normalizeGLMVolcengineDatedModelID(model); canonical != "" {
		return canonical
	}
	return normalizeGeminiThinkingTierAlias(model)
}

// normalizeGeminiThinkingTierAlias maps Antigravity's Gemini 3.6 Flash
// thinking-tier model IDs to the public base model. The tier controls reasoning
// behavior, not the published token rate, so this keeps -high/-low/-medium and
// -tiered requests on the same price card as gemini-3.6-flash.
func normalizeGeminiThinkingTierAlias(model string) string {
	const baseModel = "gemini-3.6-flash"
	for _, tier := range []string{"-high", "-low", "-medium", "-tiered"} {
		if model == baseModel+tier {
			return baseModel
		}
	}
	return model
}

func lastSegment(model string) string {
	if idx := strings.LastIndex(model, "/"); idx != -1 {
		return model[idx+1:]
	}
	return model
}

// extractBaseName 提取基础模型名称（去掉日期版本号）
func (s *PricingService) extractBaseName(model string) string {
	// 移除日期后缀 (如 -20251101, -20241022)
	parts := strings.Split(model, "-")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		// 跳过看起来像日期的部分（8位数字）
		if len(part) == 8 && isNumeric(part) {
			continue
		}
		// 跳过版本号（如 v1:0）
		if strings.Contains(part, ":") {
			continue
		}
		result = append(result, part)
	}
	return strings.Join(result, "-")
}

// resolveRegistryFamilyAlias 基于模型系列匹配
func (s *PricingService) resolveRegistryFamilyAlias(model string) *LiteLLMModelPricing {
	// modelFamily 定义一个模型系列的匹配和定价查找规则。
	type modelFamily struct {
		name    string   // 系列名称
		match   []string // 用于将模型归类到此系列的模式（strings.Contains 匹配）
		pricing []string // 用于在 registry 中查找 owner 的模式（nil 则复用 match）
	}

	// 按特异性降序排列：高版本号在前，避免 "claude-opus-4"（opus-4 系列）
	// 因子串关系误匹配 "claude-opus-4-7"（opus-4.7 系列）。
	// 注意：原 map 实现存在 Go map 迭代随机性导致的同类 bug，此处改为有序切片修复。
	families := []modelFamily{
		// Opus 5 与 Opus 4.8 同价（$5/$25 per MTok）。定价数据缺失 claude-opus-5 时
		// 必须 alias 到 4.8 owner，否则会掉进 "opus-4" 系列按更高 tier 计费。
		{name: "opus-5", match: []string{"claude-opus-5"}, pricing: []string{"claude-opus-5", "claude-opus-4-8"}},
		{name: "opus-4.8", match: []string{"claude-opus-4-8", "claude-opus-4.8"}, pricing: []string{"claude-opus-4-8", "claude-opus-4.8", "claude-opus-4-7"}},
		{name: "opus-4.7", match: []string{"claude-opus-4-7", "claude-opus-4.7"}, pricing: []string{"claude-opus-4-7", "claude-opus-4.7", "claude-opus-4-6"}},
		{name: "opus-4.6", match: []string{"claude-opus-4-6", "claude-opus-4.6"}},
		{name: "opus-4.5", match: []string{"claude-opus-4-5", "claude-opus-4.5"}},
		{name: "opus-4", match: []string{"claude-opus-4", "claude-3-opus"}},
		{name: "sonnet-4.5", match: []string{"claude-sonnet-4-5", "claude-sonnet-4.5"}},
		{name: "sonnet-4", match: []string{"claude-sonnet-4", "claude-3-5-sonnet"}},
		{name: "sonnet-3.5", match: []string{"claude-3-5-sonnet", "claude-3.5-sonnet"}},
		{name: "sonnet-3", match: []string{"claude-3-sonnet"}},
		{name: "haiku-3.5", match: []string{"claude-3-5-haiku", "claude-3.5-haiku"}},
		{name: "haiku-3", match: []string{"claude-3-haiku"}},
	}

	// Phase 1: 按有序切片归类（最具体的系列优先匹配）
	var matched *modelFamily
	for i := range families {
		for _, pattern := range families[i].match {
			if strings.Contains(model, pattern) || strings.Contains(model, strings.ReplaceAll(pattern, "-", "")) {
				matched = &families[i]
				break
			}
		}
		if matched != nil {
			break
		}
	}

	// Phase 2: alias policy——当模型 ID 不含已知模式串时，按关键字粗分
	if matched == nil {
		var fallbackName string
		switch {
		case strings.Contains(model, "opus"):
			switch {
			// "opus-5" 必须先判：不能用裸 "5" 匹配，否则 claude-opus-4-5 会被误判。
			case strings.Contains(model, "opus-5") || strings.Contains(model, "opus5"):
				fallbackName = "opus-5"
			case strings.Contains(model, "4.8") || strings.Contains(model, "4-8"):
				fallbackName = "opus-4.8"
			case strings.Contains(model, "4.7") || strings.Contains(model, "4-7"):
				fallbackName = "opus-4.7"
			case strings.Contains(model, "4.6") || strings.Contains(model, "4-6"):
				fallbackName = "opus-4.6"
			case strings.Contains(model, "4.5") || strings.Contains(model, "4-5"):
				fallbackName = "opus-4.5"
			default:
				fallbackName = "opus-4"
			}
		case strings.Contains(model, "sonnet"):
			switch {
			case strings.Contains(model, "4.5") || strings.Contains(model, "4-5"):
				fallbackName = "sonnet-4.5"
			case strings.Contains(model, "3-5") || strings.Contains(model, "3.5"):
				fallbackName = "sonnet-3.5"
			default:
				fallbackName = "sonnet-4"
			}
		case strings.Contains(model, "haiku"):
			switch {
			case strings.Contains(model, "3-5") || strings.Contains(model, "3.5"):
				fallbackName = "haiku-3.5"
			default:
				fallbackName = "haiku-3"
			}
		}
		if fallbackName != "" {
			for i := range families {
				if families[i].name == fallbackName {
					matched = &families[i]
					break
				}
			}
		}
	}

	if matched == nil {
		return nil
	}

	// Phase 3: 在定价数据中查找该系列的价格
	lookups := matched.pricing
	if lookups == nil {
		lookups = matched.match
	}
	for _, pattern := range lookups {
		for key, pricing := range s.pricingData {
			keyLower := strings.ToLower(key)
			if strings.Contains(keyLower, pattern) {
				logger.LegacyPrintf("service.pricing", "[Pricing] Fuzzy matched %s -> %s", model, key)
				return pricing
			}
		}
	}

	return nil
}

// resolveOpenAIRegistryAlias resolves an OpenAI model to a registry owner alias.
// Resolution order:
// 1. gpt-5.3-codex-spark* -> gpt-5.3-codex-spark
// 2. gpt-5.2-codex -> gpt-5.2（去掉后缀如 -codex, -mini, -max 等）
// 3. gpt-5.2-20251222 -> gpt-5.2（去掉日期版本号）
// 4. gpt-5.3-codex* / gpt-5-codex -> gpt-5.3-codex-spark
// 5. gpt-5.4* / gpt-5.4-mini* -> the corresponding registry owner
// 6. final compatibility alias to DefaultTestModel, when that owner exists in the registry
func (s *PricingService) resolveOpenAIRegistryAlias(model string) *LiteLLMModelPricing {
	if strings.HasPrefix(model, "gpt-5.3-codex-spark") {
		if pricing, ok := s.pricingData["gpt-5.3-codex-spark"]; ok {
			logger.LegacyPrintf("service.pricing", "[Pricing][SparkBilling] %s -> %s billing", model, "gpt-5.3-codex-spark")
			logger.With(zap.String("component", "service.pricing")).
				Info(fmt.Sprintf("[Pricing] OpenAI registry alias matched %s -> %s", model, "gpt-5.3-codex-spark"))
			return pricing
		}
	}

	// Try registry compatibility variants.
	variants := s.generateOpenAIRegistryAliases(model, openAIModelDatePattern)

	for _, variant := range variants {
		if pricing, ok := s.pricingData[variant]; ok {
			logger.With(zap.String("component", "service.pricing")).
				Info(fmt.Sprintf("[Pricing] OpenAI registry alias matched %s -> %s", model, variant))
			return pricing
		}
	}

	if strings.HasPrefix(model, "gpt-5.3-codex") {
		if pricing, ok := s.pricingData["gpt-5.3-codex-spark"]; ok {
			logger.With(zap.String("component", "service.pricing")).
				Info(fmt.Sprintf("[Pricing] OpenAI registry alias matched %s -> %s", model, "gpt-5.3-codex-spark"))
			return pricing
		}
	}

	if model == "gpt-5-codex" || strings.HasPrefix(model, "gpt-5-codex-") {
		if pricing, ok := s.pricingData["gpt-5.3-codex-spark"]; ok {
			logger.With(zap.String("component", "service.pricing")).
				Info(fmt.Sprintf("[Pricing] OpenAI registry alias matched %s -> %s", model, "gpt-5.3-codex-spark"))
			return pricing
		}
	}

	if strings.HasPrefix(model, "gpt-5.6-sol") {
		logger.With(zap.String("component", "service.pricing")).
			Info(fmt.Sprintf("[Pricing] OpenAI registry alias matched %s -> %s", model, "gpt-5.6-sol"))
		return tkOverlayLiteLLMModelPricing("gpt-5.6-sol")
	}
	if strings.HasPrefix(model, "gpt-5.6-terra") {
		logger.With(zap.String("component", "service.pricing")).
			Info(fmt.Sprintf("[Pricing] OpenAI registry alias matched %s -> %s", model, "gpt-5.6-terra"))
		return tkOverlayLiteLLMModelPricing("gpt-5.6-terra")
	}
	if strings.HasPrefix(model, "gpt-5.6-luna") {
		logger.With(zap.String("component", "service.pricing")).
			Info(fmt.Sprintf("[Pricing] OpenAI registry alias matched %s -> %s", model, "gpt-5.6-luna"))
		return tkOverlayLiteLLMModelPricing("gpt-5.6-luna")
	}

	// GPT-5.5 compatibility aliases to the GPT-5.4 registry owner.
	if strings.HasPrefix(model, "gpt-5.5") {
		logger.With(zap.String("component", "service.pricing")).
			Info(fmt.Sprintf("[Pricing] OpenAI registry alias matched %s -> %s", model, "gpt-5.4"))
		return tkOverlayLiteLLMModelPricing("gpt-5.4")
	}

	if strings.HasPrefix(model, "gpt-5.4-mini") {
		logger.With(zap.String("component", "service.pricing")).
			Info(fmt.Sprintf("[Pricing] OpenAI registry alias matched %s -> %s", model, "gpt-5.4-mini"))
		return tkOverlayLiteLLMModelPricing("gpt-5.4-mini")
	}

	if strings.HasPrefix(model, "gpt-5.4-nano") {
		logger.With(zap.String("component", "service.pricing")).
			Info(fmt.Sprintf("[Pricing] OpenAI registry alias matched %s -> %s", model, "gpt-5.4-nano"))
		return tkOverlayLiteLLMModelPricing("gpt-5.4-nano")
	}

	if strings.HasPrefix(model, "gpt-5.4") {
		logger.With(zap.String("component", "service.pricing")).
			Info(fmt.Sprintf("[Pricing] OpenAI registry alias matched %s -> %s", model, "gpt-5.4"))
		return tkOverlayLiteLLMModelPricing("gpt-5.4")
	}

	if isOpenAIImageGenerationModel(model) {
		for _, candidate := range []string{"gpt-image-2", "gpt-image-1.5", "gpt-image-1"} {
			if pricing, ok := s.pricingData[candidate]; ok {
				logger.LegacyPrintf("service.pricing", "[Pricing] OpenAI image registry alias matched %s -> %s", model, candidate)
				return pricing
			}
		}
		return nil
	}

	// Final compatibility alias to DefaultTestModel, when its registry owner exists.
	defaultModel := strings.ToLower(openai.DefaultTestModel)
	if pricing, ok := s.pricingData[defaultModel]; ok {
		logger.LegacyPrintf("service.pricing", "[Pricing] OpenAI registry alias to default model %s -> %s", model, defaultModel)
		return pricing
	}

	return nil
}

// generateOpenAIRegistryAliases generates OpenAI registry compatibility variants.
func (s *PricingService) generateOpenAIRegistryAliases(model string, datePattern *regexp.Regexp) []string {
	seen := make(map[string]bool)
	var variants []string

	addVariant := func(v string) {
		if v != model && !seen[v] {
			seen[v] = true
			variants = append(variants, v)
		}
	}

	// 1. 去掉日期版本号: gpt-5.2-20251222 -> gpt-5.2
	withoutDate := datePattern.ReplaceAllString(model, "")
	if withoutDate != model {
		addVariant(withoutDate)
	}

	// 2. 提取基础版本号: gpt-5.2-codex -> gpt-5.2
	// 只匹配纯数字版本号格式 gpt-X 或 gpt-X.Y，不匹配 gpt-4o 这种带字母后缀的
	if matches := openAIModelBasePattern.FindStringSubmatch(model); len(matches) > 1 {
		addVariant(matches[1])
	}

	// 3. 同时去掉日期后再提取基础版本号
	if withoutDate != model {
		if matches := openAIModelBasePattern.FindStringSubmatch(withoutDate); len(matches) > 1 {
			addVariant(matches[1])
		}
	}

	return variants
}

// GetStatus 获取服务状态
func (s *PricingService) GetStatus() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]any{
		"model_count":  len(s.pricingData),
		"last_updated": s.lastUpdated,
		"local_hash":   s.localHash[:min(8, len(s.localHash))],
	}
}

// ForceUpdate 强制更新
func (s *PricingService) ForceUpdate() error {
	if s == nil {
		return fmt.Errorf("pricing service is nil")
	}
	return s.loadRegistryPricingData()
}

// ListModelNamesByProvider returns all model names in the catalog whose
// LiteLLMProvider matches the given provider string (case-insensitive).
// The returned slice is sorted alphabetically.
func (s *PricingService) ListModelNamesByProvider(provider string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	provider = strings.ToLower(strings.TrimSpace(provider))
	names := make([]string, 0)
	for name, p := range s.pricingData {
		if strings.ToLower(p.LiteLLMProvider) == provider {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// isNumeric 检查字符串是否为纯数字
func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
