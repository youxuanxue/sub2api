//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// Explicit token ceilings still get the strong overdraft property: the hold is
// an UPPER BOUND on whatever the request can actually be billed. These tests
// pin that property across token splits and service tiers for the path where
// maxOut is a real client ceiling.

func TestEstimateTokenHold_IsUpperBoundOverDistributions(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil) // fallback pricing, no pricingService
	const (
		model   = "claude-sonnet-4"
		prompt  = 1000 // upper bound on input tokens
		maxOut  = 500  // hard output ceiling
		mult    = 1.0
		epsilon = 1e-9
	)

	for _, tier := range []string{"", "priority", "flex"} {
		hold, err := s.EstimateTokenHold(model, tier, "", prompt, maxOut, mult)
		if err != nil {
			t.Fatalf("EstimateTokenHold(tier=%q): %v", tier, err)
		}

		// Every distribution the request could actually resolve to: input is
		// split across input / cache-creation / cache-read (cache-creation is
		// the dearest, cache-read the cheapest), output up to maxOut.
		dists := []UsageTokens{
			{InputTokens: prompt, OutputTokens: maxOut},
			{CacheCreationTokens: prompt, OutputTokens: maxOut}, // most expensive input
			{InputTokens: prompt / 2, CacheCreationTokens: prompt / 2, OutputTokens: maxOut},
			{CacheReadTokens: prompt, OutputTokens: maxOut}, // cheapest input
			{InputTokens: prompt, OutputTokens: 0},
		}
		for _, d := range dists {
			bd, err := s.CalculateCostWithServiceTier(model, d, mult, tier)
			if err != nil {
				t.Fatalf("CalculateCostWithServiceTier(tier=%q, %+v): %v", tier, d, err)
			}
			if bd.ActualCost > hold+epsilon {
				t.Errorf("hold is NOT an upper bound: tier=%q dist=%+v actual=%.12f > hold=%.12f",
					tier, d, bd.ActualCost, hold)
			}
		}
	}
}

