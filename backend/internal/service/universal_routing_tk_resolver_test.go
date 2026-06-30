//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
)

type stubSpanLister struct {
	groups []Group
	calls  int
	err    error
}

func (s *stubSpanLister) GetAvailableGroups(_ context.Context, _ int64) ([]Group, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.groups, nil
}

func grp(id int64, platform string, sortOrder int, sub bool) Group {
	st := SubscriptionTypeStandard
	if sub {
		st = SubscriptionTypeSubscription
	}
	return Group{ID: id, Platform: platform, Status: StatusActive, SortOrder: sortOrder, SubscriptionType: st, AllowImageGeneration: true}
}

func grpNoImage(id int64, platform string, sortOrder int, sub bool) Group {
	g := grp(id, platform, sortOrder, sub)
	g.AllowImageGeneration = false
	return g
}

func universalKey(userID int64) *APIKey {
	return &APIKey{ID: 1, UserID: userID, RoutingMode: RoutingModeUniversal}
}

// dispatchGrp builds an OpenAI-compat group with AllowMessagesDispatch set, used
// to exercise the /v1/messages -> openai-compat candidate gate.
func dispatchGrp(id int64, platform string, sortOrder int, dispatch bool) Group {
	g := grp(id, platform, sortOrder, false)
	g.AllowMessagesDispatch = dispatch
	return g
}

func TestUniversalShapeForRequest(t *testing.T) {
	cases := []struct {
		path, method string
		want         UniversalShape
	}{
		{"/v1/messages", http.MethodPost, ShapeAnthropicMessages},
		{"/v1/messages/count_tokens", http.MethodPost, ShapeAnthropicCountTokens},
		{"/v1/chat/completions", http.MethodPost, ShapeOpenAIChat},
		{"/chat/completions", http.MethodPost, ShapeOpenAIChat},
		{"/v1/responses", http.MethodPost, ShapeOpenAIChat},
		{"/v1/responses/*subpath", http.MethodPost, ShapeOpenAIChat},
		{"/backend-api/codex/responses", http.MethodPost, ShapeOpenAIChat},
		{"/v1/responses", http.MethodGet, ShapeSkip}, // websocket
		{"/v1/embeddings", http.MethodPost, ShapeOpenAIEmbeddings},
		{"/v1/images/generations", http.MethodPost, ShapeOpenAIImages},
		{"/v1/images/edits", http.MethodPost, ShapeOpenAIImagesEdit},
		{"/v1/video/generations", http.MethodPost, ShapeOpenAIVideo},
		{"/v1/video/generations/:task_id", http.MethodGet, ShapeOpenAIVideo},
		{"/videos", http.MethodPost, ShapeOpenAIVideo},
		{"/v1beta/models/*modelAction", http.MethodPost, ShapeGemini},
		{"/v1beta/models", http.MethodGet, ShapeSkip},
		{"/v1beta/models/:model", http.MethodGet, ShapeSkip},
		{"/v1/models", http.MethodGet, ShapeSkip},
		{"/v1/usage", http.MethodGet, ShapeSkip},
	}
	for _, c := range cases {
		if got := UniversalShapeForRequest(c.path, c.method); got != c.want {
			t.Errorf("UniversalShapeForRequest(%q,%q)=%d want %d", c.path, c.method, got, c.want)
		}
	}
}

