package service

// TokenKey: per-user pricing catalog ("Group Catalog" / "分组目录").
//
// Unlike service.PricingCatalogService.BuildPublicCatalog (a platform-wide
// flat list of LiteLLM list prices), MePricingCatalogService builds a view
// scoped to ONE group — the group of the user's selected API key, or any
// other group the user can access ("explore other group" mode).
//
// TK pricing-display policy (运营要求，展示端与倍率彻底脱钩): the prices
// emitted here are the OFFICIAL list prices (multiplier 1.0) — they do NOT
// apply `group.rate_multiplier` nor the `user_group_rate` override. The
// pricing page ("分组目录"/"所有目录") is a catalog of official prices; a
// customer's real cost depends on their (hidden) rate. The billing path is
// unaffected: the gateway still charges the true effective rate. `your_price`
// keeps its field name for DTO stability but now carries the official price.
//
// Why a separate service / why not extend ChannelService.ListAvailable:
//   - This is a TK-only surface; ChannelService is upstream-shaped and we
//     keep its signature stable per CLAUDE.md §5.
//   - The cross-platform anti-leak rule and the per-group narrowing pattern
//     are already proven in `available_channel_handler.go:151-156, 230-249`;
//     we replicate that discipline here rather than coupling ChannelService
//     to user-scope concerns.
//   - LiteLLM metadata (context_window, capabilities) is joined post-hoc
//     from PricingCatalogService — the channel layer doesn't carry those.
//
// Effective rate (user_group_rate override else group.rate_multiplier) is
// still computed for the target_group rate hint fields (see #693's
// HideUserRateOverrides), but it is NO LONGER applied to model prices.
//
// Multi-channel dedupe rule: when the same model_id appears on multiple
// active channels mapped to the target group, we keep the row with the
// LOWEST combined input+output (official) price — the headline price equals
// the cheapest catalog path for that model.

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// MePricingKeyAccess is the slice of *APIKeyService that BuildForUser
// needs. Defined as an interface so unit tests can inject fakes without
// constructing a full APIKeyService.
type MePricingKeyAccess interface {
	GetAvailableGroups(ctx context.Context, userID int64) ([]Group, error)
	GetUserGroupRates(ctx context.Context, userID int64) (map[int64]float64, error)
	List(ctx context.Context, userID int64, params pagination.PaginationParams, filters APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error)
}

// MePricingChannelLister is the slice of *ChannelService that BuildForUser
// needs.
type MePricingChannelLister interface {
	ListAvailable(ctx context.Context) ([]AvailableChannel, error)
}

// MePricingCatalogProvider is the slice of *PricingCatalogService that
// BuildForUser needs.
type MePricingCatalogProvider interface {
	BuildPublicCatalog(ctx context.Context) *PublicCatalogResponse
}

// MePricingAccountSource is the slice of *AccountService that BuildForUser
// needs for the account fallback. Defined as an interface so unit tests can
// inject fakes without constructing a full AccountService.
//
// The fallback (see fillAccountFallback) turns each schedulable account into
// menu rows when no channel-configured pricing covers the target group:
// a restricted account contributes its `credentials.model_mapping` whitelist
// entries (from === to), bridging the admin "model whitelist" UX into "Your
// Menu" (TK incident 2026-05-21); an unrestricted native OAuth account
// (empty model_mapping) contributes the platform's canonical model list,
// which is what fixes the empty menu for Anthropic OAuth groups.
type MePricingAccountSource interface {
	ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]Account, error)
}

// MePricingCatalogService builds the per-user pricing-catalog DTO.
type MePricingCatalogService struct {
	keys     MePricingKeyAccess
	channels MePricingChannelLister
	catalog  MePricingCatalogProvider
	accounts MePricingAccountSource
	// availability gates the unrestricted-account fallback on live
	// model_availability so a structurally-gone model (us7 P0 2026-06-13)
	// auto-drops from the menu without a manual servable-allowlist edit. Nil
	// (Phase-1 / tests) degrades to "no gating" — the static allowlist alone.
	availability MePricingAvailability
}

// MePricingAvailability is the read slice of PricingAvailabilityService the
// menu needs: the per-(platform, model) verified-availability state.
type MePricingAvailability interface {
	GetAvailability(ctx context.Context, platform, modelID string) (AvailabilityState, error)
}

