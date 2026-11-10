//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// withoutProtocolRequestFacts returns ctx with the request-facts memo removed,
// so every derivation runs fresh: the pre-memo behavior.
func withoutProtocolRequestFacts(ctx context.Context) context.Context {
	routing, ok := ctx.Value(protocolRoutingContextKey{}).(protocolRoutingContextValue)
	if !ok {
		return ctx
	}
	routing.facts = nil
	return context.WithValue(ctx, protocolRoutingContextKey{}, routing)
}

func TestReasoningEffortPolicyKeySeparatesEveryInput(t *testing.T) {
	base := Group{MaxReasoningEffort: "high", MaxReasoningEffortOverLimit: "clamp",
		ReasoningEffortMappings: []ReasoningEffortMapping{{From: "a", To: "b", MatchType: "exact", Model: "m"}}}
	variants := []func(*Group){
		func(g *Group) { g.MaxReasoningEffort = "low" },
		func(g *Group) { g.MaxReasoningEffortOverLimit = "deny" },
		func(g *Group) { g.ReasoningEffortMappings = nil },
		func(g *Group) {
			g.ReasoningEffortMappings = []ReasoningEffortMapping{{From: "ab", To: "", MatchType: "exact", Model: "m"}}
		},
		func(g *Group) {
			g.ReasoningEffortMappings = []ReasoningEffortMapping{{From: "a", To: "b", MatchType: "prefix", Model: "m"}}
		},
		func(g *Group) {
			g.ReasoningEffortMappings = []ReasoningEffortMapping{{From: "a", To: "b", MatchType: "exact", Model: "n"}}
		},
		func(g *Group) { g.MaxReasoningEffort, g.MaxReasoningEffortOverLimit = "high5", "clamp"[1:] },
	}
	baseKey := reasoningEffortPolicyKey(&base)
	seen := map[string]bool{baseKey: true}
	for i, mutate := range variants {
		g := base
		g.ReasoningEffortMappings = append([]ReasoningEffortMapping(nil), base.ReasoningEffortMappings...)
		mutate(&g)
		key := reasoningEffortPolicyKey(&g)
		require.False(t, seen[key], "variant %d collides", i)
		seen[key] = true
	}
	same := base
	require.Equal(t, baseKey, reasoningEffortPolicyKey(&same))
}

// The billing policy memo must return the same rewrite and the same error as
// a fresh computation for each group policy.
func TestCandidateBillingReasoningBodyMemoMatchesFresh(t *testing.T) {
	r, _, _ := globalCandidateFixture(nil, nil)
	body := []byte(`{"model":"gpt-5.4","reasoning_effort":"xhigh","messages":[{"role":"user","content":"hi"}]}`)
	ctx := r.WithRequest(context.Background(), ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body)
	account := &Account{ID: 1, Platform: PlatformOpenAI}
	groups := []Group{
		{ID: 1, MaxReasoningEffort: "high"},
		{ID: 2, MaxReasoningEffort: "low"},
		{ID: 3, MaxReasoningEffort: "high", MaxReasoningEffortOverLimit: ReasoningEffortOverLimitDeny},
		{ID: 4, MaxReasoningEffort: "high"},
	}
	for round := 0; round < 2; round++ {
		for i := range groups {
			got, gotErr := candidateBillingPolicyForOrigin(ctx, account, account, &groups[i], "gpt-5.4", ShapeOpenAIChat, nil)
			want, wantErr := candidateBillingPolicyForOrigin(withoutProtocolRequestFacts(ctx), account, account, &groups[i], "gpt-5.4", ShapeOpenAIChat, nil)
			require.Equal(t, wantErr, gotErr, "group %d", groups[i].ID)
			require.Equal(t, want.reasoningBody, got.reasoningBody, "group %d", groups[i].ID)
		}
	}
	require.Len(t, protocolRoutingFacts(ctx).reasoning, 3, "groups 1 and 4 share one policy")
}

func TestCandidateImageIntentMemoIsPerModel(t *testing.T) {
	r, _, _ := globalCandidateFixture(nil, nil)
	body := []byte(`{"model":"gpt-5.4","input":"draw","tools":[{"type":"image_generation"}]}`)
	ctx := r.WithRequest(context.Background(), ShapeOpenAIChat, "/v1/responses", "gpt-5.4", body)
	group := grpNoImage(10, PlatformOpenAI, 1, false)
	account := globalCandidateAccount(1, 1, 10)
	for round := 0; round < 2; round++ {
		require.False(t, candidatePathAllowsEndpoint(ctx, &account, &group, ShapeOpenAIChat, "gpt-5.4", nil))
	}
	plain := r.WithRequest(context.Background(), ShapeOpenAIChat, "/v1/responses", "gpt-5.4", []byte(`{"model":"gpt-5.4","input":"hi"}`))
	require.True(t, candidatePathAllowsEndpoint(plain, &account, &group, ShapeOpenAIChat, "gpt-5.4", nil))
	// An image model name is intent on its own; the memo keys by model.
	require.False(t, candidatePathAllowsEndpoint(plain, &account, &group, ShapeOpenAIChat, "gpt-image-1", nil))
	require.True(t, candidatePathAllowsEndpoint(plain, &account, &group, ShapeOpenAIChat, "gpt-5.4", nil))
	require.Len(t, protocolRoutingFacts(plain).imageIntent, 2)
}

