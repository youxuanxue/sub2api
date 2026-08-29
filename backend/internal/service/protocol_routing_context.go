package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

var ErrProtocolRouteUnavailable = errors.New("protocol route unavailable")

type protocolRoutingContextValue struct {
	router  *protocolrouter.Router
	request protocolrouter.CanonicalRequest
	plans   *protocolPlanCache
}

type protocolPlanCacheKey struct {
	accountID int64
}

type protocolPlanCache struct {
	mu       sync.Mutex
	outcomes map[protocolPlanCacheKey]protocolPlanOutcome
}

type protocolPlanOutcome struct {
	plan protocolrouter.Plan
	err  error
}

func newProtocolPlanCache() *protocolPlanCache {
	return &protocolPlanCache{outcomes: make(map[protocolPlanCacheKey]protocolPlanOutcome)}
}

// getOrPlan is the per-request, per-account planning boundary. It caches both
// success and failure so scheduler rechecks cannot call Plan twice for the
// same account in one request. Send-time freshness is route-fact equivalence,
// not a version token.
func (c *protocolPlanCache) getOrPlan(
	key protocolPlanCacheKey,
	compute func() (protocolrouter.Plan, error),
) (protocolrouter.Plan, error) {
	if c == nil {
		return compute()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if outcome, ok := c.outcomes[key]; ok {
		return outcome.plan, outcome.err
	}
	plan, err := compute()
	c.outcomes[key] = protocolPlanOutcome{plan: plan, err: err}
	return plan, err
}

func (c *protocolPlanCache) get(accountID int64) (protocolrouter.Plan, bool) {
	if c == nil {
		return protocolrouter.Plan{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	outcome, ok := c.outcomes[protocolPlanCacheKey{accountID: accountID}]
	return outcome.plan, ok && outcome.err == nil
}

type protocolRoutingContextKey struct{}

func WithProtocolRouting(
	ctx context.Context,
	router *protocolrouter.Router,
	request protocolrouter.CanonicalRequest,
) context.Context {
	return context.WithValue(ctx, protocolRoutingContextKey{}, protocolRoutingContextValue{
		router:  router,
		request: request,
		plans:   newProtocolPlanCache(),
	})
}

func ProtocolRouteLegal(ctx context.Context, account *Account, requestedModel string) bool {
	_, governed, err := protocolPlanForAccount(ctx, account, requestedModel)
	return !governed || err == nil
}

// protocolRequestEligibilityReason is the single scheduler-facing owner for
// model admission plus protocol legality. Governed text requests are decided by
// one protocolrouter.Plan call, which consumes model mapping, platform extras,
// and route policy. Out-of-scope requests keep accountAdmitsRequestedModel.
func protocolRequestEligibilityReason(ctx context.Context, account *Account, requestedModel string) (bool, string) {
	_, governed, err := protocolPlanForAccount(ctx, account, requestedModel)
	if governed {
		if err == nil {
			return true, ""
		}
		if errors.Is(err, protocolrouter.ErrModelNotAllowed) {
			return false, "model_not_supported"
		}
		return false, "protocol_route_unavailable"
	}
	if requestedModel != "" && !accountAdmitsRequestedModel(account, requestedModel, thinkingEnabledFromCtx(ctx)) {
		return false, "model_not_supported"
	}
	return true, ""
}

func ProtocolRoutingRequest(ctx context.Context) (protocolrouter.CanonicalRequest, bool) {
	if ctx == nil {
		return protocolrouter.CanonicalRequest{}, false
	}
	routing, ok := ctx.Value(protocolRoutingContextKey{}).(protocolRoutingContextValue)
	if !ok || routing.router == nil {
		return protocolrouter.CanonicalRequest{}, false
	}
	return routing.request, true
}

func protocolRoutingCanonicalRequest(ctx context.Context) (protocolrouter.CanonicalRequest, bool) {
	if ctx == nil {
		return protocolrouter.CanonicalRequest{}, false
	}
	routing, ok := ctx.Value(protocolRoutingContextKey{}).(protocolRoutingContextValue)
	if !ok {
		return protocolrouter.CanonicalRequest{}, false
	}
	return routing.request, true
}

func protocolRoutingOwnsOpenAITextCapability(ctx context.Context, capability OpenAIEndpointCapability) bool {
	if _, routed := ProtocolRoutingRequest(ctx); !routed {
		return false
	}
	switch capability {
	case OpenAIEndpointCapabilityChatCompletions, OpenAIEndpointCapabilityResponses:
		return true
	default:
		return false
	}
}

func protocolPlanForAccount(
	ctx context.Context,
	account *Account,
	requestedModel string,
) (protocolrouter.Plan, bool, error) {
	routing, ok := ctx.Value(protocolRoutingContextKey{}).(protocolRoutingContextValue)
	if !ok || routing.router == nil || !protocolRoutingGovernsAccount(account) {
		return protocolrouter.Plan{}, false, nil
	}
	snapshot, err := protocolAccountSnapshotForRequestWithThinking(account, routing.request, thinkingEnabledFromCtx(ctx))
	if err != nil {
		return protocolrouter.Plan{}, true, fmt.Errorf("%w: %v", ErrProtocolRouteUnavailable, err)
	}
	key := protocolPlanCacheKey{accountID: snapshot.AccountID()}
	plan, err := routing.plans.getOrPlan(key, func() (protocolrouter.Plan, error) {
		return routing.router.Plan(routing.request, snapshot)
	})
	if err != nil {
		return protocolrouter.Plan{}, true, fmt.Errorf("%w: %w", ErrProtocolRouteUnavailable, err)
	}
	return plan, true, nil
}

func protocolRoutingGovernsAccount(account *Account) bool {
	if account == nil || account.IsBedrock() {
		return false
	}
	if protocolRoutingAccountHasNoTextModels(account) {
		return false
	}
	if account.IsNewAPIVertexServiceAccount() {
		return true
	}
	if protocolRoutingSupportsAntigravityAccount(account) {
		return true
	}
	if account.Type == AccountTypeServiceAccount {
		return false
	}
	switch account.Platform {
	case PlatformAnthropic,
		PlatformOpenAI,
		PlatformNewAPI,
		PlatformGrok,
		PlatformKimi,
		PlatformZhipu,
		PlatformDeepseek:
		return true
	default:
		return false
	}
}

func protocolRoutingSupportsAntigravityAccount(account *Account) bool {
	if account == nil || account.Platform != PlatformAntigravity {
		return false
	}
	return account.Type == AccountTypeOAuth || tkIsAntigravityEdgeRelayStub(account)
}

func attachProtocolPlan(
	ctx context.Context,
	selection *AccountSelectionResult,
) (*AccountSelectionResult, error) {
	if selection == nil || selection.Account == nil {
		return selection, nil
	}
	routing, routed := ctx.Value(protocolRoutingContextKey{}).(protocolRoutingContextValue)
	if !routed || routing.router == nil {
		return selection, nil
	}
	if !protocolRoutingGovernsAccount(selection.Account) {
		return selection, nil
	}
	if routing.plans == nil {
		return releaseProtocolSelectionOnPlanError(selection, fmt.Errorf("%w: governed account requires scheduler-created plan", ErrProtocolRouteUnavailable))
	}
	snapshot, err := protocolAccountSnapshotForRequestWithThinking(selection.Account, routing.request, thinkingEnabledFromCtx(ctx))
	if err != nil {
		if _, planned := routing.plans.get(selection.Account.ID); planned {
			return releaseProtocolSelectionOnPlanError(selection, protocolrouter.ErrStalePlan)
		}
		return releaseProtocolSelectionOnPlanError(selection, fmt.Errorf("%w: %v", ErrProtocolRouteUnavailable, err))
	}
	plan, planned := routing.plans.get(snapshot.AccountID())
	if !planned {
		return releaseProtocolSelectionOnPlanError(selection, fmt.Errorf("%w: selected account has no scheduler-created plan", ErrProtocolRouteUnavailable))
	}
	if plan.RequestDigest() != routing.request.Digest() {
		return releaseProtocolSelectionOnPlanError(selection, protocolrouter.ErrStalePlan)
	}
	selection.ProtocolPlan = &plan
	return selection, nil
}

func releaseProtocolSelectionOnPlanError(selection *AccountSelectionResult, err error) (*AccountSelectionResult, error) {
	if selection != nil {
		if selection.Acquired && selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
			selection.ReleaseFunc = nil
			selection.Acquired = false
		}
	}
	return nil, err
}

func ProtocolPlanFromSelection(selection *AccountSelectionResult) (protocolrouter.Plan, bool) {
	if selection == nil || selection.ProtocolPlan == nil {
		return protocolrouter.Plan{}, false
	}
	return *selection.ProtocolPlan, true
}