// NewMePricingCatalogService is the production constructor. Any nil
// dependency degrades to a sensible empty result rather than panicking,
// because the read path is exercised early by user-facing UI and may race
// with startup wiring.
func NewMePricingCatalogService(
	keys *APIKeyService,
	channels *ChannelService,
	catalog *PricingCatalogService,
	accounts *AccountService,
	availability *PricingAvailabilityService,
) *MePricingCatalogService {
	var (
		k     MePricingKeyAccess
		c     MePricingChannelLister
		p     MePricingCatalogProvider
		a     MePricingAccountSource
		avail MePricingAvailability
	)
	if keys != nil {
		k = keys
	}
	if channels != nil {
		c = channels
	}
	if catalog != nil {
		p = catalog
	}
	if accounts != nil {
		a = accounts
	}
	if availability != nil {
		avail = availability
	}
	return &MePricingCatalogService{keys: k, channels: c, catalog: p, accounts: a, availability: avail}
}

// pruneStructurallyGoneIDs drops model IDs that live model_availability reports
// as structurally gone upstream (model_not_found → unreachable). Delegates to the
// shared tkPruneStructurallyGoneIDs so the per-user menu and the admin selector
// prune identically. Nil-safe.
func (s *MePricingCatalogService) pruneStructurallyGoneIDs(ctx context.Context, platform string, ids []string) []string {
	if s == nil {
		return ids
	}
	return tkPruneStructurallyGoneIDs(ctx, platform, ids, s.availability)
}

// MePricingCatalogOptions selects which group the menu is built for.
// APIKeyID and GroupID are mutually-exclusive selectors; when both are
// nil the service defaults to the user's first active API-key's group,
// falling back to the first accessible group.
type MePricingCatalogOptions struct {
	APIKeyID *int64
	GroupID  *int64
	// HideUserRateOverrides 隐藏 per-user 专属倍率的"数值痕迹"：倍率提示
	// 字段（rate_multiplier / has_override / accessible_groups 的倍率）回落
	// 到分组默认值，但 models 的 your_price 仍按真实生效倍率计算——价格
	// 必须与实际计费一致。非 admin 请求由 handler 置位。
	HideUserRateOverrides bool
}

// MePricingCatalogResponse is the top-level DTO returned to the user UI.
type MePricingCatalogResponse struct {
	TargetGroup      MePricingTargetGroup `json:"target_group"`
	Models           []MePricingModel     `json:"models"`
	MyKeys           []MePricingKeyRef    `json:"my_keys"`
	AccessibleGroups []MePricingGroupRef  `json:"accessible_groups"`
	UpdatedAt        time.Time            `json:"updated_at"`
}

// MePricingTargetGroup describes the group the menu is currently scoped to.
//
// RateMultiplier is the EFFECTIVE multiplier (group default × per-user
// override). ListMultiplier is the group default — the UI uses the delta
// to render the "包含个人覆写" hint.
type MePricingTargetGroup struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	Platform         string  `json:"platform"`
	RateMultiplier   float64 `json:"rate_multiplier"`
	ListMultiplier   float64 `json:"list_multiplier"`
	HasOverride      bool    `json:"has_override"`
	IsExclusive      bool    `json:"is_exclusive"`
	SubscriptionType string  `json:"subscription_type"`
}

// MePricingModel is one row in the user menu.
type MePricingModel struct {
	ModelID         string         `json:"model_id"`
	Vendor          string         `json:"vendor,omitempty"`
	BillingMode     string         `json:"billing_mode"`
	YourPrice       MePricingPrice `json:"your_price"`
	ContextWindow   int            `json:"context_window,omitempty"`
	MaxOutputTokens int            `json:"max_output_tokens,omitempty"`
	Capabilities    []string       `json:"capabilities"`
}

// MePricingPrice mirrors the "your price" view in USD per 1k tokens (or
// per-request for non-token modes). Nil pointers signal "no price data";
// 0.0 is a real value (free subscription).
type MePricingPrice struct {
	Currency         string   `json:"currency"`
	InputPer1K       *float64 `json:"input_per_1k,omitempty"`
	OutputPer1K      *float64 `json:"output_per_1k,omitempty"`
	CacheReadPer1K   *float64 `json:"cache_read_per_1k,omitempty"`
	CacheWritePer1K  *float64 `json:"cache_write_per_1k,omitempty"`
	ImageOutputPer1K *float64 `json:"image_output_per_1k,omitempty"`
	PerRequest       *float64 `json:"per_request,omitempty"`
	// TK media units: per-generated-image (image billing_mode) and per-second
	// (video billing_mode), scaled by the user's effective rate. Carried from
	// the public catalog meta (which now surfaces media — pricing_catalog_tk.go).
	PerImage  *float64 `json:"per_image,omitempty"`
	PerSecond *float64 `json:"per_second,omitempty"`
}

