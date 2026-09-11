//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

type candidateRatesRepo struct {
	UserGroupRateRepository
	rates map[int64]float64
}

func (r candidateRatesRepo) GetByUserAndGroup(_ context.Context, _, groupID int64) (*float64, error) {
	if rate, ok := r.rates[groupID]; ok {
		return &rate, nil
	}
	return nil, nil
}

func TestCandidateBillingOriginUsesUserPriceAndIgnoresGroupOrder(t *testing.T) {
	groups := []Group{{ID: 1, SortOrder: -100, RateMultiplier: .1}, {ID: 2, SortOrder: 100, RateMultiplier: 2}}
	rates := newUserGroupRateResolver(candidateRatesRepo{rates: map[int64]float64{1: 1.5, 2: .3}}, nil, 0, nil, "")
	for _, origins := range [][]Group{groups, {groups[1], groups[0], groups[1]}} {
		selected, err := selectCandidateBillingOrigin(context.Background(), 7, &Account{ID: 115}, origins, "claude-fable-5", ShapeAnthropicMessages, nil, rates, nil)
		require.NoError(t, err)
		require.Equal(t, int64(2), selected.ID)
	}
}

func TestCandidateBillingOriginQueryFailureIsNeverTariffEquality(t *testing.T) {
	dbErr := errors.New("channel database unavailable")
	calls := 0
	repo := &mockChannelRepository{listAllFn: func(context.Context) ([]Channel, error) {
		calls++
		return nil, dbErr
	}}
	channels := NewChannelService(repo, nil, nil, nil, nil)
	groups := []Group{{ID: 1, RateMultiplier: .1}, {ID: 2, RateMultiplier: .2}}
	for range 2 {
		selected, err := selectCandidateBillingOrigin(context.Background(), 7, &Account{ID: 115}, groups, "claude-fable-5", ShapeAnthropicMessages, channels, nil, nil)
		require.Nil(t, selected)
		require.ErrorIs(t, err, dbErr)
		require.NotErrorIs(t, err, ErrCandidatePolicyConflict)
	}
	require.Equal(t, 1, calls, "short retry backoff retains the original read error")

	repo.listAllFn = func(context.Context) ([]Channel, error) { return nil, nil }
	channels.cache.Store((*channelCache)(nil))
	selected, err := selectCandidateBillingOrigin(context.Background(), 7, &Account{ID: 115}, groups, "claude-fable-5", ShapeAnthropicMessages, channels, nil, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), selected.ID)
}

func TestCandidateBillingOriginMultiplierFailureUsesOne(t *testing.T) {
	rates := newUserGroupRateResolver(&userGroupRateResolverRepoStub{err: errors.New("rate database unavailable")}, nil, 0, nil, "")
	groups := []Group{{ID: 20, RateMultiplier: .01}, {ID: 10, RateMultiplier: 100}}
	selected, err := selectCandidateBillingOrigin(context.Background(), 7, &Account{ID: 115}, groups, "claude-fable-5", ShapeAnthropicMessages, nil, rates, nil)
	require.NoError(t, err)
	require.Equal(t, int64(10), selected.ID, "both failed reads use 1; ID only breaks an equal-price accounting tie")
}

func TestCandidateBillingOriginRejectsUnequalApplicablePolicies(t *testing.T) {
	enabled, threshold := true, 180000
	price := 1.0
	for _, tc := range []struct {
		name   string
		shape  UniversalShape
		change func(*Group)
	}{
		{"token price", ShapeAnthropicMessages, func(g *Group) {
			g.ModelPricing = []ChannelModelPricing{{Models: []string{"model"}, InputPrice: &price}}
		}},
		{"compaction", ShapeAnthropicMessages, func(g *Group) {
			g.MessagesCompactionEnabled, g.MessagesCompactionInputTokensThreshold = &enabled, &threshold
		}},
		{"long context", ShapeAnthropicMessages, func(g *Group) { g.LongContextPricingEnabled = true }},
		{"image price", ShapeOpenAIImages, func(g *Group) { g.ImagePrice1K = &price }},
		{"video price", ShapeOpenAIVideo, func(g *Group) { g.VideoPrice720P = &price }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groups := []Group{{ID: 1, RateMultiplier: 1}, {ID: 2, RateMultiplier: .01}}
			tc.change(&groups[1])
			selected, err := selectCandidateBillingOrigin(context.Background(), 7, &Account{ID: 115, Platform: PlatformOpenAI}, groups, "model", tc.shape, nil, nil, nil)
			require.Nil(t, selected)
			require.ErrorIs(t, err, ErrCandidatePolicyConflict)
		})
	}
}