// A group that leaves the body unchanged keeps the ingress request and its
// memo; a group that rewrites the model builds a new request.
func TestWithRequestProfileReusesMatchingRoutedRequest(t *testing.T) {
	r, _, _ := globalCandidateFixture(nil, nil)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`)
	profile, ok := candidateRequestProfile(ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body)
	require.True(t, ok)
	ctx := r.WithRequestProfile(context.Background(), ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body, profile)
	first, ok := ProtocolRoutingRequest(ctx)
	require.True(t, ok)
	facts := protocolRoutingFacts(ctx)

	same := r.WithRequestProfile(ctx, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", append([]byte(nil), body...), profile)
	reused, _ := ProtocolRoutingRequest(same)
	require.Equal(t, first.Digest(), reused.Digest())
	require.Same(t, facts, protocolRoutingFacts(same))

	rewritten := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	other := r.WithRequestProfile(ctx, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.5", rewritten, profile)
	next, _ := ProtocolRoutingRequest(other)
	fresh := r.WithRequestProfile(context.Background(), ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.5", rewritten, profile)
	want, _ := ProtocolRoutingRequest(fresh)
	require.Equal(t, want.Digest(), next.Digest())
	require.NotEqual(t, first.Digest(), next.Digest())
	require.NotSame(t, facts, protocolRoutingFacts(other))
}

// One full admission pass yields the same candidates, groups, models and Plans
// whether or not request facts are memoized.
func TestCandidateAdmissionPassUnchangedByRequestFacts(t *testing.T) {
	ctx, memoized := candidateFactsRequest(t, 6, 3, 4096)
	_, fresh := candidateFactsRequest(t, 6, 3, 4096)
	got, gotSupported, gotErr := memoized.candidates(ctx, candidateSelectOptions{})
	want, wantSupported, wantErr := fresh.candidates(withoutProtocolRequestFacts(ctx), candidateSelectOptions{})
	require.Equal(t, wantErr, gotErr)
	require.Equal(t, wantSupported, gotSupported)
	require.Len(t, got, len(want))
	require.NotEmpty(t, got)
	for i := range want {
		require.Equal(t, want[i].account.ID, got[i].account.ID)
		require.Equal(t, want[i].group.ID, got[i].group.ID)
		require.Equal(t, want[i].model, got[i].model)
		require.Equal(t, want[i].plan == nil, got[i].plan == nil)
		if want[i].plan != nil {
			require.Equal(t, want[i].plan.RequestDigest(), got[i].plan.RequestDigest())
			require.Equal(t, want[i].plan.EffectiveRequestDigest(), got[i].plan.EffectiveRequestDigest())
			require.Equal(t, want[i].plan.Adjustment(), got[i].plan.Adjustment())
			require.Equal(t, want[i].plan.TargetProtocol(), got[i].plan.TargetProtocol())
			require.Equal(t, want[i].plan.Endpoint(), got[i].plan.Endpoint())
		}
	}
}

// pathContext passes the request's private body straight to the "model"
// rewrite. A same-length rewrite must still leave that body (which WebSocket
// ingress forwards) and the routed ingress request untouched.
func TestPathContextModelRewriteLeavesRequestBodyIntact(t *testing.T) {
	group := grp(10, PlatformOpenAI, 1, false)
	r, _, key := globalCandidateFixture([]Group{group}, nil)
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	request := &CandidateRequest{resolver: r, key: key, groups: []Group{group}, shape: ShapeOpenAIChat,
		path: "/v1/chat/completions", model: "gpt-5.4", body: append([]byte(nil), body...)}
	request.requestProfile, request.requestProfileValid = candidateRequestProfile(request.shape, request.path, request.model, body)
	ctx := r.WithRequest(context.Background(), request.shape, request.path, "gpt-5.5", body)
	ingress, ok := ProtocolRoutingRequest(ctx)
	require.True(t, ok)

	pathCtx, model, _, err := request.pathContext(ctx, &group)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4", model)
	routed, ok := ProtocolRoutingRequest(pathCtx)
	require.True(t, ok)
	require.JSONEq(t, `{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`, string(routed.Body()))
	require.Equal(t, body, request.body, "the private request body must not be rewritten in place")
	require.True(t, ingress.BodyEquals(body), "the ingress request must not be rewritten in place")
}