// MePricingKeyRef populates the key-picker dropdown. Only active keys
// whose group is in the user's accessible set are listed.
type MePricingKeyRef struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	GroupID   int64  `json:"group_id"`
	GroupName string `json:"group_name"`
}

// MePricingGroupRef populates the "explore other group" dropdown. The
// IsCurrentForKey flag lets the UI mark the row that matches the user's
// selected key without an extra lookup.
type MePricingGroupRef struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	Platform         string  `json:"platform"`
	RateMultiplier   float64 `json:"rate_multiplier"`
	IsCurrentForKey  bool    `json:"is_current_for_key"`
	IsExclusive      bool    `json:"is_exclusive"`
	SubscriptionType string  `json:"subscription_type"`
}

// Sentinel errors. Handler maps these to HTTP codes.
var (
	// ErrMePricingNoAccessibleGroups means the user has no group they can
	// bind a key to (no subscription, not on any allowed-user list, all
	// groups exclusive). Handler renders this as a 200 with empty models[]
	// and a friendly UI banner, not an error.
	ErrMePricingNoAccessibleGroups = errors.New("me_pricing: user has no accessible groups")
	// ErrMePricingAPIKeyNotFound means the api_key_id query param does not
	// refer to a key owned by this user (or the key has no group binding).
	ErrMePricingAPIKeyNotFound = errors.New("me_pricing: api key not found or not bound to a group")
	// ErrMePricingGroupForbidden means the group_id query param is outside
	// the user's accessible set.
	ErrMePricingGroupForbidden = errors.New("me_pricing: group not accessible")
	// ErrMePricingConflictingTargets means api_key_id and group_id were
	// both provided AND refer to different groups.
	ErrMePricingConflictingTargets = errors.New("me_pricing: api_key_id and group_id refer to different groups")
)

// keyListPageSize is the soft upper bound on per-user keys we walk. Users
// with > this many keys see only the first page in the picker; in
// practice TokenKey users hold single-digit keys so this is generous.
const keyListPageSize = 200