func TestCandidateBillingOriginComparesFreeFastSettlementPolicy(t *testing.T) {
	for _, subscription := range []string{SubscriptionTypeStandard, SubscriptionTypeSubscription} {
		t.Run(subscription, func(t *testing.T) {
			groups := []Group{
				{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1, SubscriptionType: subscription, FreeOpenAIFast: true},
				{ID: 2, Platform: PlatformOpenAI, RateMultiplier: .9, SubscriptionType: subscription},
			}
			account := &Account{ID: 115, Platform: PlatformOpenAI}
			for _, origins := range [][]Group{groups, {groups[1], groups[0]}} {
				selected, err := selectCandidateBillingOrigin(context.Background(), 7, account, origins, "gpt-5", ShapeOpenAIChat, nil, nil, nil)
				require.Nil(t, selected)
				require.ErrorIs(t, err, ErrCandidatePolicyConflict, "Fast and Standard tariffs cannot be compared using only group multipliers")
			}
			groups[1].FreeOpenAIFast = true
			selected, err := selectCandidateBillingOrigin(context.Background(), 7, account, groups, "gpt-5", ShapeOpenAIChat, nil, nil, nil)
			require.NoError(t, err)
			require.NotNil(t, selected)
		})
	}
}

func TestCandidateBillingOriginIgnoresInapplicableFreeFastPolicy(t *testing.T) {
	groups := []Group{
		{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1, FreeOpenAIFast: true},
		{ID: 2, Platform: PlatformOpenAI, RateMultiplier: .9},
	}
	account := &Account{ID: 115, Platform: PlatformAnthropic}
	selected, err := selectCandidateBillingOrigin(context.Background(), 7, account, groups, "claude-fable-5", ShapeAnthropicMessages, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), selected.ID)
}

func TestCandidateBillingOriginUsesEffectiveAccountCompactionAndApplicablePrice(t *testing.T) {
	enabled, threshold := true, 180000
	price := .01
	groups := []Group{{ID: 1, RateMultiplier: 1}, {ID: 2, RateMultiplier: .1,
		MessagesCompactionEnabled: &enabled, MessagesCompactionInputTokensThreshold: &threshold,
		ImagePrice1K: &price, DefaultMappedModel: "universal-ignores-this",
		ModelPricing: []ChannelModelPricing{{Models: []string{"other-model"}, InputPrice: &price}},
	}}
	account := &Account{ID: 115, Platform: PlatformOpenAI, Extra: map[string]any{"messages_compaction_enabled": false}}
	selected, err := selectCandidateBillingOrigin(context.Background(), 7, account, groups, "model", ShapeAnthropicMessages, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), selected.ID)
}

func TestCandidateBillingOriginMatchesChannelWildcardWithoutPricingMetadata(t *testing.T) {
	price := .01
	channels := NewChannelService(&mockChannelRepository{
		listAllFn: func(context.Context) ([]Channel, error) {
			return []Channel{{ID: 3, Status: StatusActive, GroupIDs: []int64{1}, ModelPricing: []ChannelModelPricing{{ID: 99, Platform: PlatformOpenAI, Models: []string{"gpt-*"}, InputPrice: &price}}}}, nil
		},
		getGroupPlatformsFn: func(context.Context, []int64) (map[int64]string, error) {
			return map[int64]string{1: PlatformOpenAI}, nil
		},
	}, nil, nil, nil, nil)
	groups := []Group{{ID: 1, RateMultiplier: 1}, {ID: 2, RateMultiplier: .1,
		ModelPricing: []ChannelModelPricing{{ID: 200, Models: []string{"gpt-5"}, BillingMode: BillingModeToken, InputPrice: &price}},
	}}
	selected, err := selectCandidateBillingOrigin(context.Background(), 7, &Account{ID: 63, Platform: PlatformOpenAI}, groups, "gpt-5", ShapeOpenAIChat, channels, nil, nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), selected.ID)
}

func TestCandidateBillingOriginComparesMappedVideoModelPrices(t *testing.T) {
	account := &Account{ID: 115, Platform: PlatformOpenAI, Credentials: map[string]any{
		"model_mapping": map[string]any{"public-video": "grok-imagine-video-1.5"},
	}}
	groups := []Group{
		{ID: 1, RateMultiplier: 1, VideoModelPrices: map[string]map[string]float64{"grok-imagine-video-1.5": {"480p": .1}}},
		{ID: 2, RateMultiplier: .1, VideoModelPrices: map[string]map[string]float64{"grok-imagine-video-1.5": {"480p": .5}}},
	}
	selected, err := selectCandidateBillingOrigin(context.Background(), 7, account, groups, "public-video", ShapeOpenAIVideo, nil, nil, nil)
	require.Nil(t, selected)
	require.ErrorIs(t, err, ErrCandidatePolicyConflict)
	groups[1].VideoModelPrices["grok-imagine-video-1.5"]["480p"] = .1
	selected, err = selectCandidateBillingOrigin(context.Background(), 7, account, groups, "public-video", ShapeOpenAIVideo, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), selected.ID)
}

