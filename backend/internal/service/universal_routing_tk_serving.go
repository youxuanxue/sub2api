package service

import (
	"context"
	"strings"
	"sync"
)

// Compatibility model-support providers and capability-discovery helpers.
// Production Universal selection uses evaluateGroupCandidates, which separates
// support from readiness and propagates unknown evidence. These adapters share
// candidateSupportsRequest for admission but retain their callers' legacy
// snapshot/fallback semantics; they are not the current selection owner.

// availableModelsProvider 返回某组(按 platform)当前可调度账号 model_mapping 键的并集;
// 无任何账号声明映射时返回 nil(native/permissive)。由 GatewayService.GetAvailableModels
// 满足，仅用于未接 candidateEvaluator 的兼容路径。经 APIKeyService 后期绑定
// (避免构造期环;见 wire_tk.go ProvideTKUniversalModelsProvider)。
type availableModelsProvider func(ctx context.Context, groupID *int64, platform string) []string

// groupModelSupportProvider 以 direct scheduler 的账号级语义判定某组是否能服务模型+协议形态。
// known=false 表示取数失败/未能判断；仅兼容路径可退回 availableModelsProvider。
type groupModelSupportProvider func(ctx context.Context, groupID *int64, platform, model string, shape UniversalShape) (serves bool, known bool)

// UniversalGroupSupportsModel preserves the old model-only contract for tests and
// degraded callers. Production selection uses evaluateGroupCandidates.
func (s *GatewayService) UniversalGroupSupportsModel(ctx context.Context, groupID *int64, platform, model string) (bool, bool) {
	return s.UniversalGroupSupportsRequest(ctx, groupID, platform, model, ShapeSkip)
}

// UniversalGroupSupportsRequest reports whether the direct gateway scheduler could
// find at least one account in group/platform that supports model. This is the
// compatibility hook: it preserves per-account semantics that a group-level
// served-model union loses, especially unrestricted passthrough accounts, wildcard
// mappings, Anthropic short-id normalization, Antigravity default mappings, and
// OpenAI alias spelling. It also includes the endpoint shape's account capability
// gates so a group that can "name-match" a model but cannot serve that protocol
// is rejected by callers that lack the full candidate evaluator.
func (s *GatewayService) UniversalGroupSupportsRequest(ctx context.Context, groupID *int64, platform, model string, shape UniversalShape) (bool, bool) {
	if s == nil || s.accountRepo == nil || platform == "" {
		return false, false
	}
	accounts, useMixed, err := s.listSchedulableAccountsForModel(ctx, groupID, platform, false, model)
	if err != nil {
		return false, false
	}
	if shape == ShapeGemini {
		ready := s.gatewayCandidates(ctx, accounts, platform, useMixed, model, nil)
		accounts = make([]Account, 0, len(ready))
		for _, account := range ready {
			accounts = append(accounts, *account)
		}
	}
	// Native Gemini has multiple wire-compatible Google backends. Its resolver
	// therefore needs current scheduler reachability so an error-only native pool
	// cannot mask a healthy Vertex group through the Gemini platform hint. Other
	// shapes retain error-account entitlement to prevent semantic cross-vendor
	// misrouting (for example Qianfan-owned DeepSeek models).
	return s.universalAccountsSupportRequest(
		ctx,
		s.accountsForUniversalRequestEntitlement(ctx, groupID, platform, shape, accounts),
		useMixed,
		platform,
		model,
		shape,
	), true
}

// UniversalGroupSupportsRequestStrict is the discovery counterpart of the
// resolver's fail-open provider. Discovery must surface repository failures
// instead of turning them into a successful empty menu.
func (s *GatewayService) UniversalGroupSupportsRequestStrict(ctx context.Context, groupID int64, platform, model string, shape UniversalShape) (bool, error) {
	if s == nil || s.accountRepo == nil || platform == "" {
		return false, ErrUniversalCapabilityUnavailable
	}
	accounts, useMixed, err := s.universalCapabilityAccounts(ctx, groupID, platform)
	if err != nil {
		return false, err
	}
	accounts = s.accountsForUniversalRequestEntitlement(ctx, &groupID, platform, shape, accounts)
	return s.universalAccountsSupportRequest(ctx, accounts, useMixed, platform, model, shape), nil
}