// BuildForUser produces the per-user catalog DTO.
//
// Algorithm (mirrors plan in /Users/xuejiao/.claude/plans/bubbly-bouncing-sunbeam.md §"数据组装算法"):
//  1. Load accessibleGroups, userKeys (active only), userRates.
//  2. Resolve targetGroupID from opts.APIKeyID > opts.GroupID > default.
//  3. Compute effectiveRate = userRates[gid] when present else group.RateMultiplier.
//  4. Walk channels, filter to active + mapped to target group, filter
//     SupportedModels.Platform == targetGroup.Platform (cross-platform leak guard).
//  5. Dedupe by model_id, keep the cheapest (input + output sum) row.
//  6. Emit OFFICIAL prices (multiplier 1.0) — effectiveRate is computed but
//     NOT applied to model prices (TK pricing-display policy; see file header).
//  7. Join LiteLLM metadata for capabilities / context_window / max_output.
//  8. Sort alpha by model_id.
func (s *MePricingCatalogService) BuildForUser(
	ctx context.Context,
	userID int64,
	opts MePricingCatalogOptions,
) (*MePricingCatalogResponse, error) {
	if s == nil || s.keys == nil {
		return nil, ErrMePricingNoAccessibleGroups
	}

	accessibleGroups, err := s.keys.GetAvailableGroups(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(accessibleGroups) == 0 {
		return nil, ErrMePricingNoAccessibleGroups
	}
	accessByID := make(map[int64]Group, len(accessibleGroups))
	for i := range accessibleGroups {
		g := accessibleGroups[i]
		accessByID[g.ID] = g
	}

	userRates, err := s.keys.GetUserGroupRates(ctx, userID)
	if err != nil {
		return nil, err
	}

	keysAll, _, err := s.keys.List(ctx, userID,
		pagination.PaginationParams{Page: 1, PageSize: keyListPageSize},
		APIKeyListFilters{Status: StatusAPIKeyActive},
	)
	if err != nil {
		return nil, err
	}

	myKeys := make([]MePricingKeyRef, 0, len(keysAll))
	for i := range keysAll {
		k := keysAll[i]
		if k.GroupID == nil {
			continue
		}
		g, ok := accessByID[*k.GroupID]
		if !ok {
			continue
		}
		myKeys = append(myKeys, MePricingKeyRef{
			ID: k.ID, Name: k.Name, GroupID: g.ID, GroupName: g.Name,
		})
	}

	targetGroupID, err := resolveTargetGroupID(opts, keysAll, accessByID, myKeys, accessibleGroups)
	if err != nil {
		return nil, err
	}
	targetGroup := accessByID[targetGroupID]

	listMult := targetGroup.RateMultiplier
	effective := listMult
	hasOverride := false
	if r, ok := userRates[targetGroupID]; ok {
		effective = r
		hasOverride = r != listMult
	}

	// TK: 模型价格一律按官方基础价（倍率 1.0）计算，不乘 effective/override。
	// pricing 页是官方定价目录；真实计费在网关按 effective 倍率执行，不受此影响。
	const officialRate = 1.0
	models := s.buildModelsForGroup(ctx, targetGroup, officialRate)

	// HideUserRateOverrides：倍率提示字段回落到分组默认值（前端已不再渲染这些
	// 字段——pricing 页与倍率彻底脱钩——但保留 #693 的口径以稳住 DTO 与测试）。
	shownRate := effective
	shownOverride := hasOverride
	if opts.HideUserRateOverrides {
		shownRate = listMult
		shownOverride = false
	}

	// Build picker DTO for accessible_groups with effective rate per group.
	groupRefs := make([]MePricingGroupRef, 0, len(accessibleGroups))
	for i := range accessibleGroups {
		g := accessibleGroups[i]
		rate := g.RateMultiplier
		if r, ok := userRates[g.ID]; ok && !opts.HideUserRateOverrides {
			rate = r
		}
		groupRefs = append(groupRefs, MePricingGroupRef{
			ID:               g.ID,
			Name:             g.Name,
			Platform:         g.Platform,
			RateMultiplier:   rate,
			IsCurrentForKey:  g.ID == targetGroupID,
			IsExclusive:      g.IsExclusive,
			SubscriptionType: g.SubscriptionType,
		})
	}

	return &MePricingCatalogResponse{
		TargetGroup: MePricingTargetGroup{
			ID:               targetGroup.ID,
			Name:             targetGroup.Name,
			Platform:         targetGroup.Platform,
			RateMultiplier:   shownRate,
			ListMultiplier:   listMult,
			HasOverride:      shownOverride,
			IsExclusive:      targetGroup.IsExclusive,
			SubscriptionType: targetGroup.SubscriptionType,
		},
		Models:           models,
		MyKeys:           myKeys,
		AccessibleGroups: groupRefs,
		UpdatedAt:        time.Now().UTC(),
	}, nil
}

// resolveTargetGroupID applies the selector precedence: explicit api_key_id
// wins; explicit group_id second; otherwise default to the first user key's
// group, then the first accessible group.
func resolveTargetGroupID(
	opts MePricingCatalogOptions,
	keysAll []APIKey,
	accessByID map[int64]Group,
	myKeys []MePricingKeyRef,
	accessibleGroups []Group,
) (int64, error) {
	switch {
	case opts.APIKeyID != nil:
		for i := range keysAll {
			k := keysAll[i]
			if k.ID != *opts.APIKeyID {
				continue
			}
			if k.GroupID == nil {
				return 0, ErrMePricingAPIKeyNotFound
			}
			if _, ok := accessByID[*k.GroupID]; !ok {
				return 0, ErrMePricingAPIKeyNotFound
			}
			if opts.GroupID != nil && *opts.GroupID != *k.GroupID {
				return 0, ErrMePricingConflictingTargets
			}
			return *k.GroupID, nil
		}
		return 0, ErrMePricingAPIKeyNotFound
	case opts.GroupID != nil:
		if _, ok := accessByID[*opts.GroupID]; !ok {
			return 0, ErrMePricingGroupForbidden
		}
		return *opts.GroupID, nil
	case len(myKeys) > 0:
		return myKeys[0].GroupID, nil
	default:
		return accessibleGroups[0].ID, nil
	}
}

// buildModelsForGroup performs steps 4-8 of the algorithm. Returns an
// empty (non-nil) slice when no models exist; UI relies on this for the
// "no models published" empty state.
//
// The build proceeds in three stages:
//
//  1. Channel pricing — walk active channels mapped to the target group,
//     keep platform-matching SupportedModels, dedupe by model_id with the
//     cheaper-wins rule.
//  2. Account fallback — for each schedulable account on the target
//     group, synthesize rows the channel loop did not already cover.
//     Restricted accounts contribute their model_mapping whitelist;
//     unrestricted native OAuth accounts (empty model_mapping) contribute
//     the platform's canonical model list. Channel pricing always wins on
//     conflict. This is what populates Your-Menu for Anthropic OAuth
//     groups, which have no channels and no whitelist. See
//     fillAccountFallback for the branch detail.
//  3. LiteLLM metadata join — vendor / context_window / capabilities /
//     max_output_tokens applied to every row, regardless of source.
func (s *MePricingCatalogService) buildModelsForGroup(
	ctx context.Context,
	targetGroup Group,
	effectiveRate float64,
) []MePricingModel {
	out := []MePricingModel{}

	// Build LiteLLM catalog index up-front: the channel metadata join and
	// the account-whitelist fallback both consume it, so we read once.
	metaByID := map[string]PublicCatalogModel{}
	if s.catalog != nil {
		if resp := s.catalog.BuildPublicCatalog(ctx); resp != nil {
			for _, m := range resp.Data {
				metaByID[m.ModelID] = m
			}
		}
	}

	bestByModel := make(map[string]MePricingModel)

	// Stage 1: channel pricing.
	if s.channels != nil {
		channels, err := s.channels.ListAvailable(ctx)
		if err == nil {
			for _, ch := range channels {
				if ch.Status != StatusActive {
					continue
				}
				mapped := false
				for _, g := range ch.Groups {
					if g.ID == targetGroup.ID {
						mapped = true
						break
					}
				}
				if !mapped {
					continue
				}
				for i := range ch.SupportedModels {
					m := ch.SupportedModels[i]
					// Cross-platform leak guard — a channel can sit on groups
					// from multiple platforms; we restrict to models declared
					// on the target group's platform.
					if m.Platform != targetGroup.Platform {
						continue
					}
					candidate := buildModelEntry(m, effectiveRate)
					if existing, ok := bestByModel[m.Name]; ok {
						bestByModel[m.Name] = pickCheaperModel(existing, candidate)
					} else {
						bestByModel[m.Name] = candidate
					}
				}
			}
		}
	}

	// Stage 2: account-whitelist fallback (channel-priced rows are
	// authoritative; this only fills gaps).
	s.fillAccountFallback(ctx, targetGroup, effectiveRate, bestByModel, metaByID)

	// Stage 3: LiteLLM metadata join — applied uniformly to all rows.
	for _, m := range bestByModel {
		if meta, ok := metaByID[m.ModelID]; ok {
			m.ContextWindow = meta.ContextWindow
			m.MaxOutputTokens = meta.MaxOutputTokens
			if len(meta.Capabilities) > 0 {
				m.Capabilities = append([]string{}, meta.Capabilities...)
			}
			if m.Vendor == "" {
				m.Vendor = meta.Vendor
			}
		}
		if m.Capabilities == nil {
			m.Capabilities = []string{}
		}
		out = append(out, m)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ModelID < out[j].ModelID })
	return out
}

