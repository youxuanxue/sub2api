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
	revision  string
}

type protocolPlanCache struct {
	mu    sync.RWMutex
	plans map[protocolPlanCacheKey]protocolrouter.Plan
}

func newProtocolPlanCache() *protocolPlanCache {
	return &protocolPlanCache{plans: make(map[protocolPlanCacheKey]protocolrouter.Plan)}
}

func (c *protocolPlanCache) put(plan protocolrouter.Plan) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.plans[protocolPlanCacheKey{accountID: plan.AccountID(), revision: plan.AccountRevision()}] = plan
	c.mu.Unlock()
}

func (c *protocolPlanCache) get(accountID int64, revision string) (protocolrouter.Plan, bool) {
	if c == nil {
		return protocolrouter.Plan{}, false
	}
	c.mu.RLock()
	plan, ok := c.plans[protocolPlanCacheKey{accountID: accountID, revision: revision}]
	c.mu.RUnlock()
	return plan, ok
}

func (c *protocolPlanCache) containsAccount(accountID int64) bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for key := range c.plans {
		if key.accountID == accountID {
			return true
		}
	}
	return false
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

func ProtocolRoutingRequest(ctx context.Context) (protocolrouter.CanonicalRequest, bool) {
	routing, ok := ctx.Value(protocolRoutingContextKey{}).(protocolRoutingContextValue)
	if !ok || routing.router == nil {
		return protocolrouter.CanonicalRequest{}, false
	}
	return routing.request, true
}

func protocolRoutingCanonicalRequest(ctx context.Context) (protocolrouter.CanonicalRequest, bool) {
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
	snapshot, err := protocolAccountSnapshotForRequest(account, routing.request)
	if err != nil {
		return protocolrouter.Plan{}, true, fmt.Errorf("%w: %v", ErrProtocolRouteUnavailable, err)
	}
	plan, err := routing.router.Plan(routing.request, snapshot)
	if err != nil {
		return protocolrouter.Plan{}, true, fmt.Errorf("%w: %v", ErrProtocolRouteUnavailable, err)
	}
	routing.plans.put(plan)
	return plan, true, nil
}

func protocolRoutingGovernsAccount(account *Account) bool {
	if account == nil || account.IsBedrock() || account.Type == AccountTypeServiceAccount {
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
	snapshot, err := protocolAccountSnapshotForRequest(selection.Account, routing.request)
	if err != nil {
		return releaseProtocolSelectionOnPlanError(selection, fmt.Errorf("%w: %v", ErrProtocolRouteUnavailable, err))
	}
	plan, planned := routing.plans.get(snapshot.AccountID(), snapshot.Revision())
	if !planned {
		if routing.plans.containsAccount(snapshot.AccountID()) {
			return releaseProtocolSelectionOnPlanError(selection, protocolrouter.ErrStalePlan)
		}
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
