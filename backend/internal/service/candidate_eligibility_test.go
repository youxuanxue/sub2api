//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func wireCandidateTestResolver(resolver *UniversalRoutingResolver, gateway *GatewayService, accounts []Account) {
	repo := groupAwareStubOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: accounts}}
	gateway.accountRepo = repo
	openai := &OpenAIGatewayService{accountRepo: repo}
	resolver.router = NewProtocolRouter()
	resolver.candidateEvaluator = func(ctx context.Context, group Group, model string, shape UniversalShape) (GroupCandidateEligibility, error) {
		group.Hydrated = true
		return gateway.evaluateGroupCandidates(ctx, openai, group, model, shape)
	}
}

func candidateGoogleFixture(t *testing.T) (*UniversalRoutingResolver, *GatewayService, []Account, protocolrouter.CanonicalRequest) {
	t.Helper()
	const model = "gemini-3.8-flash"
	accounts := []Account{
		{
			ID: 74, GroupIDs: []int64{16}, Platform: PlatformNewAPI, ChannelType: 41,
			Type: AccountTypeServiceAccount, Status: StatusActive, Schedulable: true,
			Credentials: map[string]any{
				"service_account_json": `{"type":"service_account","project_id":"vertex-project","private_key_id":"test","private_key":"unused","client_email":"test@vertex-project.iam.gserviceaccount.com"}`,
				"location":             "us-central1", "model_mapping": map[string]any{model: model},
			},
		},
		{
			ID: 62, GroupIDs: []int64{21}, Platform: PlatformAntigravity,
			Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Credentials: map[string]any{
				"base_url": "https://api-us4.tokenkey.dev", "api_key": "test-only",
				"model_mapping": map[string]any{model: model + "-medium"},
			},
		},
	}
	for i := range accounts {
		attachTestProtocolCapability(&accounts[i], protocolrouter.ProtocolGeminiGenerateContent)
	}
	gateway := &GatewayService{
		accountRepo: groupAwareStubOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: accounts}},
	}
	resolver := NewUniversalRoutingResolver(&stubSpanLister{groups: []Group{
		grp(16, PlatformNewAPI, 40, false), grp(21, PlatformAntigravity, 50, false),
	}})
	wireCandidateTestResolver(resolver, gateway, accounts)
	request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolChatCompletions, RequestedModel: model,
		Profile: protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText},
		Body:    []byte(`{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"OK"}]}`),
	})
	require.NoError(t, err)
	return resolver, gateway, accounts, request
}

func resolveCandidateTest(r *UniversalRoutingResolver, request protocolrouter.CanonicalRequest, force string) (*Group, error) {
	ctx := r.WithRequest(context.Background(), ShapeOpenAIChat, "/v1/chat/completions", request.RequestedModel(), request.Body())
	return r.Resolve(ctx, universalKey(1), ShapeOpenAIChat, request.RequestedModel(), force)
}