// fillAccountFallback inserts account-derived menu rows for each
// schedulable account on the target group, branching on whether the
// account restricts its model set:
//
//   - Restricted account (non-empty credentials.model_mapping) — emit one
//     row per whitelist entry (from === to). Mapping-mode entries
//     (from != to) are routing rewrites, not user-visible offerings, so
//     they contribute nothing (see parseWhitelistFromCredentials).
//   - Unrestricted account (empty/absent model_mapping = all models
//     allowed, the native OAuth case) — emit the platform's canonical
//     model list (claude/openai/gemini/antigravity default models, the
//     same source gateway `/v1/models` uses). This is what fixes the
//     empty "Group Catalog" for Anthropic OAuth groups: those accounts are
//     not channels and carry no whitelist, so before this branch both
//     the channel stage and the whitelist stage produced nothing.
//
// Channel-configured rows ALWAYS win on conflict — we never overwrite an
// existing key. The catalog index is passed in so the price/metadata
// lookup uses the same source the metadata-join stage does, keeping
// catalog reads to one round-trip per call.
//
// When the catalog has no entry for a model_id, vendor-prefix stripping
// (matches OpenRouter / Azure "<vendor>/<family-version>" style — same
// predicate as IsModelPriced § PR #326) is retried before giving up. If
// both lookups miss, the row is still emitted with nil prices: reachable
// coverage takes priority over hiding unpriced models. MePricingPrice's
// contract treats nil as "—" in the UI, distinct from a real 0.0
// free-subscription price.
//
// An empty schedulable pool emits nothing — the menu stays empty rather
// than advertising models a dead group cannot actually serve.
//
// Errors from the account source are absorbed: the fallback is
// best-effort and must not block the main pricing-catalog response.
func (s *MePricingCatalogService) fillAccountFallback(
	ctx context.Context,
	targetGroup Group,
	effectiveRate float64,
	bestByModel map[string]MePricingModel,
	metaByID map[string]PublicCatalogModel,
) {
	if s.accounts == nil {
		return
	}
	accounts, err := s.accounts.ListSchedulableByGroupID(ctx, targetGroup.ID)
	if err != nil || len(accounts) == 0 {
		return
	}
	// Self-heal (us7 P0 2026-06-13): drop models live model_availability reports
	// as structurally gone (model_not_found → unreachable) so an upstream-retired
	// model auto-disappears from the menu without a manual servable-allowlist
	// edit — same gone-vs-degraded rule as the public /pricing storefront.
	platformDefaults := s.pruneStructurallyGoneIDs(ctx, targetGroup.Platform, platformDefaultModelIDs(targetGroup.Platform))
	for i := range accounts {
		a := &accounts[i]
		if !accountInGroupScope(a, targetGroup.Platform) {
			continue
		}
		if accountHasModelRestriction(a.Credentials) {
			for _, modelID := range parseWhitelistFromCredentials(a.Credentials) {
				addFallbackModel(bestByModel, modelID, effectiveRate, metaByID)
			}
			continue
		}
		// Unrestricted native account → canonical platform model list.
		for _, modelID := range platformDefaults {
			if targetGroup.Platform == PlatformAnthropic {
				if _, deprecated := tkIsDeprecatedAnthropicModel(modelID); deprecated {
					continue
				}
			}
			addFallbackModel(bestByModel, modelID, effectiveRate, metaByID)
		}
	}
}

