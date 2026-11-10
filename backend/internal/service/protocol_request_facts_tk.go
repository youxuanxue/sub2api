package service

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

// protocolRequestFacts memoizes derivations that depend only on one immutable
// canonical request (plus explicit scalar inputs in the key). Candidate
// selection evaluates every account against the same request, so these facts
// are computed once per request instead of once per account. The memo lives on
// the protocol routing context value; a different request digest always gets
// a fresh memo from WithProtocolRouting.
type protocolRequestFacts struct {
	mu          sync.Mutex
	imageIntent map[string]bool
	reasoning   map[string]protocolReasoningBodyOutcome
}

type protocolReasoningBodyOutcome struct {
	body string
	err  error
}

func newProtocolRequestFacts() *protocolRequestFacts {
	return &protocolRequestFacts{}
}

func protocolRoutingFacts(ctx context.Context) *protocolRequestFacts {
	if ctx == nil {
		return nil
	}
	routing, ok := ctx.Value(protocolRoutingContextKey{}).(protocolRoutingContextValue)
	if !ok || routing.router == nil {
		return nil
	}
	return routing.facts
}

// protocolRoutingExplicitImageIntent returns compute() for the routed request
// and model, evaluating it at most once per request and model.
func protocolRoutingExplicitImageIntent(ctx context.Context, model string, compute func() bool) bool {
	facts := protocolRoutingFacts(ctx)
	if facts == nil {
		return compute()
	}
	facts.mu.Lock()
	defer facts.mu.Unlock()
	if intent, ok := facts.imageIntent[model]; ok {
		return intent
	}
	intent := compute()
	if facts.imageIntent == nil {
		facts.imageIntent = make(map[string]bool)
	}
	facts.imageIntent[model] = intent
	return intent
}

// protocolRoutingReasoningBody returns compute() for the routed request under
// one group's reasoning-effort policy, evaluating it at most once per request
// and policy. Both the rewritten body and the policy error are cached.
func protocolRoutingReasoningBody(ctx context.Context, group *Group, compute func() (string, error)) (string, error) {
	facts := protocolRoutingFacts(ctx)
	if facts == nil {
		return compute()
	}
	key := reasoningEffortPolicyKey(group)
	facts.mu.Lock()
	defer facts.mu.Unlock()
	if outcome, ok := facts.reasoning[key]; ok {
		return outcome.body, outcome.err
	}
	body, err := compute()
	if facts.reasoning == nil {
		facts.reasoning = make(map[string]protocolReasoningBodyOutcome)
	}
	facts.reasoning[key] = protocolReasoningBodyOutcome{body: body, err: err}
	return body, err
}

// reasoningEffortPolicyKey encodes every group input ApplyOpenAIReasoningEffortPolicy
// reads, length-prefixed so distinct policies can never share a key.
func reasoningEffortPolicyKey(group *Group) string {
	var key strings.Builder
	write := func(value string) {
		_, _ = key.WriteString(strconv.Itoa(len(value)))
		_ = key.WriteByte(':')
		_, _ = key.WriteString(value)
	}
	write(group.MaxReasoningEffort)
	write(group.MaxReasoningEffortOverLimit)
	_, _ = key.WriteString(strconv.Itoa(len(group.ReasoningEffortMappings)))
	_ = key.WriteByte('#')
	for _, mapping := range group.ReasoningEffortMappings {
		write(mapping.From)
		write(mapping.To)
		write(mapping.MatchType)
		write(mapping.Model)
	}
	return key.String()
}

// protocolRoutingRequestFor returns the routed request only when it was
// attached by this router, the same condition WithProtocolRouting uses to keep
// an existing routing value.
func protocolRoutingRequestFor(ctx context.Context, router *protocolrouter.Router) (protocolrouter.CanonicalRequest, bool) {
	if ctx == nil {
		return protocolrouter.CanonicalRequest{}, false
	}
	routing, ok := ctx.Value(protocolRoutingContextKey{}).(protocolRoutingContextValue)
	if !ok || routing.router == nil || routing.router != router {
		return protocolrouter.CanonicalRequest{}, false
	}
	return routing.request, true
}
