package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

var ErrCandidatePolicyConflict = errors.New("candidate has conflicting authorized billing or request policies")

// selectCandidateBillingOrigin receives legal origins for one account in one
// payment tier. Price only selects its accounting origin, never an account.
func selectCandidateBillingOrigin(ctx context.Context, userID int64, account *Account, groups []Group, model string, shape UniversalShape, channels *ChannelService, rates *userGroupRateResolver, accounts AccountRepository) (*Group, error) {
	if len(groups) == 0 {
		return nil, ErrUniversalNoEntitledGroup
	}
	billingAccount, err := resolveCredentialAccount(ctx, accounts, account)
	if err != nil {
		return nil, err
	}
	at := time.Now()
	var selected *Group
	var selectedPolicy candidateBillingPolicy
	var selectedRate float64
	for i := range groups {
		group := &groups[i]
		policy, err := candidateBillingPolicyForOrigin(ctx, account, billingAccount, group, model, shape, channels)
		if err != nil {
			return nil, fmt.Errorf("candidate billing origin group %d: %w", group.ID, err)
		}
		if selected != nil && (selected.IsSubscriptionType() != group.IsSubscriptionType() || !reflect.DeepEqual(selectedPolicy, policy)) {
			return nil, fmt.Errorf("%w: account %d, groups %d and %d", ErrCandidatePolicyConflict, candidateBillingAccountID(account), selected.ID, group.ID)
		}
		rate := 0.0
		if !group.IsSubscriptionType() {
			rate = candidateBillingRate(ctx, userID, group, model, shape, policy.pricing, rates, at)
		}
		if selected == nil || (!group.IsSubscriptionType() && (rate < selectedRate || (rate == selectedRate && group.ID < selected.ID))) {
			selected, selectedPolicy, selectedRate = group, policy, rate
		}
	}
	// Subscription entitlement ordering belongs to the authorization/payment
	// owner. Preserve its first usable origin instead of comparing quota prices.
	result := *selected
	return &result, nil
}

func candidateBillingAccountID(account *Account) int64 {
	if account == nil {
		return 0
	}
	return account.ID
}

type candidateBillingPolicy struct {
	mappedModel        string
	billingModel       string
	responsePricing    bool
	pricing            *ChannelModelPricing
	fallbackPricing    []*ChannelModelPricing
	responseCards      []ChannelModelPricing
	longContext        bool
	freeOpenAIFast     bool
	compaction         openAICompatMessagesCompactionPolicy
	reasoningBody      string
	reasoningMaximum   string
	reasoningOverLimit string
	reasoningMapping   []ReasoningEffortMapping
	mcpXML             bool
	channelFeatures    map[string]any
	imagePrices        [3]*float64
	videoPrices        [][3]*float64
	searchPrice        *float64
	webSearchPrice     *float64
	audioPrices        [3]*float64
}