func TestUniversalCandidatePlatforms(t *testing.T) {
	contains := func(s []string, v string) bool {
		for _, x := range s {
			if x == v {
				return true
			}
		}
		return false
	}

	// forced platform wins regardless of shape.
	if got := universalCandidatePlatforms(ShapeOpenAIChat, PlatformAntigravity, false, ""); len(got) != 1 || got[0] != PlatformAntigravity {
		t.Errorf("forced platform should win: %v", got)
	}

	// anthropic messages: native pair, no openai-compat unless a dispatch group exists.
	base := universalCandidatePlatforms(ShapeAnthropicMessages, "", false, "")
	if !contains(base, PlatformAnthropic) || !contains(base, PlatformAntigravity) || contains(base, PlatformOpenAI) {
		t.Errorf("messages base candidates wrong: %v", base)
	}
	withDispatch := universalCandidatePlatforms(ShapeAnthropicMessages, "", true, "")
	if !contains(withDispatch, PlatformOpenAI) {
		t.Errorf("messages with dispatch should include openai-compat: %v", withDispatch)
	}

	// count_tokens never includes openai-compat.
	ct := universalCandidatePlatforms(ShapeAnthropicCountTokens, "", true, "")
	if contains(ct, PlatformOpenAI) || contains(ct, PlatformNewAPI) {
		t.Errorf("count_tokens must stay anthropic/antigravity: %v", ct)
	}

	// chat = OpenAI-compat pool (includes grok).
	chat := universalCandidatePlatforms(ShapeOpenAIChat, "", false, "")
	if !contains(chat, PlatformOpenAI) || !contains(chat, PlatformNewAPI) || !contains(chat, PlatformGrok) {
		t.Errorf("chat candidates should be openai-compat pool: %v", chat)
	}
	// gemini-native image on chat completions also includes antigravity.
	chatImage := universalCandidatePlatforms(ShapeOpenAIChat, "", false, "gemini-3.1-flash-image")
	if !contains(chatImage, PlatformAntigravity) {
		t.Errorf("gemini-native image chat should include antigravity: %v", chatImage)
	}

	// gemini native pair.
	gem := universalCandidatePlatforms(ShapeGemini, "", false, "")
	if !contains(gem, PlatformGemini) || !contains(gem, PlatformAntigravity) {
		t.Errorf("gemini candidates wrong: %v", gem)
	}
}

func TestUniversalModelPlatformHint(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-8":        PlatformAnthropic,
		"grok-4":                 PlatformGrok,
		"gpt-5":                  PlatformOpenAI,
		"o3-mini":                PlatformOpenAI,
		"gemini-3-pro":           PlatformGemini,
		"gemini-3.1-flash-image": PlatformAntigravity,
		"doubao-seedream-4":      PlatformNewAPI,
		"deepseek-chat":          PlatformNewAPI,
		"some-unknown-model-xyz": "",
		"":                       "",
	}
	for model, want := range cases {
		if got := universalModelPlatformHint(model); got != want {
			t.Errorf("hint(%q)=%q want %q", model, got, want)
		}
	}
}

func TestResolve_PicksByPlatformAndHint(t *testing.T) {
	span := []Group{
		grp(10, PlatformAnthropic, 0, false),
		grp(20, PlatformOpenAI, 1, false),
		grp(30, PlatformGrok, 2, false),
		grp(40, PlatformGemini, 3, false),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	ctx := context.Background()
	key := universalKey(1)

	// anthropic shape + claude model → anthropic group.
	g, err := r.Resolve(ctx, key, ShapeAnthropicMessages, "claude-opus-4-8", "")
	if err != nil || g == nil || g.Platform != PlatformAnthropic {
		t.Fatalf("anthropic resolve got %v err %v", g, err)
	}
	// chat shape + grok model → grok group (hint beats sort_order).
	g, err = r.Resolve(ctx, key, ShapeOpenAIChat, "grok-4", "")
	if err != nil || g == nil || g.Platform != PlatformGrok {
		t.Fatalf("grok resolve got %v err %v", g, err)
	}
	// chat shape + gpt model → openai group.
	g, err = r.Resolve(ctx, key, ShapeOpenAIChat, "gpt-5", "")
	if err != nil || g == nil || g.Platform != PlatformOpenAI {
		t.Fatalf("openai resolve got %v err %v", g, err)
	}
	// gemini shape → gemini group.
	g, err = r.Resolve(ctx, key, ShapeGemini, "gemini-3-pro", "")
	if err != nil || g == nil || g.Platform != PlatformGemini {
		t.Fatalf("gemini resolve got %v err %v", g, err)
	}
}

func TestResolve_NoEntitledGroup(t *testing.T) {
	// user only has an anthropic group; an openai chat request must fail clearly.
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: []Group{grp(10, PlatformAnthropic, 0, false)}})
	if _, err := r.Resolve(context.Background(), universalKey(1), ShapeOpenAIChat, "gpt-5", ""); err == nil {
		t.Fatalf("expected ErrUniversalNoEntitledGroup, got nil")
	}
}