func TestCandidateBillingOriginUsesSettlementServedModel(t *testing.T) {
	for _, shadow := range []bool{false, true} {
		t.Run(fmt.Sprintf("shadow=%v", shadow), func(t *testing.T) {
			low, high := .01, .02
			groups := []Group{
				{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1, ModelPricing: []ChannelModelPricing{{Models: []string{"qwen3.7-plus"}, InputPrice: &low}}},
				{ID: 2, Platform: PlatformOpenAI, RateMultiplier: .1, ModelPricing: []ChannelModelPricing{{Models: []string{"qwen3.7-plus"}, InputPrice: &high}}},
			}
			channels := newTestChannelService(makeStandardRepo(Channel{ID: 3, Status: StatusActive, GroupIDs: []int64{1, 2},
				ModelMapping: map[string]map[string]string{PlatformOpenAI: {"qwen-plus": "qwen3.6-plus"}},
			}, map[int64]string{1: PlatformOpenAI, 2: PlatformOpenAI}))
			parent := &Account{ID: 115, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Credentials: map[string]any{"model_mapping": map[string]any{"qwen-plus": "qwen3.7-plus"}}}
			account := parent
			repo := &stubOpenAIAccountRepo{accounts: []Account{*parent}}
			if shadow {
				account = &Account{ID: 124, ParentAccountID: &parent.ID, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
					Credentials: map[string]any{"model_mapping": map[string]any{"qwen-plus": "qwen3.6-plus"}}}
			}
			selected, err := selectCandidateBillingOrigin(context.Background(), 7, account, groups, "qwen-plus", ShapeOpenAIChat, channels, nil, repo)
			require.Nil(t, selected)
			require.ErrorIs(t, err, ErrCandidatePolicyConflict, "the served tariff must be compared even when channel or shadow mappings point elsewhere")
			groups[1].ModelPricing[0].InputPrice = &low
			selected, err = selectCandidateBillingOrigin(context.Background(), 7, account, groups, "qwen-plus", ShapeOpenAIChat, channels, nil, repo)
			require.NoError(t, err)
			require.Equal(t, int64(2), selected.ID)
		})
	}
}

func TestCandidateBillingRateUsesBillingOwnerMediaAndPeakRules(t *testing.T) {
	group := &Group{ID: 1, RateMultiplier: .5, SubscriptionType: SubscriptionTypeSubscription,
		PeakRateEnabled: true, PeakStart: "09:00", PeakEnd: "18:00", PeakRateMultiplier: 3,
		ImageRateIndependent: true, ImageRateMultiplier: .2, VideoRateIndependent: true, VideoRateMultiplier: .4}
	at := time.Date(2026, 9, 8, 10, 0, 0, 0, timezone.Location())
	for _, tc := range []struct {
		shape   UniversalShape
		pricing *ChannelModelPricing
		want    float64
	}{
		{ShapeOpenAIChat, nil, 1.5},
		{ShapeOpenAIImages, nil, .2},
		{ShapeOpenAIImages, &ChannelModelPricing{BillingMode: BillingModeToken}, 1.5},
		{ShapeOpenAIVideo, nil, .4},
	} {
		require.Equal(t, tc.want, candidateBillingRate(context.Background(), 7, group, "model", tc.shape, tc.pricing, nil, at))
	}
}

type candidateQuotaCache struct {
	balanceEligibilityCacheStub
	platformReads  atomic.Int64
	platformWrites atomic.Int64
	entry          *UserPlatformQuotaCacheEntry
	subscription   *SubscriptionCacheData
	keyRate        *APIKeyRateLimitCacheData
}

func (c *candidateQuotaCache) GetSubscriptionCache(context.Context, int64, int64) (*SubscriptionCacheData, error) {
	return c.subscription, nil
}

func (c *candidateQuotaCache) GetAPIKeyRateLimit(context.Context, int64) (*APIKeyRateLimitCacheData, error) {
	return c.keyRate, nil
}

func (c *candidateQuotaCache) GetUserPlatformQuotaCache(context.Context, int64, string) (*UserPlatformQuotaCacheEntry, bool, error) {
	c.platformReads.Add(1)
	return c.entry, true, nil
}

