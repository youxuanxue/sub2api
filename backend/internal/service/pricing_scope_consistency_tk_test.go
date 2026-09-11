//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPricingScopeGroupNormalizedPricePrecedence(t *testing.T) {
	var groupID int64 = 777
	cs := newChannelServiceWithPricings(groupID, []ChannelModelPricing{tokenPricingForModels([]string{"gpt-5.6-luna"}, 0.4)})
	bs := NewBillingService(nil, nil)
	resolver := NewModelPricingResolver(cs, bs)
	group := &Group{ID: groupID, Platform: PlatformOpenAI, ModelPricing: []ChannelModelPricing{tokenPricingForModels([]string{"gpt-5.6-luna"}, 0.8)}}
	base := resolver.Resolve(context.Background(), PricingInput{Model: "gpt-5.6-luna", GroupID: &groupID, Group: group})
	variant := resolver.Resolve(context.Background(), PricingInput{Model: "gpt-5.6-luna-high", GroupID: &groupID, Group: group})
	t.Logf("base source=%s input/M=%g; variant source=%s input/M=%g", base.Source, base.BasePricing.InputPricePerToken*1e6, variant.Source, variant.BasePricing.InputPricePerToken*1e6)
	require.Equal(t, base.Source, variant.Source, "same canonical model must honor group price before channel")
}

func TestPricingScopeCandidateNormalizedChannelPrice(t *testing.T) {
	var groupID int64 = 777
	cs := newChannelServiceWithPricings(groupID, []ChannelModelPricing{tokenPricingForModels([]string{"gpt-5.6-luna"}, 0.4)})
	resolver := NewModelPricingResolver(cs, NewBillingService(nil, nil))
	group := &Group{ID: groupID, Platform: PlatformOpenAI}
	channel, err := cs.lookupGroupChannel(context.Background(), groupID)
	require.NoError(t, err)
	settlement := resolver.Resolve(context.Background(), PricingInput{Model: "gpt-5.6-luna-high", GroupID: &groupID, Group: group})
	comparable := candidateOriginModelPricing(group, channel, "gpt-5.6-luna-high")
	t.Logf("settlement source=%s; candidateComparable=%#v", settlement.Source, comparable)
	require.NotNil(t, comparable, "candidate price comparison must resolve same configured card as settlement")
}

func TestPricingScopeEquivalentChannelCardsDoNotRejectCandidate(t *testing.T) {
	channels := []Channel{
		{ID: 1, Name: "base-card", Status: StatusActive, GroupIDs: []int64{1}, ModelPricing: []ChannelModelPricing{tokenPricingForModels([]string{"gpt-5.6-luna"}, 0.4)}},
		{ID: 2, Name: "variant-card", Status: StatusActive, GroupIDs: []int64{2}, ModelPricing: []ChannelModelPricing{tokenPricingForModels([]string{"gpt-5.6-luna-high"}, 0.4)}},
	}
	cs := &ChannelService{}
	cs.cache.Store(populateChannelCache(channels, map[int64]string{1: PlatformOpenAI, 2: PlatformOpenAI}))
	groups := []Group{{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1}, {ID: 2, Platform: PlatformOpenAI, RateMultiplier: 1}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	resolver := NewModelPricingResolver(cs, NewBillingService(nil, nil))
	for i := range groups {
		actual := resolver.Resolve(context.Background(), PricingInput{Model: "gpt-5.6-luna-high", GroupID: &groups[i].ID, Group: &groups[i]})
		t.Logf("group=%d actual source=%s input/M=%g", groups[i].ID, actual.Source, actual.BasePricing.InputPricePerToken*1e6)
	}
	_, err := selectCandidateBillingOrigin(context.Background(), 1, account, groups, "gpt-5.6-luna-high", ShapeOpenAIChat, cs, nil, nil)
	require.NoError(t, err, "equivalent effective channel prices must not reject the candidate")
	channels[1].ModelPricing[0].InputPrice = float64Ptr(0.5e-6)
	cs.cache.Store(populateChannelCache(channels, map[int64]string{1: PlatformOpenAI, 2: PlatformOpenAI}))
	_, err = selectCandidateBillingOrigin(context.Background(), 1, account, groups, "gpt-5.6-luna-high", ShapeOpenAIChat, cs, nil, nil)
	require.ErrorIs(t, err, ErrCandidatePolicyConflict, "different effective tariffs still reject ambiguous origins")
}

func TestPricingScopeImageGroupPriceGuardSettlementParity(t *testing.T) {
	price := 0.25
	group := &Group{ID: 1, Platform: PlatformOpenAI, ModelPricing: []ChannelModelPricing{{Models: []string{"vendor-image-custom"}, BillingMode: BillingModeImage, PerRequestPrice: &price}}}
	bs := NewBillingService(nil, &PricingService{})
	resolver := NewModelPricingResolver(nil, bs)
	gateway := &OpenAIGatewayService{billingService: bs, resolver: resolver}
	actual, err := gateway.calculateOpenAIImageCost(context.Background(), "vendor-image-custom", &APIKey{GroupID: &group.ID, Group: group}, &OpenAIForwardResult{ImageCount: 1}, 1)
	require.NoError(t, err)
	require.Equal(t, 0.25, actual.TotalCost)
	blocked := bs.TkImageModelUnpriced("vendor-image-custom", group, "")
	t.Logf("image group settlement=%g; unpriced guard blocked=%v", actual.TotalCost, blocked)
	require.False(t, blocked, "valid group image price must pass the same pre-forward price check")
	repo := &openAIRecordUsageLogRepoStub{inserted: true}
	gateway = newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	gateway.billingService, gateway.resolver = bs, resolver
	group.RateMultiplier = 1
	err = gateway.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result:             &OpenAIForwardResult{RequestID: "group-image-price", Model: "vendor-image-custom", BillingModel: "vendor-image-custom", ImageCount: 1, Duration: time.Second},
		ChannelUsageFields: ChannelUsageFields{OriginalModel: "vendor-image-custom", ChannelMappedModel: "vendor-image-custom"},
		APIKey:             &APIKey{ID: 1, GroupID: &group.ID, Group: group}, User: &User{ID: 1}, Account: &Account{ID: 1, Platform: PlatformOpenAI},
	})
	require.NoError(t, err)
	require.NotNil(t, repo.lastLog)
	require.Equal(t, 0.25, repo.lastLog.TotalCost)
}