type universalCapabilityAccountCacheContextKey struct{}

type universalCapabilityAccountCacheKey struct {
	groupID  int64
	platform string
}

type universalCapabilityAccountCacheEntry struct {
	accounts []Account
	useMixed bool
	err      error
}

// Capability discovery evaluates many model/shape pairs against the same group.
// Keep one scheduler-filtered account snapshot per group for this request only.
type universalCapabilityAccountCache struct {
	mu      sync.Mutex
	entries map[universalCapabilityAccountCacheKey]universalCapabilityAccountCacheEntry
}

func withUniversalCapabilityAccountCache(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(universalCapabilityAccountCacheContextKey{}).(*universalCapabilityAccountCache); ok {
		return ctx
	}
	return context.WithValue(ctx, universalCapabilityAccountCacheContextKey{}, &universalCapabilityAccountCache{
		entries: make(map[universalCapabilityAccountCacheKey]universalCapabilityAccountCacheEntry),
	})
}

func (s *GatewayService) universalCapabilityAccounts(ctx context.Context, groupID int64, platform string) ([]Account, bool, error) {
	cache, _ := ctx.Value(universalCapabilityAccountCacheContextKey{}).(*universalCapabilityAccountCache)
	if cache == nil {
		return s.listSchedulableAccounts(ctx, &groupID, platform, false)
	}

	key := universalCapabilityAccountCacheKey{groupID: groupID, platform: platform}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cached, ok := cache.entries[key]; ok {
		return cached.accounts, cached.useMixed, cached.err
	}
	accounts, useMixed, err := s.listSchedulableAccounts(ctx, &groupID, platform, false)
	cache.entries[key] = universalCapabilityAccountCacheEntry{
		accounts: accounts,
		useMixed: useMixed,
		err:      err,
	}
	return accounts, useMixed, err
}

func (s *GatewayService) accountsForUniversalRequestEntitlement(
	ctx context.Context,
	groupID *int64,
	platform string,
	shape UniversalShape,
	accounts []Account,
) []Account {
	if shape == ShapeGemini {
		return accounts
	}
	return append(accounts, s.listErrorAccountsForUniversalEntitlement(ctx, groupID, platform)...)
}

func (s *GatewayService) listErrorAccountsForUniversalEntitlement(ctx context.Context, groupID *int64, platform string) []Account {
	if s == nil || s.accountRepo == nil || groupID == nil || *groupID <= 0 || strings.TrimSpace(platform) == "" {
		return nil
	}
	accounts, err := s.accountRepo.ListAllWithFilters(ctx, platform, "", StatusError, "", *groupID, "", 0)
	if err != nil || len(accounts) == 0 {
		return nil
	}
	return accounts
}

func (s *GatewayService) universalAccountsSupportRequest(ctx context.Context, accounts []Account, useMixed bool, platform, model string, shape UniversalShape) bool {
	for i := range accounts {
		if supported, err := s.candidateSupportsRequest(ctx, &accounts[i], platform, useMixed, model, shape); err == nil && supported {
			return true
		}
	}
	return false
}

func universalOpenAICompatAccountSupportsModel(ctx context.Context, s *GatewayService, account *Account, model string, shape UniversalShape) bool {
	if account == nil {
		return false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return true
	}
	mapping := account.GetModelMapping()
	if len(mapping) > 0 {
		if mappingSupportsRequestedModel(mapping, model) {
			return universalOpenAICompatMappingHonorsPlatformHint(account, model, shape)
		}
		if norm := normalizeRequestedModelForLookup(account.Platform, model); norm != model {
			if mappingSupportsRequestedModel(mapping, norm) {
				return universalOpenAICompatMappingHonorsPlatformHint(account, model, shape)
			}
		}
		return account.Platform == PlatformGrok && grokGroupServesNativeCatalogModel(model)
	}
	if account.Platform == PlatformGrok && grokGroupServesNativeCatalogModel(model) {
		return true
	}
	if account.Platform == PlatformNewAPI {
		return false
	}
	if s != nil && s.isModelSupportedByAccountWithContext(ctx, account, model) {
		if hint := universalRequestPlatformHint(shape, model); hint != "" {
			return hint == account.Platform
		}
		return true
	}
	return false
}