// addFallbackModel synthesizes one fallback menu row for modelID unless a
// higher-priority source (a channel-priced row, or an earlier account)
// already covers it. Channel rows always win on conflict.
func addFallbackModel(
	bestByModel map[string]MePricingModel,
	modelID string,
	effectiveRate float64,
	metaByID map[string]PublicCatalogModel,
) {
	if _, exists := bestByModel[modelID]; exists {
		return
	}
	bestByModel[modelID] = buildAccountFallbackEntry(modelID, effectiveRate, metaByID)
}

// platformDefaultModelIDs returns the model-ID list an unrestricted native
// account contributes to the Group Catalog.
//
// Anthropic / OpenAI / Grok use the empirically-servable allowlist
// (supportedCatalogModelIDsForPlatform) — the SAME source the public /pricing
// catalog filters by — so both surfaces advertise exactly the models that
// passed a live prod probe (operator directive: "实测通过的才行"). This
// supersedes the earlier canonical-list fallback, which advertised
// upstream-rejected models (e.g. gpt-5.2, gpt-4o) that 400/502 at runtime. Grok
// has no canonical DefaultModels list at all — its served set IS its priced
// overlay set, so the allowlist is the only correct source.
//
// Gemini uses the empirical set once populated; an empty set still degrades to
// canonical inside tkServableCandidateIDs. This keeps advertised-dead ids such
// as gemini-2.0-flash and gemini-3.x chat out of the unrestricted menu once the
// live probe has populated the allowlist.
//
// Antigravity is deliberately EXCLUDED: resolveModelMapping injects
// domain.DefaultAntigravityModelMapping for an antigravity account with no
// explicit mapping, so such an account is effectively whitelist-restricted to
// that mapping's keys. newapi (and any unknown platform) also returns nil —
// it is channel / channel_type driven.
func platformDefaultModelIDs(platform string) []string {
	switch platform {
	case PlatformAnthropic, PlatformOpenAI:
		return supportedCatalogModelIDsForPlatform(platform)
	case PlatformGemini:
		return supportedCatalogModelIDsForPlatform(platform)
	case PlatformGrok:
		// Grok (seventh platform) is native OAuth-relay: accounts are
		// unrestricted and carry no channel, so the menu must fall back to the
		// curated grok served set (the priced overlay models) — the SAME source
		// the public /pricing catalog filters by. Without this case grok hit the
		// default (nil) arm and a grok group's "分组目录" was empty (2026-06-20).
		return supportedCatalogModelIDsForPlatform(platform)
	default:
		return nil
	}
}