func candidateBillingPolicyForOrigin(ctx context.Context, account, billingAccount *Account, group *Group, model string, shape UniversalShape, channels *ChannelService) (candidateBillingPolicy, error) {
	policy := candidateBillingPolicy{mappedModel: model, billingModel: model}
	var channel *channelLookup
	if channels != nil {
		var err error
		// Read configuration with its error from one cache snapshot. A legacy
		// nil-pricing fallback cannot establish equivalence between two tariffs.
		channel, err = channels.lookupGroupChannel(ctx, group.ID)
		if err != nil {
			return policy, err
		}
	}
	if channel != nil {
		mapping := resolveMapping(channel, group.ID, model)
		policy.mappedModel = mapping.MappedModel
		switch mapping.BillingModelSource {
		case BillingModelSourceUpstream:
			policy.billingModel = mapping.MappedModel
			if account != nil {
				policy.billingModel = account.GetMappedModel(mapping.MappedModel)
			}
		case BillingModelSourceChannelMapped:
			policy.billingModel = mapping.MappedModel
		case BillingModelSourceResponse:
			policy.responsePricing = true
		}
		if len(channel.channel.FeaturesConfig) > 0 {
			policy.channelFeatures = channel.channel.FeaturesConfig
		}
	}
	// Settlement applies the credential account's served-model mapping after
	// channel policy. Compare that same tariff while retaining the execution Plan.
	policy.billingModel = settleBillingOnAccountServedModel(billingAccount, model, policy.billingModel)
	policy.pricing = candidateOriginModelPricing(group, channel, policy.billingModel)
	upstreamModel := policy.mappedModel
	if account != nil {
		upstreamModel = account.GetMappedModel(policy.mappedModel)
	}
	// Settlement can fall back between the request, channel and upstream names.
	// All reachable configured prices must agree before comparing multipliers.
	fallbacks := []string{model, policy.mappedModel, upstreamModel}
	if policy.pricing == nil {
		fallbacks = usageBillingModelCandidates(model, policy.mappedModel, upstreamModel)
	}
	fallbacks = append(fallbacks, policy.billingModel)
	for _, fallback := range fallbacks {
		policy.fallbackPricing = append(policy.fallbackPricing, candidateOriginModelPricing(group, channel, fallback))
	}
	if policy.responsePricing {
		// The upstream response model is not known at admission. Equality must
		// cover every configured response price, not just the requested model.
		for i := range group.ModelPricing {
			policy.responseCards = append(policy.responseCards, *candidateComparablePricing(&group.ModelPricing[i]))
			policy.responseCards[len(policy.responseCards)-1].Models = group.ModelPricing[i].Models
		}
		if channel != nil {
			for i := range channel.channel.ModelPricing {
				card := &channel.channel.ModelPricing[i]
				if isPlatformPricingMatch(channel.platform, card.Platform) {
					policy.responseCards = append(policy.responseCards, *candidateComparablePricing(card))
					policy.responseCards[len(policy.responseCards)-1].Models = card.Models
				}
			}
		}
	}
	image := shape == ShapeOpenAIImages || shape == ShapeOpenAIImagesEdit || antigravity.IsImageModel(model)
	policy.longContext = group.LongContextPricingEnabled
	// Compare the Fast tariff using settlement's credential/platform gates.
	// A multiplier alone cannot order Standard and Priority billing policies.
	policy.freeOpenAIFast = groupBillsOpenAIFastAtStandard(&APIKey{Group: group}, billingAccount, "priority")
	switch {
	case image:
		policy.imagePrices = [3]*float64{group.ImagePrice1K, group.ImagePrice2K, group.ImagePrice4K}
		if policy.pricing == nil || policy.pricing.BillingMode != BillingModeToken {
			policy.longContext = false
		}
	case shape == ShapeOpenAIVideo:
		policy.longContext = false
		for _, billingModel := range fallbacks {
			policy.videoPrices = append(policy.videoPrices, [3]*float64{
				group.GetVideoPriceForModel(billingModel, VideoBillingResolution480P),
				group.GetVideoPriceForModel(billingModel, VideoBillingResolution720P),
				group.GetVideoPriceForModel(billingModel, VideoBillingResolution1080P),
			})
		}
	default:
		policy.searchPrice = group.SearchPricePer1k
		policy.webSearchPrice = group.WebSearchPricePerCall
		policy.audioPrices = [3]*float64{group.AudioRealtimePricePerMin, group.AudioTTSPricePerMillionChars, group.AudioSTTPricePerHour}
	}
	if shape == ShapeAnthropicMessages && account != nil && IsOpenAICompatPlatform(account.Platform) {
		policy.compaction = resolveOpenAICompatMessagesCompactionPolicy(account, group)
	}
	if account != nil && account.Platform == PlatformAntigravity {
		policy.mcpXML = group.MCPXMLInject
	}
	if account != nil && account.Platform == PlatformOpenAI && (shape == ShapeOpenAIChat || shape == ShapeAnthropicMessages || shape == ShapeAnthropicCountTokens) {
		if request, ok := ProtocolRoutingRequest(ctx); ok {
			body, _, err := ApplyOpenAIReasoningEffortPolicy(request.Body(), group.MaxReasoningEffort, group.ReasoningEffortMappings, group.MaxReasoningEffortOverLimit)
			if err != nil {
				return policy, err
			}
			policy.reasoningBody = string(body)
		} else {
			policy.reasoningMaximum = NormalizeMaxReasoningEffort(group.MaxReasoningEffort)
			policy.reasoningOverLimit = NormalizeMaxReasoningEffortOverLimit(group.MaxReasoningEffortOverLimit)
			if len(group.ReasoningEffortMappings) > 0 {
				policy.reasoningMapping = group.ReasoningEffortMappings
			}
		}
	}
	return policy, nil
}

func candidateOriginModelPricing(group *Group, channel *channelLookup, model string) *ChannelModelPricing {
	pricing := matchGroupModelPricing(group, model)
	if pricing != nil && (pricing.BillingMode == "" || pricing.BillingMode == BillingModeToken) {
		pricing.Intervals = nil // Group token cards only override flat prices.
	}
	if pricing == nil && channel != nil {
		pricing = lookupPricingAcrossPlatforms(channel.cache, group.ID, channel.platform, model)
	}
	return candidateComparablePricing(pricing)
}

func candidateComparablePricing(pricing *ChannelModelPricing) *ChannelModelPricing {
	if pricing == nil {
		return nil
	}
	cp := pricing.Clone()
	cp.ID, cp.ChannelID, cp.Platform, cp.Models = 0, 0, "", nil
	cp.CreatedAt, cp.UpdatedAt = time.Time{}, time.Time{}
	if cp.BillingMode == "" {
		cp.BillingMode = BillingModeToken
	}
	if len(cp.Intervals) == 0 {
		cp.Intervals = nil
	}
	for i := range cp.Intervals {
		cp.Intervals[i].ID, cp.Intervals[i].PricingID, cp.Intervals[i].SortOrder = 0, 0, 0
		cp.Intervals[i].CreatedAt, cp.Intervals[i].UpdatedAt = time.Time{}, time.Time{}
	}
	return &cp
}

func candidateBillingRate(ctx context.Context, userID int64, group *Group, model string, shape UniversalShape, pricing *ChannelModelPricing, rates *userGroupRateResolver, at time.Time) float64 {
	base := rates.Resolve(ctx, userID, group.ID, group.RateMultiplier)
	key := &APIKey{Group: group}
	text, image := computePeakAwareMultipliers(key, base, at)
	switch {
	case shape == ShapeOpenAIVideo:
		return resolveVideoRateMultiplier(key, base)
	case shape == ShapeOpenAIImages || shape == ShapeOpenAIImagesEdit || antigravity.IsImageModel(model):
		if pricing != nil && (pricing.BillingMode == BillingModeToken || pricing.BillingMode == "") {
			return text
		}
		return image
	default:
		return text
	}
}