func (c *candidateQuotaCache) IncrUserPlatformQuotaUsageCache(context.Context, int64, string, float64, time.Duration, bool) error {
	c.platformWrites.Add(1)
	return nil
}

func TestUniversalBillingAdmissionSkipsPlatformQuotaButKeepsBalance(t *testing.T) {
	zero := 0.0
	cache := &candidateQuotaCache{balanceEligibilityCacheStub: balanceEligibilityCacheStub{balance: 10},
		entry: &UserPlatformQuotaCacheEntry{SchemaVersion: UserPlatformQuotaCacheSchemaV1, DailyLimitUSD: &zero, DailyWindowStart: currentDayStart()}}
	cfg := &config.Config{}
	cfg.Billing.MinimumBalanceReserve = .01
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, cfg, &fakeQuotaRepo{})
	t.Cleanup(svc.Stop)
	group := &Group{ID: 1, Platform: PlatformAnthropic}
	key := &APIKey{ID: 1, RoutingMode: RoutingModeUniversal, Group: group, GroupID: &group.ID}
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)
	require.Empty(t, QuotaPlatform(ctx, key))
	require.NoError(t, svc.CheckBillingEligibility(ctx, &User{ID: 7}, key, group, nil, PlatformAnthropic))
	require.Zero(t, cache.platformReads.Load())
	cache.balance = 0
	require.ErrorIs(t, svc.CheckBillingEligibility(ctx, &User{ID: 7}, key, group, nil, PlatformAnthropic), ErrInsufficientBalance)
	cache.balance = 10
	key.RoutingMode = RoutingModeDirect
	require.ErrorIs(t, svc.CheckBillingEligibility(ctx, &User{ID: 7}, key, group, nil, PlatformAnthropic), ErrUserPlatformDailyQuotaExhausted)
	require.Positive(t, cache.platformReads.Load())
}

func TestCandidateBillingRevalidationPreservesBudgetWithoutCountingRPM(t *testing.T) {
	cache := &balanceEligibilityCacheStub{balance: 10}
	rpm := &userRPMCacheStub{}
	svc := NewBillingCacheService(cache, nil, nil, nil, rpm, nil, &config.Config{}, nil)
	t.Cleanup(svc.Stop)
	user, group := &User{ID: 7, RPMLimit: 100}, &Group{ID: 2, RPMLimit: 100}
	key := &APIKey{ID: 1, RoutingMode: RoutingModeUniversal, Quota: 5}
	for range 3 {
		require.NoError(t, svc.CheckCandidateBillingEligibility(context.Background(), user, key, group, nil))
	}
	require.Zero(t, atomic.LoadInt32(&rpm.userCalls))
	require.Zero(t, atomic.LoadInt32(&rpm.userGroupCalls))
	require.NoError(t, svc.CheckBillingEligibility(context.Background(), user, key, group, nil, ""))
	require.Equal(t, int32(1), atomic.LoadInt32(&rpm.userCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&rpm.userGroupCalls))
	key.QuotaUsed = 5
	require.ErrorIs(t, svc.CheckCandidateBillingEligibility(context.Background(), user, key, group, nil), ErrAPIKeyQuotaExhausted)
}

func TestCandidateBillingRevalidationKeepsSubscriptionAndKeyWindows(t *testing.T) {
	limit := 1.0
	cache := &candidateQuotaCache{subscription: &SubscriptionCacheData{Status: SubscriptionStatusActive,
		ExpiresAt: time.Now().Add(time.Hour), DailyUsage: 1}}
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(svc.Stop)
	user := &User{ID: 7}
	group := &Group{ID: 2, SubscriptionType: SubscriptionTypeSubscription, DailyLimitUSD: &limit}
	key := &APIKey{ID: 1, RoutingMode: RoutingModeUniversal, Group: group, GroupID: &group.ID}
	subscription := &UserSubscription{ID: 3}
	require.ErrorIs(t, svc.CheckCandidateBillingEligibility(context.Background(), user, key, group, subscription), ErrDailyLimitExceeded)
	cache.subscription.DailyUsage = 0
	require.NoError(t, svc.CheckCandidateBillingEligibility(context.Background(), user, key, group, subscription))
	key.RateLimit5h = 1
	cache.keyRate = &APIKeyRateLimitCacheData{Usage5h: 1, Window5h: time.Now().Unix()}
	require.Error(t, svc.CheckCandidateBillingEligibility(context.Background(), user, key, group, subscription))
	require.Zero(t, cache.platformReads.Load())
}