func universalOpenAICompatMappingHonorsPlatformHint(account *Account, model string, shape UniversalShape) bool {
	if account == nil {
		return false
	}
	hint := universalRequestPlatformHint(shape, model)
	if hint == "" || hint == account.Platform {
		return true
	}
	// Listing-only OpenAI relays (tokensea / ainzy / official GPT 专线 leftovers)
	// must not steal curated newapi vendor models. CloudWise is the exception:
	// its openai apikey floor is explicitly glm-*/deepseek-*/kimi-*/minimax-*/hy3*.
	if account.Platform == PlatformOpenAI && hint == PlatformNewAPI {
		return isCloudwiseRelayAccount(account)
	}
	return true
}

func universalOpenAICompatAccountSupportsShape(account *Account, shape UniversalShape) bool {
	switch shape {
	case ShapeGemini:
		return account.IsNewAPIVertexServiceAccount()
	case ShapeOpenAIEmbeddings:
		return accountSupportsOpenAIRequestCapabilities(account, OpenAIEndpointCapabilityEmbeddings, "", false)
	case ShapeOpenAIImages, ShapeOpenAIImagesEdit:
		return accountSupportsOpenAIRequestCapabilities(account, "", OpenAIImagesCapabilityBasic, false)
	case ShapeOpenAIVideo:
		return accountSupportsOpenAIRequestCapabilities(account, "", "", true)
	default:
		return true
	}
}

// groupServesModel 是 GetAvailableModels fallback 的组服务集判定(上述三分流)。
// provider 非 nil 由调用方保证。
func groupServesModel(ctx context.Context, provider availableModelsProvider, g Group, model string) bool {
	gid := g.ID
	served := provider(ctx, &gid, g.Platform)
	if served != nil {
		// 组有显式 model_mapping → 精确成员判定。
		if modelInServedSet(model, served, g.Platform) {
			return true
		}
		// Grok native OAuth: chat-only mapping entries must not hide the curated
		// grok-imagine media + probed chat catalog that fillAccountFallback advertises.
		if g.Platform == PlatformGrok && grokGroupServesNativeCatalogModel(model) {
			return true
		}
		return false
	}
	// served == nil:native/permissive(无账号声明映射,或 provider 取数失败)。
	if g.Platform == PlatformNewAPI {
		// 多 vendor 平台的空映射 = 配置缺失,不靠 hint 撞(防 openai 名/其它 vendor 误投)。
		return false
	}
	// 单 vendor 原生平台:模型平台 hint == 组平台。
	return nativePlatformHintMatchesGroup(g.Platform, model)
}

func nativePlatformHintMatchesGroup(platform, model string) bool {
	hint := universalModelPlatformHint(model)
	if hint == platform {
		return true
	}
	// Kiro exposes Claude-family models through the Anthropic Messages shape, but
	// keeps an isolated platform/pool. A provider fallback must not reject the
	// only Kiro group for a claude-* model just because the broad model hint is
	// "anthropic".
	return platform == PlatformKiro && hint == PlatformAnthropic
}

// modelInServedSet 判定 model 是否在组的显式服务集里(精确 + 归一,与 IsModelSupported 同口径)。
func modelInServedSet(model string, served []string, platform string) bool {
	mapping := make(map[string]string, len(served))
	for _, m := range served {
		mapping[m] = m
	}
	if mappingSupportsRequestedModel(mapping, model) {
		return true
	}
	if norm := normalizeRequestedModelForLookup(platform, model); norm != model {
		return mappingSupportsRequestedModel(mapping, norm)
	}
	return false
}