func TestResolve_DeterministicTiebreak(t *testing.T) {
	// two openai-compat groups, no hint match (newapi channel model) → subscription first, then sort_order.
	span := []Group{
		grp(20, PlatformOpenAI, 5, false),
		grp(21, PlatformOpenAI, 1, true), // subscription, should win
		grp(22, PlatformOpenAI, 0, false),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	g, err := r.Resolve(context.Background(), universalKey(1), ShapeOpenAIChat, "mystery-channel-model", "")
	if err != nil || g == nil || g.ID != 21 {
		t.Fatalf("expected subscription group 21, got %v err %v", g, err)
	}
}

func TestResolve_ForcedPlatformConstrains(t *testing.T) {
	span := []Group{
		grp(10, PlatformAnthropic, 0, false),
		grp(50, PlatformAntigravity, 1, false),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	// /antigravity forces antigravity even on an anthropic-shaped path.
	g, err := r.Resolve(context.Background(), universalKey(1), ShapeAnthropicMessages, "claude-opus-4-8", PlatformAntigravity)
	if err != nil || g == nil || g.Platform != PlatformAntigravity {
		t.Fatalf("forced antigravity got %v err %v", g, err)
	}
}

func TestResolve_SpanCacheHitsOnce(t *testing.T) {
	stub := &stubSpanLister{groups: []Group{grp(10, PlatformAnthropic, 0, false)}}
	r := NewUniversalRoutingResolver(stub)
	ctx := context.Background()
	key := universalKey(7)
	for i := 0; i < 5; i++ {
		if _, err := r.Resolve(ctx, key, ShapeAnthropicMessages, "claude-opus-4-8", ""); err != nil {
			t.Fatalf("resolve %d err %v", i, err)
		}
	}
	if stub.calls != 1 {
		t.Fatalf("span lister should be called once (cached), got %d", stub.calls)
	}
	// After invalidation, the next resolve recomputes.
	r.Invalidate(7)
	if _, err := r.Resolve(ctx, key, ShapeAnthropicMessages, "claude-opus-4-8", ""); err != nil {
		t.Fatalf("post-invalidate resolve err %v", err)
	}
	if stub.calls != 2 {
		t.Fatalf("expected recompute after invalidate, calls=%d", stub.calls)
	}
}

func TestResolve_MessagesDispatchGate(t *testing.T) {
	// /v1/messages with a non-Claude model: openai-compat is a candidate ONLY when
	// the span has a messages-dispatch-enabled group. With one, gpt-5 routes there.
	withDispatch := []Group{
		grp(10, PlatformAnthropic, 0, false),
		dispatchGrp(20, PlatformOpenAI, 1, true),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: withDispatch})
	g, err := r.Resolve(context.Background(), universalKey(1), ShapeAnthropicMessages, "gpt-5", "")
	if err != nil || g == nil || g.ID != 20 {
		t.Fatalf("messages+gpt-5 with dispatch group should route to openai group 20, got %v err %v", g, err)
	}

	// Without a dispatch group, the gate stays closed: openai is NOT a candidate for
	// /v1/messages, so gpt-5 can never be routed to the openai group. It falls to the
	// anthropic group (where the scheduler later rejects gpt-5 as unsupported) — the
	// key point is the gate prevents an openai pick.
	noDispatch := []Group{
		grp(10, PlatformAnthropic, 0, false),
		grp(20, PlatformOpenAI, 1, false), // dispatch=false
	}
	r2 := NewUniversalRoutingResolver(&stubSpanLister{groups: noDispatch})
	g2, err := r2.Resolve(context.Background(), universalKey(1), ShapeAnthropicMessages, "gpt-5", "")
	if err != nil || g2 == nil || g2.Platform == PlatformOpenAI {
		t.Fatalf("messages+gpt-5 without dispatch group must NOT route to openai, got %v err %v", g2, err)
	}
}

func TestResolve_NoMatchingPlatformInSpan(t *testing.T) {
	// span non-empty but fully disjoint from candidates -> clear error.
	span := []Group{grp(40, PlatformGemini, 0, false), grp(50, PlatformAntigravity, 1, false)}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	if _, err := r.Resolve(context.Background(), universalKey(1), ShapeOpenAIChat, "gpt-5", ""); err == nil {
		t.Fatalf("openai-chat with only gemini/antigravity span should yield no entitled group")
	}
}

func TestResolve_ForcedPlatformMultiGroupTiebreak(t *testing.T) {
	// forced platform narrows to multiple same-platform groups -> tiebreak still applies.
	span := []Group{
		grp(20, PlatformOpenAI, 5, false),
		grp(21, PlatformOpenAI, 1, true), // subscription -> wins under forced platform too
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	g, err := r.Resolve(context.Background(), universalKey(1), ShapeOpenAIChat, "mystery", PlatformOpenAI)
	if err != nil || g == nil || g.ID != 21 {
		t.Fatalf("forced openai multi-group should pick subscription group 21, got %v err %v", g, err)
	}
}

func TestResolve_InvalidateAll(t *testing.T) {
	stub := &stubSpanLister{groups: []Group{grp(10, PlatformAnthropic, 0, false)}}
	r := NewUniversalRoutingResolver(stub)
	ctx := context.Background()
	_, _ = r.Resolve(ctx, universalKey(1), ShapeAnthropicMessages, "claude", "")
	_, _ = r.Resolve(ctx, universalKey(2), ShapeAnthropicMessages, "claude", "")
	if stub.calls != 2 {
		t.Fatalf("expected 2 cold loads, got %d", stub.calls)
	}
	r.InvalidateAll()
	_, _ = r.Resolve(ctx, universalKey(1), ShapeAnthropicMessages, "claude", "")
	if stub.calls != 3 {
		t.Fatalf("InvalidateAll should force recompute, calls=%d", stub.calls)
	}
}

func TestResolve_SkipsInactiveGroups(t *testing.T) {
	inactive := grp(10, PlatformAnthropic, 0, false)
	inactive.Status = "disabled"
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: []Group{inactive}})
	if _, err := r.Resolve(context.Background(), universalKey(1), ShapeAnthropicMessages, "claude-opus-4-8", ""); err == nil {
		t.Fatalf("inactive-only span should yield no entitled group")
	}
}

