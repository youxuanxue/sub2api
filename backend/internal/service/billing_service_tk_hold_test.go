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
	s := NewBillingService(&config.Config{}, nil) // embedded registry, no PricingService dependency
	const (
		model   = "claude-sonnet-4"
		prompt  = 1000 // upper bound on input tokens
		maxOut  = 500  // hard output ceiling
		mult    = 1.0
		epsilon = 1e-9
	)

	for _, tier := range []string{"", "priority", "flex"} {
		hold, err := s.EstimateTokenHold(model, tier, prompt, maxOut, mult)
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

func TestEstimateTokenHold_ScalesWithRateMultiplier(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	h1, err := s.EstimateTokenHold("claude-sonnet-4", "", 1000, 500, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := s.EstimateTokenHold("claude-sonnet-4", "", 1000, 500, 2.0)
	if err != nil {
		t.Fatal(err)
	}
	if h2 <= h1 {
		t.Errorf("hold should scale with rate multiplier: mult=1 → %.10f, mult=2 → %.10f", h1, h2)
	}
}

func TestEstimateTokenHold_UnpricedModelErrors(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	if _, err := s.EstimateTokenHold("definitely-not-a-real-model-xyz", "", 100, 100, 1.0); err == nil {
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

func TestTkReserveImageHold_RejectsUnboundedImageTokenOwner(t *testing.T) {
	repo := &videoHoldRepoStub{}
	s := &OpenAIGatewayService{
		billingService:   NewBillingService(&config.Config{}, nil),
		usageBillingRepo: repo,
	}
	apiKey := &APIKey{ID: 2, Group: &Group{ID: 10, RateMultiplier: 1}}

	held, reject := s.TkReserveImageHold(
		context.Background(), "image-token-unbounded", "gpt-image-2",
		&User{ID: 1}, apiKey, 1,
	)

	if held || !reject {
		t.Fatalf("unbounded image-token owner must fail closed: held=%v reject=%v", held, reject)
	}
	if repo.command != nil {
		t.Fatalf("unbounded image-token owner must not submit a zero hold: %+v", repo.command)
	}
}

func TestTkReserveImageHold_ZeroMultiplierNeedsNoBound(t *testing.T) {
	repo := &videoHoldRepoStub{}
	s := &OpenAIGatewayService{
		billingService:   NewBillingService(&config.Config{}, nil),
		usageBillingRepo: repo,
	}
	apiKey := &APIKey{
		ID: 2,
		Group: &Group{
			ID:                   10,
			RateMultiplier:       1,
			ImageRateIndependent: true,
			ImageRateMultiplier:  0,
			VideoRateIndependent: true,
			VideoRateMultiplier:  1,
		},
	}

	held, reject := s.TkReserveImageHold(
		context.Background(), "image-token-free", "gpt-image-2",
		&User{ID: 1}, apiKey, 1,
	)

	if held || reject {
		t.Fatalf("an intentional zero image multiplier needs no balance hold: held=%v reject=%v", held, reject)
	}
	if repo.command != nil {
		t.Fatalf("zero-cost image request must not reserve balance: %+v", repo.command)
	}
}

func TestTkReserveImageHold_GroupPricesBoundTokenOwnerAndPrecedeChannel(t *testing.T) {
	const groupID = int64(100)
	price1K, price2K, price4K := 0.2, 0.3, 0.4
	channelPrice := 0.05
	cache := newEmptyChannelCache()
	cache.pricingByGroupModel[channelModelKey{groupID: groupID, model: "gpt-image-2"}] = &ChannelModelPricing{
		BillingMode:     BillingModeImage,
		PerRequestPrice: &channelPrice,
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
	apiKey := &APIKey{
		ID:      2,
		GroupID: i64p(groupID),
		Group: &Group{
			ID:                  groupID,
			RateMultiplier:      1,
			ImageRateMultiplier: 1,
			VideoRateMultiplier: 1,
			ImagePrice1K:        &price1K,
			ImagePrice2K:        &price2K,
			ImagePrice4K:        &price4K,
		},
	}

	held, reject := s.TkReserveImageHold(
		context.Background(), "image-token-group-prices", "gpt-image-2",
		&User{ID: 1}, apiKey, 2,
	)

	if !held || reject {
		t.Fatalf("complete fixed group prices make the hold bounded: held=%v reject=%v", held, reject)
	}
	if repo.command == nil {
		t.Fatal("expected a balance hold")
	}
	want := price4K * 2
	if repo.command.Amount != want {
		t.Errorf("group image tiers must precede the cheaper channel price: amount=%.6f want=%.6f", repo.command.Amount, want)
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