func TestCandidateBillingOwnReservationCoversAdmissionAndReselection(t *testing.T) {
	r, _, key := globalCandidateFixture([]Group{grp(10, PlatformOpenAI, 1, false), grp(20, PlatformOpenAI, 2, false)},
		[]Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 2, 20)})
	key.ID = 77
	cache := &balanceEligibilityCacheStub{balance: 10}
	billing := NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(billing.Stop)
	r.candidateGateway.billingCacheService = billing
	ctx, state := prepareGlobalCandidate(t, r, key)
	SetCandidateBalanceReserved(ctx, true)
	cache.balance = 0 // This request's reserve can consume the entire wallet.
	require.NoError(t, billing.CheckBillingEligibility(ctx, key.User, key, key.Group, nil, ""))
	otherKey := *key
	otherKey.ID++
	require.ErrorIs(t, billing.CheckCandidateBillingEligibility(ctx, key.User, &otherKey, key.Group, nil), ErrInsufficientBalance)
	otherUser := &User{ID: key.UserID + 1}
	require.ErrorIs(t, billing.CheckCandidateBillingEligibility(ctx, otherUser, key, key.Group, nil), ErrInsufficientBalance)
	key.Quota, key.QuotaUsed = 1, 1
	require.ErrorIs(t, billing.CheckCandidateBillingEligibility(ctx, key.User, key, key.Group, nil), ErrAPIKeyQuotaExhausted)
	key.QuotaUsed = 0
	hookCalls := 0
	SetCandidateBillingHook(ctx, func() error {
		hookCalls++
		SetCandidateBalanceReserved(ctx, false)
		SetCandidateBalanceReserved(ctx, true)
		return nil
	})
	selection, err := r.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", "gpt-5.4", map[int64]struct{}{1: {}}, "", key.UserID)
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID)
	require.Equal(t, 1, hookCalls)
	require.True(t, state.balanceReserved)
	selection.ReleaseFunc()

	// Hand-off ends this turn's coverage even while its asynchronous bill is
	// queued. A new WebSocket turn must satisfy its own wallet admission.
	SetCandidateBalanceReserved(ctx, false)
	err = state.RevalidateTurn(ctx, 2, "gpt-5.4", []byte(`{"model":"gpt-5.4","input":"next turn"}`))
	require.ErrorIs(t, err, ErrInsufficientBalance)
}

func TestCandidateBillingFailedRebindRestoresPathWithoutRevivingReleasedHold(t *testing.T) {
	for _, failure := range []string{"release", "reserve", "reserve outage"} {
		t.Run(failure, func(t *testing.T) {
			r, _, key := globalCandidateFixture([]Group{grp(10, PlatformOpenAI, 1, false), grp(20, PlatformOpenAI, 2, false)},
				[]Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 2, 20)})
			key.ID = 77
			cache := &balanceEligibilityCacheStub{balance: 10}
			billing := NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
			t.Cleanup(billing.Stop)
			r.candidateGateway.billingCacheService = billing
			var released []int64
			r.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{releasedIDs: &released})
			ctx, state := prepareGlobalCandidate(t, r, key)
			previous := state.current
			var observed []int64
			state.SetBindingObserver(func(_ context.Context, group *Group, _ *UserSubscription) { observed = append(observed, group.ID) })
			SetCandidateBalanceReserved(ctx, true)
			cache.balance = 0
			releaseFailure := errors.New("release failed")
			SetCandidateBillingHook(ctx, func() error {
				if failure == "release" {
					return releaseFailure
				}
				SetCandidateBalanceReserved(ctx, false)
				if failure == "reserve" {
					return ErrInsufficientBalance
				}
				return nil
			})
			selected, err := r.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", "gpt-5.4", map[int64]struct{}{1: {}}, "", key.UserID)
			require.Nil(t, selected)
			if failure == "release" {
				require.ErrorIs(t, err, releaseFailure)
				require.True(t, state.balanceReserved)
			} else {
				require.ErrorIs(t, err, ErrInsufficientBalance)
				require.False(t, state.balanceReserved)
			}
			require.Same(t, previous, state.current)
			require.Equal(t, int64(10), *key.GroupID)
			require.Equal(t, []int64{10, 20, 10}, observed)
			require.Equal(t, []int64{2}, released)
		})
	}
}