// accountHasModelRestriction reports whether an account restricts its
// model set via a non-empty credentials.model_mapping. This mirrors
// Account.IsModelSupported's whitelist semantics: an empty/absent mapping
// means "all models allowed" (unrestricted), a non-empty mapping is a
// whitelist. Defensive against malformed JSON — a non-object value is
// treated as no restriction.
func accountHasModelRestriction(creds map[string]any) bool {
	if creds == nil {
		return false
	}
	raw, ok := creds["model_mapping"].(map[string]any)
	return ok && len(raw) > 0
}

// accountInGroupScope encodes the cross-platform leak guard for the
// fallback path. Matches the scheduling-pool partition the scheduler
// enforces (docs/approved/newapi-as-fifth-platform.md §2.1 "不混池"):
// openai groups schedule openai accounts only, newapi groups schedule
// newapi accounts only — there is no cross-platform routing between
// the two. We delegate openai / newapi to IsOpenAICompatPoolMember so
// the newapi pool additionally enforces channel_type > 0 (a newapi
// account with channel_type=0 has no New API adaptor target and would
// crash bridge dispatch — same defense as the scheduler in
// openai_account_scheduler.go). Other platforms only need strict
// platform equality.
func accountInGroupScope(a *Account, groupPlatform string) bool {
	if a == nil {
		return false
	}
	switch groupPlatform {
	case "openai", "newapi":
		return a.IsOpenAICompatPoolMember(groupPlatform)
	default:
		return a.Platform == groupPlatform
	}
}