func TestCandidateEligibilityGoogleBackends(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Account)
	}{
		{"disabled", func(a *Account) { a.Schedulable = false }},
		{"error", func(a *Account) { a.Status = StatusError }},
		{"temporarily_unschedulable", func(a *Account) { until := time.Now().Add(time.Minute); a.TempUnschedulableUntil = &until }},
		{"rate_limited", func(a *Account) { reset := time.Now().Add(time.Hour); a.RateLimitResetAt = &reset }},
		{"model_cooled", func(a *Account) {
			model := "gemini-3.8-flash"
			if a.Platform == PlatformAntigravity {
				model += "-medium"
			}
			a.Extra = map[string]any{"model_rate_limits": map[string]any{model: map[string]any{
				"rate_limit_reset_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			}}}
		}},
		{"missing_protocol_evidence", func(a *Account) { attachTestProtocolCapability(a) }},
		{"unsupported_model", func(a *Account) {
			a.Credentials["model_mapping"] = map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"}
		}},
	} {
		for unavailable := range 2 {
			t.Run(fmt.Sprintf("%s/account=%d", test.name, unavailable), func(t *testing.T) {
				resolver, gateway, accounts, request := candidateGoogleFixture(t)
				test.mutate(&accounts[unavailable])
				wireCandidateTestResolver(resolver, gateway, accounts)
				group, err := resolveCandidateTest(resolver, request, "")
				require.NoError(t, err)
				require.Equal(t, accounts[1-unavailable].GroupIDs[0], group.ID)
			})
		}
	}
}

func TestCandidateEligibilityCapacityIsNotEntitlement(t *testing.T) {
	resolver, gateway, accounts, request := candidateGoogleFixture(t)
	for i := range accounts {
		accounts[i].Schedulable = false
	}
	wireCandidateTestResolver(resolver, gateway, accounts)
	group, err := resolveCandidateTest(resolver, request, "")
	require.Nil(t, group)
	require.ErrorIs(t, err, ErrUniversalCapacityUnavailable)
	for i := range accounts {
		accounts[i].Credentials["model_mapping"] = map[string]any{"other": "other"}
	}
	wireCandidateTestResolver(resolver, gateway, accounts)
	_, err = resolveCandidateTest(resolver, request, "")
	require.ErrorIs(t, err, ErrUniversalNoEntitledGroup)
}

func TestCandidateEligibilityNativeAndConverterEqual(t *testing.T) {
	accounts := []Account{*protocolRoutingOpenAIAccount(101, "chat_completions"), *protocolRoutingOpenAIAccount(102, "responses")}
	for i := range accounts {
		accounts[i].GroupIDs = []int64{int64(i + 1)}
	}
	for _, convertedFirst := range []bool{false, true} {
		groups := []Group{grp(1, PlatformOpenAI, 1, false), grp(2, PlatformOpenAI, 2, false)}
		want := int64(1)
		if convertedFirst {
			groups[1].SortOrder = 0
			want = 2
		}
		resolver := NewUniversalRoutingResolver(&stubSpanLister{groups: groups})
		wireCandidateTestResolver(resolver, &GatewayService{}, accounts)
		request := protocolRoutingTestRequest(t, protocolrouter.ProtocolChatCompletions)
		group, err := resolveCandidateTest(resolver, request, "")
		require.NoError(t, err)
		require.Equal(t, want, group.ID)
	}
}

func TestCandidateEligibilityUnknownCapabilityIsNotEntitlement(t *testing.T) {
	resolver, gateway, accounts, request := candidateGoogleFixture(t)
	for i := range accounts {
		accounts[i].ProtocolEndpointCapability = nil
		accounts[i].ProtocolEndpointCapabilityID = nil
	}
	wireCandidateTestResolver(resolver, gateway, accounts)
	_, err := resolveCandidateTest(resolver, request, "")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUniversalNoEntitledGroup)
}

func TestCandidateEligibilitySaturationPreservesBillingTier(t *testing.T) {
	for _, test := range []struct {
		name              string
		firstSubscription bool
		counts            map[int64]int64
		want              int64
	}{
		{"healthy", false, nil, 1},
		{"transient", false, map[int64]int64{101: 2}, 1},
		{"saturated", false, map[int64]int64{101: 3}, 2},
		{"all_saturated", false, map[int64]int64{101: 3, 102: 3}, 1},
		{"subscription_before_soft_health", true, map[int64]int64{101: 3}, 1},
		{"expired", false, nil, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			accounts := []Account{*protocolRoutingOpenAIAccount(101, "chat_completions"), *protocolRoutingOpenAIAccount(102, "responses")}
			for i := range accounts {
				accounts[i].GroupIDs = []int64{int64(i + 1)}
				accounts[i].Credentials["base_url"] = "https://api-us4.tokenkey.dev"
				attachTestProtocolCapability(&accounts[i], protocolrouter.ProtocolResponses)
			}
			resolver := NewUniversalRoutingResolver(&stubSpanLister{groups: []Group{
				grp(1, PlatformOpenAI, 1, test.firstSubscription), grp(2, PlatformOpenAI, 2, false),
			}})
			gateway := &GatewayService{rateLimitService: &RateLimitService{openaiSaturationCounter: &fakeSaturationCache{counts: test.counts}}}
			wireCandidateTestResolver(resolver, gateway, accounts)
			group, err := resolveCandidateTest(resolver, protocolRoutingTestRequest(t, protocolrouter.ProtocolChatCompletions), "")
			require.NoError(t, err)
			require.Equal(t, test.want, group.ID)
		})
	}
}