func TestUniversalUsageBillingNeverRevivesGroupPlatformQuota(t *testing.T) {
	for _, gateway := range []string{"gateway", "openai"} {
		for _, mode := range []string{RoutingModeUniversal, RoutingModeDirect} {
			t.Run(gateway+"/"+mode, func(t *testing.T) {
				limit := 10.0
				cache := &candidateQuotaCache{entry: &UserPlatformQuotaCacheEntry{DailyLimitUSD: &limit}}
				billingCache := &BillingCacheService{cache: cache, cfg: &config.Config{}}
				usageRepo := &openAIRecordUsageLogRepoStub{}
				billingRepo := &openAIRecordUsageBillingRepoStub{}
				group := &Group{ID: 2, Platform: PlatformOpenAI, RateMultiplier: .5}
				key := &APIKey{ID: 1, RoutingMode: mode, Group: group, GroupID: &group.ID, Quota: 100}
				user, account := &User{ID: 7}, &Account{ID: 63, Platform: PlatformOpenAI}
				requestID := fmt.Sprintf("candidate-quota-%s-%s", gateway, mode)
				if gateway == "gateway" {
					svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, nil, nil)
					svc.billingCacheService, svc.userPlatformQuotaRepo = billingCache, &fakeQuotaRepo{}
					svc.cfg.Database.UserPlatformQuotaFlusherEnabled = true
					err := svc.RecordUsage(context.Background(), &RecordUsageInput{APIKey: key, User: user, Account: account,
						APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{}, QuotaPlatform: PlatformAnthropic,
						Result: &ForwardResult{RequestID: requestID, Model: "gpt-4", Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 10}}})
					require.NoError(t, err)
				} else {
					svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, nil, nil, nil)
					svc.billingCacheService, svc.userPlatformQuotaRepo = billingCache, &fakeQuotaRepo{}
					svc.cfg.Database.UserPlatformQuotaFlusherEnabled = true
					err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{APIKey: key, User: user, Account: account,
						APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{}, QuotaPlatform: PlatformAnthropic,
						Result: &OpenAIForwardResult{RequestID: requestID, Model: "gpt-4", Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 10}}})
					require.NoError(t, err)
				}
				require.Positive(t, billingRepo.lastCmd.BalanceCost)
				require.Equal(t, billingRepo.lastCmd.BalanceCost, billingRepo.lastCmd.APIKeyQuotaCost)
				if mode == RoutingModeUniversal {
					require.Zero(t, cache.platformReads.Load())
					require.Zero(t, cache.platformWrites.Load())
				} else {
					require.Equal(t, int64(1), cache.platformWrites.Load())
				}
			})
		}
	}
}

func TestCandidateBillingSelectedOriginReachesHoldAndSettlement(t *testing.T) {
	for _, mode := range []string{RoutingModeUniversal, RoutingModeDirect} {
		t.Run(mode, func(t *testing.T) {
			groups := []Group{grp(10, PlatformAnthropic, -100, false), grp(20, PlatformGemini, 100, false)}
			groups[0].RateMultiplier, groups[1].RateMultiplier = .1, 2
			rates := candidateRatesRepo{rates: map[int64]float64{10: 1.5, 20: .3}}
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			billingRepo := &openAIRecordUsageBillingRepoStub{}
			holds := &videoHoldRepoStub{UsageBillingRepository: billingRepo}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, holds, nil, nil, rates)
			cache := &balanceEligibilityCacheStub{balance: 100}
			billing := NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
			t.Cleanup(billing.Stop)
			r, repo, key := globalCandidateFixture(groups, []Account{globalCandidateAccount(115, 1, 10, 20)})
			r.candidateGateway.userGroupRateResolver = svc.userGroupRateResolver
			r.candidateGateway.billingCacheService = billing
			svc.accountRepo = repo
			r.candidateOpenAI = svc
			key.ID, key.Quota = 77, 100
			wantGroup, wantRate := int64(20), .3
			if mode == RoutingModeDirect {
				key.RoutingMode, key.Group, key.GroupID = mode, &groups[0], &groups[0].ID
				wantGroup, wantRate = 10, 1.5
			}
			body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
			ctx, _, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-4", body, "", "")
			require.NoError(t, err)
			require.Equal(t, wantGroup, *key.GroupID)
			requestID := "candidate-billing-" + mode
			held, reject := svc.TkReserveTokenHold(ctx, requestID, "gpt-4", "", key.User, key, 1000, 10)
			require.True(t, held)
			require.False(t, reject)
			wantHold, err := svc.billingService.EstimateTokenHold("gpt-4", "", 1000, 10, wantRate)
			require.NoError(t, err)
			require.InDelta(t, wantHold, holds.command.Amount, 1e-9)
			SetCandidateBalanceReserved(ctx, true)
			cache.balance = 0
			selected, err := r.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", "gpt-4", nil, "", key.UserID)
			require.NoError(t, err)
			defer selected.ReleaseFunc()
			require.Equal(t, int64(115), selected.Account.ID)
			require.Equal(t, wantGroup, *key.GroupID)
			usage := OpenAIUsage{InputTokens: 1000, OutputTokens: 10}
			err = svc.RecordUsage(ctx, &OpenAIRecordUsageInput{APIKey: key, User: key.User, Account: selected.Account,
				APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{}, TkHoldRequestID: requestID,
				Result: &OpenAIForwardResult{RequestID: requestID, Model: "gpt-4", Usage: usage}})
			require.NoError(t, err)
			require.Equal(t, wantGroup, *usageRepo.lastLog.GroupID)
			require.Equal(t, wantRate, usageRepo.lastLog.RateMultiplier)
			require.Equal(t, requestID, billingRepo.lastCmd.TkHoldRequestID)
			require.InDelta(t, expectedOpenAICost(t, svc, "gpt-4", usage, wantRate).ActualCost, billingRepo.lastCmd.BalanceCost, 1e-9)
			require.Equal(t, billingRepo.lastCmd.BalanceCost, billingRepo.lastCmd.APIKeyQuotaCost)
		})
	}
}