func TestEstimateTokenHold_HonorsInclusiveLongContextBoundary(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	pricing := s.fallbackPrices["grok-4.6"]
	pricing.LongContextThresholdInclusive = true
	const (
		prompt = 200000
		maxOut = 1000
	)

	hold, err := s.EstimateTokenHold("grok-4.6", "", "", prompt, maxOut, 1)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := s.CalculateCostWithServiceTier(
		"grok-4.6",
		UsageTokens{InputTokens: prompt, OutputTokens: maxOut},
		1,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if actual.ActualCost > hold {
		t.Fatalf("inclusive threshold hold must cover settlement: actual=%.12f hold=%.12f", actual.ActualCost, hold)
	}
}

// The reserve must stay an upper bound on whatever settlement can bill, across
// every tier the request could be billed at (the upstream may only lower the
// tier) and every token split — WITHOUT double-counting the tier premium by
// applying a multiplier on top of the priority unit prices already taken by
// max(). This sweep is the guard for the oversized-reserve fix: it fails both
// if the estimate under-reserves and (via the sizing test below) if it reverts
// to charging the priority premium twice.
// The sweep must settle through the SAME entry point production uses —
// CalculateCostUnified → calculateTokenCost — not CalculateCostWithServiceTier.
// The latter stops at computeTokenBreakdown and silently skips the
// reasoning-effort and channel-time premiums applied after it
// (billing_service.go:1020-1021), so a sweep built on it cannot see an
// under-reservation caused by either. `ultrafast` is in the tier list for the
// same reason: usePriorityServiceTierPricing matches priority/fast only, so
// ultrafast settles as unit price × UltrafastMultiplier.
func TestEstimateTokenHold_UpperBoundAcrossTiersAndSplits(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	resolver := NewModelPricingResolver(nil, s)
	const epsilon = 1e-9
	tiers := []string{"", "default", "priority", "fast", "flex", "scale", OpenAIFastTierUltrafast}
	// Effort is pinned at reserve time from the post-policy body; omitted
	// effort reserves 1x (settlement never injects one). Sweep still walks
	// every declared effort so a premium cannot silently under-reserve.
	efforts := []string{"", "minimal", "low", "medium", "high", "xhigh", "max", "none"}

	for _, model := range []string{"gpt-6-astra", "claude-sonnet-4", "grok-4.6", "gpt-5.4", "claude-fable-5-1", "claude-opus-5-5"} {
		for _, pm := range [][2]int{{5000, 2000}, {300000, 4000}, {271999, 10}, {272001, 10}} {
			prompt, maxOut := pm[0], pm[1]
			for _, requested := range tiers {
				for _, requestedEffort := range efforts {
					hold, err := s.EstimateTokenHold(model, requested, requestedEffort, prompt, maxOut, 1.0)
					if err != nil {
						continue
					}
					requestedRank, _ := serviceTierCostRank(requested)
					// Settlement prices the effort finally forwarded, which the
					// reserve resolves from the same post-policy body: the group
					// policy caps and maps declared values but never injects one,
					// so the billed effort equals the reserved one.
					billedEfforts := []string{requestedEffort}
					for _, billed := range tiers {
						// Settlement bills the requested tier or a cheaper one.
						if billedRank, _ := serviceTierCostRank(billed); billedRank > requestedRank {
							continue
						}
						for _, billedEffort := range billedEfforts {
							for _, d := range []UsageTokens{
								{InputTokens: prompt, OutputTokens: maxOut},
								{CacheCreationTokens: prompt, OutputTokens: maxOut},
								{InputTokens: prompt / 2, CacheCreationTokens: prompt / 2, OutputTokens: maxOut},
								{CacheReadTokens: prompt, OutputTokens: maxOut},
								{InputTokens: prompt, OutputTokens: maxOut, ImageOutputTokens: maxOut / 2},
								{CacheCreationTokens: prompt, CacheCreation1hTokens: prompt, OutputTokens: maxOut},
								{InputTokens: prompt, ImageInputTokens: prompt / 2, OutputTokens: maxOut},
							} {
								bd, err := s.CalculateCostUnified(CostInput{
									Ctx:             context.Background(),
									Model:           model,
									Tokens:          d,
									RateMultiplier:  1.0,
									ServiceTier:     billed,
									ReasoningEffort: billedEffort,
									Resolver:        resolver,
								})
								if err != nil || bd == nil {
									continue
								}
								if bd.ActualCost > hold+epsilon {
									t.Fatalf("hold is NOT an upper bound: model=%s requested_tier=%q billed_tier=%q requested_effort=%q billed_effort=%q prompt=%d dist=%+v actual=%.12f hold=%.12f",
										model, requested, billed, requestedEffort, billedEffort, prompt, d, bd.ActualCost, hold)
								}
							}
						}
					}
				}
			}
		}
	}
}

// The reserve must carry the reasoning-effort premium settlement applies after
// computeTokenBreakdown. Fable 5.1 bills an unconfigured `max` effort at 3x, so
// before this the reserve covered a third of the bill and the overdraw guard did
// not hold for the dearest requests on that model.
func TestEstimateTokenHold_CoversReasoningEffortPremium(t *testing.T) {
	const model = "claude-fable-5-1"
	s := NewBillingService(&config.Config{}, nil)
	resolver := NewModelPricingResolver(nil, s)
	const prompt, maxOut = 5000, 2000

	base, err := s.EstimateTokenHold(model, "", "low", prompt, maxOut, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	maxEffort, err := s.EstimateTokenHold(model, "", "max", prompt, maxOut, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if maxEffort <= base {
		t.Errorf("a max-effort request must reserve above a low-effort one: max=%.8f low=%.8f", maxEffort, base)
	}

	settled, err := s.CalculateCostUnified(CostInput{
		Ctx:             context.Background(),
		Model:           model,
		Tokens:          UsageTokens{InputTokens: prompt, OutputTokens: maxOut},
		RateMultiplier:  1.0,
		ReasoningEffort: "max",
		Resolver:        resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if settled.ActualCost > maxEffort+1e-9 {
		t.Errorf("max-effort settlement must not exceed its reserve: actual=%.8f hold=%.8f", settled.ActualCost, maxEffort)
	}

	// An omitted effort must reserve 1x, exactly as settlement bills it: the
	// policy never injects an effort into a body that omits one, so reserving
	// the dearest premium here would re-create the oversized reserve this path
	// exists to remove.
	unknown, err := s.EstimateTokenHold(model, "", "", prompt, maxOut, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if unknown != base {
		t.Errorf("an omitted effort must reserve the unpremiumed amount: unknown=%.8f low=%.8f", unknown, base)
	}
	unknownSettled, err := s.CalculateCostUnified(CostInput{
		Ctx:            context.Background(),
		Model:          model,
		Tokens:         UsageTokens{InputTokens: prompt, OutputTokens: maxOut},
		RateMultiplier: 1.0,
		Resolver:       resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if unknownSettled.ActualCost > unknown+1e-9 {
		t.Errorf("an omitted-effort settlement must not exceed its reserve: actual=%.8f hold=%.8f", unknownSettled.ActualCost, unknown)
	}
}

// A body with thinking enabled and no explicit effort is forwarded as `high` on
// passback-required models, so the reserve must price that same effort rather
// than the absent one — otherwise a premium model under-reserves whenever the
// client expresses thinking through `thinking.type` instead of an effort field.
func TestRequestedReasoningEffortForHold_MirrorsThinkingEnabledFallback(t *testing.T) {
	const model = "claude-fable-5-1"
	if DefaultEffortForThinkingEnabled(model) == nil {
		t.Skipf("%s is not a passback-required model; case no longer applicable", model)
	}
	s := NewBillingService(&config.Config{}, nil)
	const prompt, maxOut = 5000, 2000

	absent, err := s.EstimateTokenHold(model, "", "", prompt, maxOut, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := s.EstimateTokenHold(model, "", *DefaultEffortForThinkingEnabled(model), prompt, maxOut, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if fallback < absent {
		t.Errorf("the thinking-enabled fallback effort must not reserve below an absent one: fallback=%.8f absent=%.8f", fallback, absent)
	}
}

// `ultrafast` is not a priority-unit tier: computeTokenBreakdown resolves it to
// UltrafastMultiplier (6x on gpt-6-astra), so dropping the multiplier for it
// would under-reserve by that factor.
func TestEstimateTokenHold_CoversUltrafastMultiplier(t *testing.T) {
	const model = "gpt-6-astra"
	s := NewBillingService(&config.Config{}, nil)
	resolver := NewModelPricingResolver(nil, s)
	const prompt, maxOut = 5000, 2000

	ultrafast, err := s.EstimateTokenHold(model, OpenAIFastTierUltrafast, "", prompt, maxOut, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	priority, err := s.EstimateTokenHold(model, "priority", "", prompt, maxOut, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if ultrafast <= priority {
		t.Errorf("ultrafast carries its own multiplier, so it must reserve above priority: ultrafast=%.8f priority=%.8f", ultrafast, priority)
	}

	settled, err := s.CalculateCostUnified(CostInput{
		Ctx:            context.Background(),
		Model:          model,
		Tokens:         UsageTokens{InputTokens: prompt, OutputTokens: maxOut},
		RateMultiplier: 1.0,
		ServiceTier:    OpenAIFastTierUltrafast,
		Resolver:       resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if settled.ActualCost > ultrafast+1e-9 {
		t.Errorf("ultrafast settlement must not exceed its reserve: actual=%.8f hold=%.8f", settled.ActualCost, ultrafast)
	}
}

// Catalog DeepSeek list prices are off-peak; settlement scales by
// PeakMultiplier during windows. The hold must carry that peak factor or a
// peak-window request overdraws by that multiplier (proven 2.00x before the
// fix on deepseek-v4-flash).
func TestEstimateTokenHold_CoversDeepSeekPeakMultiplier(t *testing.T) {
	const model = "deepseek-v4-flash"
	s := NewBillingService(&config.Config{}, &PricingService{useActiveRegistry: true})
	resolver := NewModelPricingResolver(nil, s)
	const prompt, maxOut = 5000, 2000
	peakAt := time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC) // 10:00 Beijing peak

	hold, err := s.EstimateTokenHold(model, "", "", prompt, maxOut, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	settled, err := s.CalculateCostUnified(CostInput{
		Ctx:            context.Background(),
		Model:          model,
		Tokens:         UsageTokens{InputTokens: prompt, OutputTokens: maxOut},
		RateMultiplier: 1.0,
		Resolver:       resolver,
		PricingAt:      peakAt,
		BillingAt:      peakAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if settled.ActualCost > hold+1e-9 {
		t.Fatalf("deepseek peak settlement must not exceed its reserve: actual=%.12f hold=%.12f", settled.ActualCost, hold)
	}
	policy := loadTkDeepSeekPeakValleyPolicy()
	if policy == nil || policy.PeakMultiplier <= 1 {
		t.Fatal("expected a DeepSeek peak policy with PeakMultiplier > 1")
	}
	// Off-peak catalog hold without the peak scale would be settle/PeakMultiplier.
	// Require the hold to be at least the peak settlement (within epsilon).
	if hold+1e-9 < settled.ActualCost {
		t.Fatalf("hold under-reserved peak: hold=%.12f settle=%.12f", hold, settled.ActualCost)
	}
}

// Channel TimePricing multiplies settlement after computeTokenBreakdown.
// Catalog EstimateTokenHold alone cannot see it; EstimateTokenHoldResolved
// must carry the dearest period multiplier.
func TestEstimateTokenHoldResolved_CoversChannelTimeMultiplier(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	const groupID int64 = 42
	const model = "gpt-5.1"
	const prompt, maxOut = 5000, 2000
	resolver := newOpenAITokenImageChannelPricingResolverWithTimeForTest(t, groupID, model, &ChannelTimePricing{
		Timezone: "UTC",
		Periods:  []ChannelTimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}},
	})
	apiKey := &APIKey{ID: 1, GroupID: &[]int64{groupID}[0], Group: &Group{ID: groupID, Platform: PlatformOpenAI, RateMultiplier: 1}}
	gw := &OpenAIGatewayService{billingService: s, resolver: resolver}
	resolved := gw.resolveOpenAIChannelPricing(context.Background(), model, apiKey)
	if resolved == nil || resolved.Source != PricingSourceChannel {
		t.Fatalf("expected channel pricing, got %#v", resolved)
	}

	hold, err := s.EstimateTokenHoldResolved(model, "", "", prompt, maxOut, 1.0, resolved, resolver)
	if err != nil {
		t.Fatal(err)
	}
	catalogHold, err := s.EstimateTokenHold(model, "", "", prompt, maxOut, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if hold <= catalogHold {
		t.Fatalf("channel time hold must exceed catalog hold: channel=%.12f catalog=%.12f", hold, catalogHold)
	}

	peakAt := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	settled, err := s.CalculateCostUnified(CostInput{
		Ctx:            context.Background(),
		Model:          model,
		GroupID:        &[]int64{groupID}[0],
		Tokens:         UsageTokens{InputTokens: prompt, OutputTokens: maxOut},
		RateMultiplier: 1.0,
		Resolver:       resolver,
		Resolved:       resolved,
		PricingAt:      peakAt,
		BillingAt:      peakAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if settled.ActualCost > hold+1e-9 {
		t.Fatalf("channel time settlement must not exceed its reserve: actual=%.12f hold=%.12f", settled.ActualCost, hold)
	}
}

// A client ceiling far above what the model can emit is not reservable cost.
// gpt-6-astra caps output at 128k, so an absurd max_output_tokens must reserve
// the capped amount rather than scaling without limit — this is what pinned
// ~47% of a real user's balance on one request and produced a 403 burst while
// the account visibly had money.
func TestEstimateTokenHold_ClampsOutputCeilingToModelLimit(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	outputCeiling := tkRegistryMaxOutputTokens("gpt-6-astra")
	if outputCeiling <= 0 {
		t.Fatal("expected a catalog output ceiling for gpt-6-astra")
	}

	atLimit, err := s.EstimateTokenHold("gpt-6-astra", "", "", 1000, outputCeiling, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	absurd, err := s.EstimateTokenHold("gpt-6-astra", "", "", 1000, outputCeiling*50, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if absurd != atLimit {
		t.Errorf("a ceiling above the model limit must reserve the capped amount: absurd=%.6f at_limit=%.6f", absurd, atLimit)
	}

	// Ceilings below the model limit are still honored verbatim.
	small, err := s.EstimateTokenHold("gpt-6-astra", "", "", 1000, 512, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if small >= atLimit {
		t.Errorf("a small explicit ceiling must reserve less than the model cap: small=%.6f at_limit=%.6f", small, atLimit)
	}
}

// A declared public alias has no registry row of its own, so the ceiling must
// resolve through its owner — otherwise every aliased model silently loses the
// clamp, and an alias and its owner would reserve different amounts for an
// identical request.
func TestRegistryMaxOutputTokens_ResolvesThroughAliasOwner(t *testing.T) {
	const (
		alias = "deepseek-chat"
		owner = "deepseek-v4-flash"
	)
	ownerCap := tkRegistryMaxOutputTokens(owner)
	if ownerCap <= 0 {
		t.Skipf("registry declares no output ceiling for %s", owner)
	}
	if got := tkRegistryMaxOutputTokens(alias); got != ownerCap {
		t.Errorf("alias %s must inherit its owner's ceiling: got %d, want %d", alias, got, ownerCap)
	}

	s := NewBillingService(&config.Config{}, nil)
	aliasHold, err := s.EstimateTokenHold(alias, "", "", 1000, ownerCap*50, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	ownerHold, err := s.EstimateTokenHold(owner, "", "", 1000, ownerCap*50, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if aliasHold != ownerHold {
		t.Errorf("alias and owner must reserve identically: alias=%.6f owner=%.6f", aliasHold, ownerHold)
	}
}

// A model whose registry row declares no ceiling must keep the previous
// unbounded behaviour: the clamp only ever reduces over-reserving, it must
// never become a path to under-reserving. grok-code-fast-1 is a direct registry
// row that omits max_output_tokens, so it exercises exactly that case.
func TestRegistryMaxOutputTokens_NoCeilingLeavesReserveUnclamped(t *testing.T) {
	const model = "grok-code-fast-1"
	if got := tkRegistryMaxOutputTokens(model); got != 0 {
		t.Skipf("%s now declares a ceiling (%d); case no longer applicable", model, got)
	}
	s := NewBillingService(&config.Config{}, nil)
	big, err := s.EstimateTokenHold(model, "", "", 1000, 2_000_000, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	small, err := s.EstimateTokenHold(model, "", "", 1000, 1000, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if big <= small {
		t.Errorf("an undeclared ceiling must leave the reserve unclamped: big=%.6f small=%.6f", big, small)
	}
}

// An unknown model declares no ceiling, so the clamp must not collapse the
// reserve to zero (that would silently disable overdraft protection).
func TestRegistryMaxOutputTokens_UnknownModelIsUnbounded(t *testing.T) {
	if got := tkRegistryMaxOutputTokens("definitely-not-a-real-model-xyz"); got != 0 {
		t.Errorf("unknown model must report no ceiling, got %d", got)
	}
	s := NewBillingService(&config.Config{}, nil)
	// claude-sonnet-4 resolves via fallback pricing; a huge ceiling must still
	// scale the reserve rather than be clamped to nothing.
	big, err := s.EstimateTokenHold("claude-sonnet-4", "", "", 1000, 1_000_000, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	small, err := s.EstimateTokenHold("claude-sonnet-4", "", "", 1000, 1000, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if big <= small {
		t.Errorf("an unclamped ceiling must still scale the reserve: big=%.6f small=%.6f", big, small)
	}
}

// The tier premium lives in the priority UNIT prices that max() already takes,
// so it must not be multiplied in a second time. computeTokenBreakdown picks
// exactly one of the two.
func TestEstimateTokenHold_DoesNotDoubleCountPriorityTier(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	const prompt, maxOut = 160000, 128000

	priority, err := s.EstimateTokenHold("gpt-6-astra", "priority", "", prompt, maxOut, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	standard, err := s.EstimateTokenHold("gpt-6-astra", "", "", prompt, maxOut, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	// Priority units are already the max() for this model, so a priority request
	// reserves the same as a standard one — not 2x it.
	if priority != standard {
		t.Errorf("priority must not add a multiplier on top of priority unit prices: priority=%.6f standard=%.6f", priority, standard)
	}

	pricing, err := s.GetModelPricing("gpt-6-astra")
	if err != nil {
		t.Fatal(err)
	}
	unitIn := maxFloat(pricing.InputPricePerToken, pricing.InputPricePerTokenPriority,
		pricing.CacheCreationPricePerToken, pricing.CacheCreationPricePerTokenPriority,
		pricing.CacheCreation5mPrice, pricing.CacheCreation1hPrice)
	unitOut := maxFloat(pricing.OutputPricePerToken, pricing.OutputPricePerTokenPriority,
		pricing.ThinkingOutputPricePerToken, pricing.ImageOutputPricePerToken)
	// Long-context multipliers apply at this prompt size (threshold 272k is not
	// crossed by 160k), so the expectation is the flat product.
	want := float64(prompt)*unitIn + float64(maxOut)*unitOut
	if priority != want {
		t.Errorf("priority reserve = %.6f, want the single-premium product %.6f", priority, want)
	}
}

func TestEstimateTokenHold_ScalesWithRateMultiplier(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	h1, err := s.EstimateTokenHold("claude-sonnet-4", "", "", 1000, 500, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := s.EstimateTokenHold("claude-sonnet-4", "", "", 1000, 500, 2.0)
	if err != nil {
		t.Fatal(err)
	}
	if h2 <= h1 {
		t.Errorf("hold should scale with rate multiplier: mult=1 → %.10f, mult=2 → %.10f", h1, h2)
	}
}

func TestEstimateTokenHold_UnpricedModelErrors(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	if _, err := s.EstimateTokenHold("definitely-not-a-real-model-xyz", "", "", 100, 100, 1.0); err == nil {
		t.Error("expected an error for an unpriced model so the caller can fail-open (chat serves $0)")
	}
}

func TestEstimateImageHold_CoversFewerDeliveredImages(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	// Reserve for the requested count; actual delivers ≤ n, so hold ≥ actual.
	hold := s.EstimateImageHold("some-image-model", "2K", 4, nil, 1.0)
	actual := s.CalculateImageCost("some-image-model", "2K", 2, nil, 1.0).ActualCost
	if actual > hold {
		t.Errorf("image hold (n=4) must cover actual fewer images (n=2): hold=%.6f actual=%.6f", hold, actual)
	}
	// An omitted size tier must be priced as the dearest tier (4K), never under.
	holdEmpty := s.EstimateImageHold("some-image-model", "", 1, nil, 1.0)
	hold4K := s.EstimateImageHold("some-image-model", "4K", 1, nil, 1.0)
	if holdEmpty < hold4K {
		t.Errorf("empty size tier must reserve as 4K: empty=%.6f 4K=%.6f", holdEmpty, hold4K)
	}
}

func TestEstimateVideoHold_MatchesBilledDuration(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	hold := s.EstimateVideoHold("some-video-model", 8, 1.0, "", nil, nil)
	actual := s.CalculateVideoCost("some-video-model", VideoBillingResolution720P, 1, 8, nil, 1.0, nil).ActualCost
	if hold < actual {
		t.Errorf("video hold must be ≥ billed cost for the same duration: hold=%.6f actual=%.6f", hold, actual)
	}
}

func TestEstimateVideoHold_UsesGroupTierOverride(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	price1080P := 0.9
	groupConfig := &VideoPriceConfig{Price1080P: &price1080P}
	hold := s.EstimateVideoHold("veo-3.1-generate-001", 2, 1.5, VideoBillingResolution1080P, groupConfig, nil)
	want := price1080P * 2 * 1.5
	if hold != want {
		t.Errorf("video hold must use the settlement group tier override: hold=%.6f want=%.6f", hold, want)
	}
}

type videoHoldRepoStub struct {
	UsageBillingRepository
	command *HoldCommand
}

func (s *videoHoldRepoStub) ReserveBalanceHold(_ context.Context, command *HoldCommand) (bool, error) {
	s.command = command
	return true, nil
}

func (s *videoHoldRepoStub) ReleaseBalanceHold(context.Context, string) (bool, error) {
	return true, nil
}

func (s *videoHoldRepoStub) ReleaseExpiredBalanceHolds(context.Context, time.Time, int) (int, error) {
	return 0, nil
}

func TestTkReserveVideoHold_UsesIndependentVideoMultiplier(t *testing.T) {
	price1080P := 0.9
	ctx := context.Background()
	repo := &videoHoldRepoStub{}
	s := &OpenAIGatewayService{
		billingService:   NewBillingService(&config.Config{}, nil),
		usageBillingRepo: repo,
	}
	apiKey := &APIKey{
		ID: 2,
		Group: &Group{
			VideoPrice1080P:      &price1080P,
			VideoRateIndependent: true,
			VideoRateMultiplier:  0.4,
		},
	}

	held, reject := s.TkReserveVideoHold(
		ctx, "video-independent-rate", "veo-3.1-generate-001",
		&User{ID: 1}, apiKey, 2, VideoBillingResolution1080P, nil,
	)

	if !held || reject {
		t.Fatalf("expected hold to be reserved: held=%v reject=%v", held, reject)
	}
	if repo.command == nil {
		t.Fatal("expected hold command")
	}
	want := price1080P * 2 * 0.4
	if repo.command.Amount != want {
		t.Errorf("video hold must use independent video multiplier: amount=%.6f want=%.6f", repo.command.Amount, want)
	}
	settled := s.calculateOpenAIVideoCost(ctx, "veo-3.1-generate-001", apiKey, &OpenAIForwardResult{
		VideoCount:           1,
		VideoDurationSeconds: 2,
		VideoResolution:      VideoBillingResolution1080P,
	}, resolveVideoRateMultiplier(apiKey, 1))
	if settled == nil || repo.command.Amount != settled.ActualCost {
		t.Errorf("video hold must equal settlement: hold=%.6f settled=%v", repo.command.Amount, settled)
	}
}

func TestTkReserveVideoHold_UsesChannelResolutionTier(t *testing.T) {
	const (
		groupID = int64(100)
		model   = "veo-3.1-generate-001"
	)
	defaultPrice := 0.2
	price1080P := 0.7
	ctx := context.Background()
	cache := newEmptyChannelCache()
	cache.pricingByGroupModel[channelModelKey{groupID: groupID, model: model}] = &ChannelModelPricing{
		BillingMode:     BillingModePerRequest,
		PerRequestPrice: &defaultPrice,
		Intervals: []PricingInterval{{
			TierLabel:       VideoBillingResolution1080P,
			PerRequestPrice: &price1080P,
		}},
	}
	cache.channelByGroupID[groupID] = &Channel{ID: groupID, Status: StatusActive}
	cache.groupPlatform[groupID] = ""
	cache.loadedAt = time.Now()
	channelService := &ChannelService{}
	channelService.cache.Store(cache)
	billingService := NewBillingService(&config.Config{}, nil)
	repo := &videoHoldRepoStub{}
	s := &OpenAIGatewayService{
		billingService:   billingService,
		usageBillingRepo: repo,
		resolver:         NewModelPricingResolver(channelService, billingService),
	}
	apiKey := &APIKey{ID: 2, GroupID: i64p(groupID), Group: &Group{ID: groupID}}

	held, reject := s.TkReserveVideoHold(
		ctx, "video-channel-tier", model,
		&User{ID: 1}, apiKey, 8, VideoBillingResolution1080P, nil,
	)

	if !held || reject {
		t.Fatalf("expected hold to be reserved: held=%v reject=%v", held, reject)
	}
	if repo.command == nil {
		t.Fatal("expected hold command")
	}
	if repo.command.Amount != price1080P {
		t.Errorf("video hold must use channel per-request tier without duration scaling: amount=%.6f want=%.6f", repo.command.Amount, price1080P)
	}
	settled := s.calculateOpenAIVideoCost(ctx, model, apiKey, &OpenAIForwardResult{
		VideoCount:           1,
		VideoDurationSeconds: 8,
		VideoResolution:      VideoBillingResolution1080P,
	}, 1)
	if settled == nil || repo.command.Amount != settled.ActualCost {
		t.Errorf("video hold must equal settlement: hold=%.6f settled=%v", repo.command.Amount, settled)
	}
}

func TestTkReserveTokenHold_NilBillingServiceFailsOpen(t *testing.T) {
	s := &OpenAIGatewayService{}
	held, reject := s.TkReserveTokenHold(
		context.Background(),
		"generated:nil-billing",
		"gpt-5.4",
		"",
		"",
		&User{ID: 1},
		&APIKey{ID: 2},
		32,
		256,
	)
	if held || reject {
		t.Fatalf("nil billingService must fail open: held=%v reject=%v", held, reject)
	}
}

func TestMaxFloat(t *testing.T) {
	cases := []struct {
		in   []float64
		want float64
	}{
		{nil, 0},
		{[]float64{1, 2, 3}, 3},
		{[]float64{-1, -2}, 0}, // never below zero
		{[]float64{0.3e-6, 3.75e-6, 3e-6}, 3.75e-6},
	}
	for _, c := range cases {
		if got := maxFloat(c.in...); got != c.want {
			t.Errorf("maxFloat(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestEstimateTTSHold_MatchesCharacterSettlement(t *testing.T) {
	s := &BillingService{}
	chars := 5000
	hold := s.EstimateTTSHold("qwen-audio-3.0-tts-plus", chars, nil, 2.0)
	settle := s.CalculateAudioCostForModel("qwen-audio-3.0-tts-plus", "tts", float64(chars)/1_000_000.0, nil, 2.0)
	if hold != settle.ActualCost {
		t.Fatalf("EstimateTTSHold=%v settle=%v", hold, settle.ActualCost)
	}
	if hold <= 0 {
		t.Fatal("expected positive TTS hold for priced registry model")
	}
	if s.EstimateTTSHold("qwen-audio-3.0-tts-plus", 0, nil, 1) != 0 {
		t.Fatal("zero characters must reserve nothing")
	}
}