func TestResolve_ProbeGroupsMustNotBeInUniversalSpan(t *testing.T) {
	probe := grp(99, PlatformAnthropic, 0, false)
	probe.Name = "__tk_probe_20260623"
	probe.IsExclusive = true

	regular := grp(10, PlatformAnthropic, 10, false)
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: []Group{regular}})
	g, err := r.Resolve(context.Background(), universalKey(1), ShapeAnthropicMessages, "claude-opus-4-8", "")
	if err != nil || g == nil || g.ID != regular.ID {
		t.Fatalf("universal span should contain only entitled non-probe groups, got %v err %v", g, err)
	}

	r2 := NewUniversalRoutingResolver(&stubSpanLister{groups: []Group{probe}})
	if _, err := r2.Resolve(context.Background(), universalKey(1), ShapeAnthropicMessages, "claude-opus-4-8", ""); err == nil {
		t.Fatalf("exclusive probe group must not be selected by universal routing")
	}
}

func TestResolve_ProbeMessagesDispatchDoesNotOpenOpenAICompatGate(t *testing.T) {
	probeDispatch := dispatchGrp(99, PlatformOpenAI, 0, true)
	probeDispatch.Name = "__tk_probe_openai"
	probeDispatch.IsExclusive = true

	regular := grp(10, PlatformAnthropic, 10, false)
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: []Group{regular, probeDispatch}})
	g, err := r.Resolve(context.Background(), universalKey(1), ShapeAnthropicMessages, "gpt-5", "")
	if err != nil || g == nil || g.ID != regular.ID {
		t.Fatalf("probe dispatch group must not open messages dispatch gate, got %v err %v", g, err)
	}
}