func TestCandidateEligibilityMixedPoolUsesSchedulerMembership(t *testing.T) {
	resolver, gateway, accounts, request := candidateGoogleFixture(t)
	accounts = accounts[1:]
	accounts[0].GroupIDs = []int64{10}
	accounts[0].Extra = map[string]any{"mixed_scheduling": true}
	resolver = NewUniversalRoutingResolver(&stubSpanLister{groups: []Group{grp(10, PlatformGemini, 1, false)}})
	wireCandidateTestResolver(resolver, gateway, accounts)
	group, err := resolveCandidateTest(resolver, request, "")
	require.NoError(t, err)
	require.Equal(t, int64(10), group.ID)
}

func TestCandidateEligibilityRequestFeaturesAndPath(t *testing.T) {
	resolver := &UniversalRoutingResolver{router: NewProtocolRouter()}
	ctx := resolver.WithRequest(context.Background(), ShapeOpenAIChat, "/v1/responses/compact", "gpt-5.4",
		[]byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"lookup"}],"previous_response_id":"resp_1","input":"hi"}`))
	request, ok := ProtocolRoutingRequest(ctx)
	require.True(t, ok)
	require.Equal(t, protocolrouter.ResponsesPathCompact, request.ResponsesPath())
	require.True(t, request.Profile().Tools)
	require.Equal(t, protocolrouter.ContinuationPreviousResponse, request.Profile().Continuation)
	require.False(t, ProtocolRouteLegal(ctx, protocolRoutingOpenAIAccount(1, "chat_completions"), "gpt-5.4"))
}

func TestCandidateEligibilityStateReadFailureKeepsBaseOrder(t *testing.T) {
	state := candidateSaturationState{anthropic: &fakeSaturationCache{getErr: errors.New("redis unavailable")}}
	counts := state.counts(context.Background(), []*Account{{ID: 1, Platform: PlatformAnthropic}}, "claude-sonnet-4-6")
	require.False(t, candidateSaturated(counts[1]))
}

type candidateAntigravityCounter struct {
	counts map[AntigravitySaturationScope]int64
}

func (c *candidateAntigravityCounter) IncrementSaturation(_ context.Context, id int64, model string, _ int) (int64, error) {
	scope := AntigravitySaturationScope{id, model}
	c.counts[scope]++
	return c.counts[scope], nil
}

func (c *candidateAntigravityCounter) GetSaturationBatch(_ context.Context, scopes []AntigravitySaturationScope) (map[AntigravitySaturationScope]int64, error) {
	out := make(map[AntigravitySaturationScope]int64)
	for _, scope := range scopes {
		out[scope] = c.counts[scope]
	}
	return out, nil
}

func TestCandidateEligibilityAntigravitySaturationScopeAndParity(t *testing.T) {
	resolver, gateway, accounts, request := candidateGoogleFixture(t)
	resolver.lister = &stubSpanLister{groups: []Group{grp(16, PlatformNewAPI, 40, false), grp(21, PlatformAntigravity, 0, false)}}
	cache := &candidateAntigravityCounter{counts: map[AntigravitySaturationScope]int64{}}
	gateway.rateLimitService = &RateLimitService{antigravitySaturationCounter: cache}
	for _, test := range []struct {
		name, model string
		wantGroup   int64
		penalized   bool
	}{
		{"different_model", "gemini-2.5-flash", 21, false},
		{"public_name_is_not_scope", "gemini-3.8-flash", 21, false},
		{"resolved_model", "gemini-3.8-flash-medium", 16, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cache.counts = map[AntigravitySaturationScope]int64{{accounts[1].ID, test.model}: 3}
			group, err := resolveCandidateTest(resolver, request, "")
			require.NoError(t, err)
			require.Equal(t, test.wantGroup, group.ID)
			candidates := []accountWithLoad{{account: &accounts[1]}}
			gateway.computeAnthropicSaturationPenalties(context.Background(), candidates, request.RequestedModel())
			require.Equal(t, test.penalized, candidates[0].saturationPenalty > 0)
			require.Equal(t, test.penalized, gateway.tkShouldClearStickyForSaturation(context.Background(), &accounts[1], "session", request.RequestedModel()))
			require.True(t, accounts[1].IsSchedulableForModelWithContext(context.Background(), request.RequestedModel()), "preference cannot become a cooldown")
		})
	}
	group, err := resolveCandidateTest(resolver, request, PlatformAntigravity)
	require.NoError(t, err)
	require.Equal(t, int64(21), group.ID, "a saturated pool remains a forced last resort")
}

func TestCandidateEligibilityEmptyPoolClassificationIncludesCompatPlatforms(t *testing.T) {
	for _, platform := range OpenAICompatPlatforms() {
		account := &Account{Platform: platform, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"base_url": "https://api-us4.tokenkey.dev", "api_key": "test-only"}}
		require.True(t, tkSkipOpenAIDownstreamCapacityPenalty(account, 429, "No available accounts", nil), platform)
		require.False(t, tkSkipOpenAIDownstreamCapacityPenalty(account, 429, "Quota exceeded", nil), platform)
	}
}

func TestCandidateEligibilityHardGatesPrecedeWindowRecovery(t *testing.T) {
	const model = "claude-sonnet-4-6"
	account := anthropicHotAccount(1, time.Now())
	account.ID, account.Status, account.Schedulable = 1, StatusActive, true
	account.GroupIDs = []int64{7}
	account.Credentials = map[string]any{"model_mapping": map[string]any{model: model}}
	account.Extra["base_rpm"] = 1
	ctx := context.WithValue(context.Background(), rpmPrefetchContextKey, map[int64]int{1: 100})
	gateway := &GatewayService{accountRepo: groupAwareStubOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: []Account{*account}}}}
	require.Empty(t, gateway.gatewayCandidates(ctx, []Account{*account}, PlatformAnthropic, false, model, nil),
		"window recovery cannot reintroduce an RPM-blocked account")
	group := grp(7, PlatformAnthropic, 1, false)
	group.Hydrated = true
	state, err := gateway.evaluateGroupCandidates(ctx, nil, group, model, ShapeAnthropicMessages)
	require.NoError(t, err)
	require.True(t, state.Supported)
	require.False(t, state.Available)
	account.Type = AccountTypeAPIKey
	account.Extra = map[string]any{"quota_limit": float64(1), "quota_used": float64(2)}
	gateway.accountRepo = groupAwareStubOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: []Account{*account}}}
	state, err = gateway.evaluateGroupCandidates(context.Background(), nil, group, model, ShapeAnthropicMessages)
	require.NoError(t, err)
	require.True(t, state.Supported)
	require.False(t, state.Available, "quota rejection must match the direct filter")
}

func TestCandidateEligibilityProductionWiring(t *testing.T) {
	resolver, gateway, accounts, request := candidateGoogleFixture(t)
	for i := range resolver.lister.(*stubSpanLister).groups {
		resolver.lister.(*stubSpanLister).groups[i].Hydrated = true
	}
	api := &APIKeyService{universalResolver: resolver}
	openai := &OpenAIGatewayService{accountRepo: gateway.accountRepo}
	resolver.candidateEvaluator, resolver.router = nil, nil
	ProvideTKUniversalModelsProvider(api, gateway, nil, openai, NewProtocolRouter())
	group, err := resolveCandidateTest(resolver, request, PlatformAntigravity)
	require.NoError(t, err)
	require.Equal(t, accounts[1].GroupIDs[0], group.ID)
}
