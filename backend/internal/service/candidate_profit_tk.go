package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

type candidateProfitPricingKey struct{}

type candidateProfitPricing struct {
	at    time.Time
	gates map[int64]*openAIProfitControlGate
}

// Candidate paths own their billing origin. Never inherit an ingress or
// previous candidate's profit gate, even when that origin now disables it.
func (r *CandidateRequest) withProfitControl(path *candidateExecutionPath) context.Context {
	ctx := context.WithValue(path.ctx, openAIProfitControlGateCtxKey{}, (*openAIProfitControlGate)(nil))
	if !r.profitControlledRequest(ctx) {
		return ctx
	}
	ctx = context.WithValue(ctx, ctxkey.Group, path.group)
	ctx = context.WithValue(ctx, gatewayTokenRequestBillingGroupCtxKey{}, path.group)
	pricing, _ := ctx.Value(candidateProfitPricingKey{}).(*candidateProfitPricing)
	if pricing != nil {
		if gate, found := pricing.gates[path.group.ID]; found {
			return context.WithValue(ctx, openAIProfitControlGateCtxKey{}, gate)
		}
	}
	pricingAt, _ := gatewayTokenRequestPricingAtFromContext(ctx)
	ctx = r.resolver.candidateGateway.withGatewayProfitControlGroups(ctx, path.group, path.group, pricingAt)
	if pricing != nil {
		pricing.gates[path.group.ID], _ = ctx.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
	}
	return ctx
}

func (r *CandidateRequest) profitControlledRequest(ctx context.Context) bool {
	if _, suppressed := ctx.Value(openAIProfitControlSuppressCtxKey{}).(struct{}); suppressed {
		return false
	}
	switch r.shape {
	case ShapeAnthropicMessages, ShapeOpenAIChat, ShapeOpenAIEmbeddings:
		return true
	case ShapeGemini:
		return !strings.HasSuffix(r.path, ":countTokens")
	default:
		return false
	}
}

func (r *CandidateRequest) withProfitPricing(ctx context.Context) context.Context {
	if !r.profitControlledRequest(ctx) {
		return ctx
	}
	pricingAt, ok := openAIPricingAtFromContext(ctx)
	if !ok {
		pricingAt, ok = gatewayTokenRequestPricingAtFromContext(ctx)
	}
	if !ok {
		pricingAt = timezone.Now()
	}
	// RevalidateTurn clears current before reevaluating the next WS request.
	// Its admission cannot reuse a previous turn's peak factor or group gate.
	if r.websocket && r.current == nil {
		pricingAt = timezone.Now()
	}
	ctx = context.WithValue(ctx, openAIPricingAtCtxKey{}, pricingAt)
	ctx = context.WithValue(ctx, gatewayTokenRequestPricingAtCtxKey{}, pricingAt)
	if pricing, _ := ctx.Value(candidateProfitPricingKey{}).(*candidateProfitPricing); pricing == nil || !pricing.at.Equal(pricingAt) {
		ctx = context.WithValue(ctx, candidateProfitPricingKey{}, &candidateProfitPricing{at: pricingAt, gates: make(map[int64]*openAIProfitControlGate)})
	}
	return ctx
}