func TestCandidateRebindingEnforcesDestinationRPM(t *testing.T) {
	groups := []Group{grp(10, PlatformOpenAI, 1, false), grp(20, PlatformOpenAI, 2, false)}
	groups[0].RPMLimit, groups[1].RPMLimit = 10, 1
	r, _, key := globalCandidateFixture(groups, []Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 2, 20)})
	key.User.RPMLimit = 10
	rpm := &userRPMCacheStub{userGroupCounts: []int{1, 2}}
	billing := NewBillingCacheService(&balanceEligibilityCacheStub{balance: 10}, nil, nil, nil, rpm, nil, &config.Config{}, nil)
	t.Cleanup(billing.Stop)
	r.candidateGateway.billingCacheService = billing
	ctx, state := prepareGlobalCandidate(t, r, key)
	require.NoError(t, billing.CheckBillingEligibility(ctx, key.User, key, key.Group, nil, ""))
	for range 2 {
		selected, err := state.selectAccount(ctx, candidateSelectOptions{acquire: true})
		require.NoError(t, err)
		selected.ReleaseFunc()
	}
	selected, err := state.selectAccount(ctx, candidateSelectOptions{acquire: true, excluded: map[int64]struct{}{1: {}}})
	if selected != nil && selected.ReleaseFunc != nil {
		selected.ReleaseFunc()
	}
	require.ErrorIs(t, err, ErrGroupRPMExceeded)
	require.Nil(t, selected)
	require.EqualValues(t, 1, atomic.LoadInt32(&rpm.userCalls), "reselection must not count the user request again")
	require.EqualValues(t, 2, atomic.LoadInt32(&rpm.userGroupCalls), "each attempted origin is counted once")
	require.Equal(t, int64(10), *key.GroupID)
}

type candidateRPMCache struct {
	userRPMCacheStub
	counts map[int64]int
}

func (c *candidateRPMCache) GetUserGroupRPM(_ context.Context, _, groupID int64) (int, error) {
	return c.counts[groupID], nil
}

