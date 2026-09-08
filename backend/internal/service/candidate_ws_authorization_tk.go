package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

// RefreshWebSocketAuthorization bounds each turn to current entitlements. A
// persistent socket cannot keep using the opening request's group snapshot.
func (r *CandidateRequest) RefreshWebSocketAuthorization(ctx context.Context) error {
	if r == nil || r.resolver == nil || r.key == nil || r.key.User == nil {
		return ErrUniversalNoEntitledGroup
	}
	var groups []Group
	if r.key.IsUniversal() {
		var err error
		groups, err = r.resolver.span(ctx, r.key.UserID)
		if err != nil {
			return err
		}
	} else if r.key.Group != nil {
		group := r.key.Group
		if group.IsSubscriptionType() || r.key.User.CanBindGroup(group.ID, group.IsExclusive) {
			groups = []Group{*group}
		}
	}
	eligible := make([]Group, 0, len(groups))
	for _, group := range groups {
		if group.IsActive() && (!r.key.IsUniversal() || !isUniversalProbeGroup(group)) {
			eligible = append(eligible, group)
		}
	}
	if len(eligible) == 0 {
		return ErrUniversalNoEntitledGroup
	}
	r.groups = eligible
	return nil
}

// ValidateWebSocketExecution checks the connection's immutable account against
// this turn's fresh Plan. Route changes require a new upstream connection.
func (r *CandidateRequest) ValidateWebSocketExecution(account *Account) (*Account, error) {
	if r == nil || r.current == nil || account == nil || account.ID != r.current.account.ID {
		return nil, ErrUniversalCapacityUnavailable
	}
	fresh := r.current.account
	if account.Platform != fresh.Platform || account.Type != fresh.Type || account.GetCredential("api_key") != fresh.GetCredential("api_key") {
		return nil, protocolrouter.ErrStalePlan
	}
	if r.current.plan != nil {
		request, ok := ProtocolRoutingRequest(r.current.ctx)
		if !ok {
			return nil, ErrProtocolRouteUnavailable
		}
		snapshot, err := protocolAccountSnapshotForRequestWithThinking(account, request, thinkingEnabledFromCtx(r.current.ctx))
		if err != nil {
			return nil, err
		}
		connectedPlan, err := r.resolver.router.Plan(request, snapshot)
		if err != nil {
			return nil, err
		}
		if !protocolPlansRoutingEquivalent(connectedPlan, *r.current.plan) {
			return nil, protocolrouter.ErrStalePlan
		}
	}
	return fresh, nil
}