func TestPricingScopeGroupNormalizedRecordUsage(t *testing.T) {
	for _, model := range []string{"gpt-5.6-luna", "gpt-5.6-luna-high", "gpt-5.6-luna-2026-08-01"} {
		t.Run(model, func(t *testing.T) {
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			svc.channelService = newChannelServiceWithPricings(777, []ChannelModelPricing{tokenPricingForModels([]string{"gpt-5.6-luna"}, 0.4)})
			svc.resolver = NewModelPricingResolver(svc.channelService, svc.billingService)
			group := &Group{ID: 777, Platform: PlatformOpenAI, RateMultiplier: 1, ModelPricing: []ChannelModelPricing{tokenPricingForModels([]string{"gpt-5.6-luna"}, 0.8)}}
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result:             &OpenAIForwardResult{RequestID: "pricing-scope", Model: model, BillingModel: model, Usage: OpenAIUsage{InputTokens: 1_000_000}, Duration: time.Second},
				ChannelUsageFields: ChannelUsageFields{OriginalModel: model, ChannelMappedModel: model},
				APIKey:             &APIKey{ID: 1, GroupID: &group.ID, Group: group}, User: &User{ID: 1}, Account: &Account{ID: 1, Platform: PlatformOpenAI},
			})
			require.NoError(t, err)
			require.NotNil(t, repo.lastLog)
			require.InDelta(t, 0.8, repo.lastLog.InputCost, 1e-9)
		})
	}
}
func TestPricingScopeLiteralAndWildcardBeforeNormalized(t *testing.T) {
	for _, pattern := range []string{"gpt-5.6-luna-high", "gpt-5.6-luna-*"} {
		group := &Group{ModelPricing: []ChannelModelPricing{tokenPricingForModels([]string{"gpt-5.6-luna"}, 0.4), tokenPricingForModels([]string{pattern}, 0.9)}}
		require.InDelta(t, 0.9e-6, *matchGroupModelPricing(group, "gpt-5.6-luna-high").InputPrice, 1e-15)
		require.Nil(t, matchGroupModelPricing(group, "unrelated-model"))
	}
}
func TestPricingScopeImageEmptyCardsRemainBlocked(t *testing.T) {
	bs := NewBillingService(nil, &PricingService{})
	for _, card := range []ChannelModelPricing{
		{BillingMode: BillingModeImage},
		{BillingMode: BillingModeImage, PerRequestPrice: float64Ptr(0)},
		{BillingMode: BillingModeImage, PerRequestPrice: float64Ptr(-1)},
		{BillingMode: BillingModeImage, Intervals: []PricingInterval{{InputPrice: float64Ptr(1)}}},
		{BillingMode: BillingModeToken, InputPrice: float64Ptr(1e-6), OutputPrice: float64Ptr(2e-6)},
		{BillingMode: BillingModeVideo, PerRequestPrice: float64Ptr(1)},
	} {
		card.Models = []string{"vendor-image-custom"}
		require.True(t, bs.TkImageModelUnpriced("vendor-image-custom", &Group{ModelPricing: []ChannelModelPricing{card}}, ""))
	}
}