func TestCandidateRPMUsesAvailableOriginAndResetsEachTurn(t *testing.T) {
	groups := []Group{grp(10, PlatformOpenAI, 1, false), grp(20, PlatformOpenAI, 2, false)}
	groups[0].RPMLimit, groups[1].RPMLimit = 1, 10
	groups[0].RateMultiplier, groups[1].RateMultiplier = .1, 1
	r, _, key := globalCandidateFixture(groups, []Account{globalCandidateAccount(1, 1, 10, 20)})
	key.User.RPMLimit = 10
	rpm := &candidateRPMCache{counts: map[int64]int{10: 1}}
	billing := NewBillingCacheService(&balanceEligibilityCacheStub{balance: 10}, nil, nil, nil, rpm, nil, &config.Config{}, nil)
	t.Cleanup(billing.Stop)
	r.candidateGateway.billingCacheService = billing
	ctx, state := prepareGlobalCandidate(t, r, key)
	require.Equal(t, int64(20), *key.GroupID, "the cheaper exhausted origin cannot hide a usable path")
	require.Zero(t, atomic.LoadInt32(&rpm.userGroupCalls), "candidate projection does not consume RPM")
	for range 2 {
		require.NoError(t, billing.CheckBillingEligibility(ctx, key.User, key, key.Group, nil, ""))
	}
	require.EqualValues(t, 1, atomic.LoadInt32(&rpm.userCalls))
	require.EqualValues(t, 1, atomic.LoadInt32(&rpm.userGroupCalls))
	rpm.counts[10] = 0
	require.NoError(t, state.RevalidateTurn(ctx, 1, "gpt-5.4", []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"next"}]}`)))
	require.Equal(t, int64(10), *key.GroupID)
	require.NoError(t, billing.CheckBillingEligibility(ctx, key.User, key, key.Group, nil, ""))
	require.EqualValues(t, 2, atomic.LoadInt32(&rpm.userCalls))
	require.EqualValues(t, 2, atomic.LoadInt32(&rpm.userGroupCalls))
}

// Controlled prices are inputs; the arithmetic oracle never calls the resolver.
func TestCandidateBillingPriceChangeBetweenHoldAndSettlement(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	const model = "ssot-price-change"
	for _, mode := range []string{RoutingModeDirect, RoutingModeUniversal} {
		for _, scale := range []float64{0.5, 2} {
			t.Run(fmt.Sprintf("%s/price-scale-%g", mode, scale), func(t *testing.T) {
				publish := func(input, output float64) {
					envelope := registryEnvelopeForTest(t, func(registry map[string]any) {
						registry[model] = map[string]any{"mode": "chat", "litellm_provider": "test",
							"input_cost_per_token": input, "output_cost_per_token": output}
					}, nil)
					rebuildTKOverlayUnion([]byte(envelope))
				}
				publish(.001, .002)
				groups := []Group{grp(10, PlatformOpenAI, 0, false), grp(20, PlatformOpenAI, 1, false)}
				rates := candidateRatesRepo{rates: map[int64]float64{10: 1, 20: .5}}
				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				billingRepo := &openAIRecordUsageBillingRepoStub{}
				holds := &videoHoldRepoStub{UsageBillingRepository: billingRepo}
				svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, holds, nil, nil, rates)
				svc.billingService = NewBillingService(nil, &PricingService{})
				// Share the production multiplier cache across admission and settlement.
				svc.userGroupRateResolver = newUserGroupRateResolver(rates, nil, 0, nil, "")
				account := globalCandidateAccount(115, 1, 10, 20)
				account.Credentials["model_mapping"] = map[string]any{model: model}
				r, repo, key := globalCandidateFixture(groups, []Account{account})
				r.candidateGateway.userGroupRateResolver = svc.userGroupRateResolver
				r.candidateOpenAI = svc
				svc.accountRepo = repo
				key.ID, key.Quota = 77, 100
				groupID, beforeRate := int64(20), .5
				if mode == RoutingModeDirect {
					key.RoutingMode, key.Group, key.GroupID = mode, &groups[0], &groups[0].ID
					groupID, beforeRate = 10, 1
				}
				body := []byte(`{"model":"ssot-price-change","messages":[{"role":"user","content":"hi"}]}`)
				ctx, _, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", model, body, "", "")
				require.NoError(t, err)
				require.Equal(t, groupID, *key.GroupID)
				held, reject := svc.TkReserveTokenHold(ctx, "price-change", model, "", key.User, key, 100, 10)
				require.True(t, held)
				require.False(t, reject)
				require.InDelta(t, .12*beforeRate, holds.command.Amount, 1e-9)
				publish(.001*scale, .002*scale)
				rates.rates[groupID] = beforeRate * scale
				require.Equal(t, beforeRate, svc.ResolveUserGroupRateMultiplier(ctx, key.UserID, groupID, 1), "cached rate remains valid until invalidation/expiry")
				// Model an expired entry without a wall-clock sleep.
				svc.userGroupRateResolver.cache.Delete(fmt.Sprintf("%d:%d", key.UserID, groupID))
				err = svc.RecordUsage(ctx, &OpenAIRecordUsageInput{APIKey: key, User: key.User, Account: &account,
					APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{}, TkHoldRequestID: "price-change",
					Result: &OpenAIForwardResult{RequestID: "price-change", Model: model,
						Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 10}}})
				require.NoError(t, err)
				require.Equal(t, groupID, *usageRepo.lastLog.GroupID)
				require.Equal(t, beforeRate*scale, usageRepo.lastLog.RateMultiplier)
				require.InDelta(t, .12*beforeRate*scale*scale, billingRepo.lastCmd.BalanceCost, 1e-9)
				require.Equal(t, "price-change", billingRepo.lastCmd.TkHoldRequestID)
				require.Equal(t, billingRepo.lastCmd.BalanceCost, billingRepo.lastCmd.APIKeyQuotaCost)
			})
		}
	}
}