// parseWhitelistFromCredentials extracts the whitelist-mode entries from
// an account's credentials.model_mapping JSON. Whitelist mode is the
// identity map ({from: from}); mapping mode ({from: to}, from != to) is
// a routing rewrite and contributes nothing to the menu — those models
// are *aliases*, not user-visible offerings.
//
// Returns nil for absent / non-object / empty input. Defensive type
// assertions guard against malformed JSON without panicking; a single
// non-string value entry is skipped rather than poisoning the whole map.
func parseWhitelistFromCredentials(creds map[string]any) []string {
	if creds == nil {
		return nil
	}
	raw, ok := creds["model_mapping"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for from, toAny := range raw {
		to, ok := toAny.(string)
		if !ok {
			continue
		}
		if to != from {
			continue
		}
		out = append(out, from)
	}
	return out
}

// buildAccountFallbackEntry constructs a MePricingModel for one
// whitelisted model_id, sourcing per-1k price and vendor from the
// catalog index when available. The metadata-join stage in
// buildModelsForGroup applies context_window / max_output_tokens /
// capabilities uniformly later, so this function only needs to seed
// price + vendor.
//
// When the catalog lacks the model (e.g. a freshly released family not
// yet in LiteLLM), the row is returned with nil prices so the user
// still sees the model is reachable through the gateway.
func buildAccountFallbackEntry(modelID string, rate float64, metaByID map[string]PublicCatalogModel) MePricingModel {
	entry := MePricingModel{
		ModelID:      modelID,
		BillingMode:  string(BillingModeToken),
		YourPrice:    MePricingPrice{Currency: "USD"},
		Capabilities: []string{},
	}
	meta, ok := metaByID[modelID]
	if !ok {
		if stripped, stripOK := stripVendorPrefixForCatalogLookup(modelID); stripOK {
			meta, ok = metaByID[stripped]
		}
	}
	if !ok {
		return entry
	}
	entry.Vendor = meta.Vendor
	if meta.Pricing.BillingMode != "" {
		entry.BillingMode = meta.Pricing.BillingMode
	}
	entry.YourPrice.InputPer1K = scaleCatalogPrice(meta.Pricing.InputPer1KTokens, rate)
	entry.YourPrice.OutputPer1K = scaleCatalogPrice(meta.Pricing.OutputPer1KTokens, rate)
	entry.YourPrice.CacheReadPer1K = scaleCatalogPrice(meta.Pricing.CacheReadPer1K, rate)
	entry.YourPrice.CacheWritePer1K = scaleCatalogPrice(meta.Pricing.CacheWritePer1K, rate)
	// Media units (nil when 0 — non-media models stay token-only).
	entry.YourPrice.PerImage = scaleCatalogPrice(meta.Pricing.OutputCostPerImage, rate)
	entry.YourPrice.PerSecond = scaleCatalogPrice(meta.Pricing.OutputCostPerSecond, rate)
	return entry
}

// scaleCatalogPrice multiplies a PublicCatalogPricing value (already in
// per-1k tokens — see pricing_catalog_tk.go PublicCatalogPricing) by the
// user's effective rate. Unlike scaleTo1K there is NO ×1000 unit
// conversion: catalog prices are already per-1k. Catalog uses 0.0 as
// the sentinel for "no price published" (fields are floats, not
// pointers), so 0.0 surfaces as nil here — preserving MePricingPrice's
// nil-vs-0 contract (nil = "—", 0 = real free-subscription price).
func scaleCatalogPrice(v, rate float64) *float64 {
	if v == 0 {
		return nil
	}
	r := v * rate
	return &r
}

// buildModelEntry maps a single SupportedModel + effective rate into a
// MePricingModel. Pricing fields are scaled to per-1k tokens for token
// modes; per_request stays as a per-call value.
func buildModelEntry(m SupportedModel, rate float64) MePricingModel {
	entry := MePricingModel{
		ModelID:      m.Name,
		BillingMode:  string(BillingModeToken),
		YourPrice:    MePricingPrice{Currency: "USD"},
		Capabilities: []string{},
	}
	p := m.Pricing
	if p == nil {
		return entry
	}
	if p.BillingMode != "" {
		entry.BillingMode = string(p.BillingMode)
	}
	entry.YourPrice.InputPer1K = scaleTo1K(p.InputPrice, rate)
	entry.YourPrice.OutputPer1K = scaleTo1K(p.OutputPrice, rate)
	entry.YourPrice.CacheReadPer1K = scaleTo1K(p.CacheReadPrice, rate)
	entry.YourPrice.CacheWritePer1K = scaleTo1K(p.CacheWritePrice, rate)
	entry.YourPrice.ImageOutputPer1K = scaleTo1K(p.ImageOutputPrice, rate)
	entry.YourPrice.PerRequest = scalePtr(p.PerRequestPrice, rate)
	return entry
}

// scaleTo1K converts a per-token price to per-1k-tokens and applies the
// effective multiplier. nil stays nil so the UI can render "—" instead
// of "$0".
func scaleTo1K(v *float64, rate float64) *float64 {
	if v == nil {
		return nil
	}
	r := *v * 1000.0 * rate
	return &r
}

// scalePtr applies the multiplier to a pointer-valued price (per_request
// flavored — not per-token, so no ×1000).
func scalePtr(v *float64, rate float64) *float64 {
	if v == nil {
		return nil
	}
	r := *v * rate
	return &r
}

// pickCheaperModel returns the entry whose combined input+output cost is
// lower. nil price components are treated as "no data" rather than free,
// so a candidate with concrete prices wins over one with all-nil pricing.
// For per_request mode, the per-call price contributes (×1000 to put it
// on the same magnitude axis as per-1k prices for a coarse comparison).
func pickCheaperModel(a, b MePricingModel) MePricingModel {
	ac, ah := modelComparableCost(a)
	bc, bh := modelComparableCost(b)
	switch {
	case ah && !bh:
		return a
	case bh && !ah:
		return b
	case ah && bh:
		if bc < ac {
			return b
		}
		return a
	default:
		return a
	}
}

// modelComparableCost returns (cost, has-any-price). cost only matters
// when has-any-price is true.
func modelComparableCost(m MePricingModel) (float64, bool) {
	var (
		c float64
		h bool
	)
	if v := m.YourPrice.InputPer1K; v != nil {
		c += *v
		h = true
	}
	if v := m.YourPrice.OutputPer1K; v != nil {
		c += *v
		h = true
	}
	if v := m.YourPrice.PerRequest; v != nil {
		c += *v * 1000
		h = true
	}
	return c, h
}