func TestPricingScopeImageGroupModesAndScope(t *testing.T) {
	bs := NewBillingService(nil, &PricingService{})
	for _, card := range []ChannelModelPricing{
		{BillingMode: BillingModePerRequest, PerRequestPrice: float64Ptr(0.25)},
		{BillingMode: BillingModeImage, Intervals: []PricingInterval{{TierLabel: "1K", PerRequestPrice: float64Ptr(0.25)}}},
	} {
		card.Models = []string{"vendor-image-custom"}
		group := &Group{ModelPricing: []ChannelModelPricing{card}}
		require.False(t, bs.TkImageModelUnpriced("vendor-image-custom", group, "1K"))
		require.True(t, bs.TkImageModelUnpriced("other-image-custom", group, ""))
	}
}

func TestPricingScopeImageGroupZeroCardOverridesGlobalPrice(t *testing.T) {
	const model = "imagen-4.0-generate-001"
	for _, mode := range []BillingMode{BillingModeImage, BillingModePerRequest} {
		for _, price := range []*float64{nil, float64Ptr(0), float64Ptr(-0.25)} {
			billing := tkMediaGuardBillingService()
			require.False(t, billing.TkImageModelUnpriced(model, nil, ""), "fixture has a positive global image price")
			group := &Group{ID: 1, ModelPricing: []ChannelModelPricing{{Models: []string{model}, BillingMode: mode, PerRequestPrice: price}}}
			gateway := &OpenAIGatewayService{billingService: billing, resolver: NewModelPricingResolver(nil, billing)}
			actual, err := gateway.calculateOpenAIImageCost(context.Background(), model, &APIKey{GroupID: &group.ID, Group: group}, &OpenAIForwardResult{ImageCount: 1}, 1)
			require.NoError(t, err)
			require.LessOrEqual(t, actual.TotalCost, 0.0, "settlement honors the matched group card instead of global pricing")
			require.True(t, billing.TkImageModelUnpriced(model, group, ""), "matched nonpositive card must not fall back to the global price for admission")
		}
	}
}
func TestPricingScopeImageTokenCardCannotPriceCountOnlyUsage(t *testing.T) {
	const model = "vendor-image-custom"
	repo := &openAIRecordUsageLogRepoStub{inserted: true}
	gateway := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	billing := NewBillingService(nil, &PricingService{})
	gateway.billingService = billing
	gateway.resolver = NewModelPricingResolver(nil, billing)
	group := &Group{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1, ModelPricing: []ChannelModelPricing{{Models: []string{model}, BillingMode: BillingModeToken, InputPrice: float64Ptr(1e-6), OutputPrice: float64Ptr(2e-6)}}}
	err := gateway.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result:             &OpenAIForwardResult{RequestID: "count-only-group-token-card", Model: model, BillingModel: model, ImageCount: 1, Duration: time.Second},
		ChannelUsageFields: ChannelUsageFields{OriginalModel: model, ChannelMappedModel: model},
		APIKey:             &APIKey{ID: 1, GroupID: &group.ID, Group: group}, User: &User{ID: 1}, Account: &Account{ID: 1, Platform: PlatformOpenAI},
	})
	require.NoError(t, err)
	require.NotNil(t, repo.lastLog)
	require.Zero(t, repo.lastLog.TotalCost, "a group token card cannot price an image count without reported tokens")
	require.True(t, billing.TkImageModelUnpriced(model, group, ""), "token card alone must not newly admit count-only image execution")
}

func TestPricingScopeImageRequestedSizeMatchesSettlement(t *testing.T) {
	const model = "vendor-image-custom"
	for _, tc := range []struct {
		name, size               string
		tier, defaultPrice, want float64
	}{
		{name: "priced_1k", size: "1K", tier: 0.25, want: 0.25},
		{name: "unpriced_2k", size: "2K", tier: 0.25, want: 0},
		{name: "default_size_2k", size: "", tier: 0.25, want: 0},
		{name: "pixel_size", size: "1024x1024", tier: 0.25, want: 0.25},
		{name: "default_price", size: "2K", tier: 0.25, defaultPrice: 0.5, want: 0.5},
		{name: "zero_tier_fallback", size: "1K", tier: 0, defaultPrice: 0.5, want: 0.5},
		{name: "negative_tier_does_not_fallback", size: "1K", tier: -0.25, defaultPrice: 0.5, want: -0.25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			billing := NewBillingService(nil, &PricingService{})
			group := &Group{ID: 1, ModelPricing: []ChannelModelPricing{{Models: []string{model}, BillingMode: BillingModeImage, PerRequestPrice: &tc.defaultPrice, Intervals: []PricingInterval{{TierLabel: "1K", PerRequestPrice: &tc.tier}}}}}
			gateway := &OpenAIGatewayService{billingService: billing, resolver: NewModelPricingResolver(nil, billing)}
			actual, err := gateway.calculateOpenAIImageCost(context.Background(), model, &APIKey{GroupID: &group.ID, Group: group}, &OpenAIForwardResult{ImageCount: 1, ImageSize: tc.size}, 1)
			require.NoError(t, err)
			require.Equal(t, tc.want, actual.TotalCost)
			require.Equal(t, tc.want <= 0, billing.TkImageModelUnpriced(model, group, tc.size), "admission must evaluate the price settlement will use for the requested size")
		})
	}
}
