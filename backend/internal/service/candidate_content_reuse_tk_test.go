//go:build unit

package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestCandidateMappedCursorContentReuseKeepsFreshFacts(t *testing.T) {
	const model = "claude-sonnet-4-6"
	group := grp(1, PlatformNewAPI, 1, false)
	group.AllowMessagesDispatch = true
	group.MessagesDispatchModelConfig.ExactModelMappings = map[string]string{"alias": model}
	account := cursorCandidateAccount(model)
	resolver, _, key := globalCandidateFixture([]Group{group}, []Account{*account})
	key.RoutingMode, key.Group, key.GroupID = RoutingModeDirect, &group, &group.ID
	body := []byte(`{"model":"alias","input":"hi"}`)
	ctx := resolver.WithRequest(context.Background(), ShapeOpenAIChat, "/v1/responses", "alias", body)
	request := &CandidateRequest{resolver: resolver, key: key, shape: ShapeOpenAIChat,
		path: "/v1/responses", model: "alias", body: body}
	first, resolved, _, err := request.pathContext(ctx, &group)
	require.NoError(t, err)
	_, _, err = protocolPlanForAccount(first, account, resolved)
	require.NoError(t, err)
	firstRouting := first.Value(protocolRoutingContextKey{}).(protocolRoutingContextValue)
	second, resolved, _, err := request.pathContext(ctx, &group)
	require.NoError(t, err)
	_, _, err = protocolPlanForAccount(second, account, resolved)
	require.NoError(t, err)
	secondRouting := second.Value(protocolRoutingContextKey{}).(protocolRoutingContextValue)
	require.Same(t, firstRouting.content, secondRouting.content, "mapped request content should survive reselection")
	require.NotSame(t, firstRouting.plans, secondRouting.plans, "fresh path preparation must not inherit plans")
	otherGroup := group
	otherGroup.ID = 2
	other, resolved, _, err := request.pathContext(ctx, &otherGroup)
	require.NoError(t, err)
	_, _, err = protocolPlanForAccount(other, account, resolved)
	require.NoError(t, err)
	require.Same(t, firstRouting.content, other.Value(protocolRoutingContextKey{}).(protocolRoutingContextValue).content)
	_, _, err = protocolPlanForAccount(withProtocolNativeOnly(other, true), account, resolved)
	require.ErrorIs(t, err, protocolrouter.ErrNoLegalRoute, "content eligibility cannot grant conversion permission")

	// Even an already warmed Plan must reject fresh account capability loss.
	account.Credentials["base_url"] = "https://different.example"
	_, _, err = protocolPlanForAccount(second, account, resolved)
	require.ErrorIs(t, err, ErrProtocolCapabilityUnknown)

	// Group policy changes must resolve a new model and its content verdict.
	request.shape, request.path = ShapeAnthropicMessages, "/v1/messages"
	request.body = []byte(genuineWebSearchBody)
	group.MessagesDispatchModelConfig.ExactModelMappings["alias"] = "deepseek-v3.2"
	deepseek, resolved, _, err := request.pathContext(context.Background(), &group)
	require.NoError(t, err)
	canonical, ok := ProtocolRoutingRequest(deepseek)
	require.True(t, ok)
	require.Equal(t, "deepseek-v3.2", resolved)
	require.True(t, request.cursorContent.supported(canonical, resolved))
	group.MessagesDispatchModelConfig.ExactModelMappings["alias"] = model
	claude, resolved, _, err := request.pathContext(context.Background(), &group)
	require.NoError(t, err)
	canonical, ok = ProtocolRoutingRequest(claude)
	require.True(t, ok)
	require.Equal(t, model, resolved)
	require.False(t, request.cursorContent.supported(canonical, resolved), "model-dependent normalization must stay isolated")
}

func TestCandidateCursorContentResetsEachTurn(t *testing.T) {
	const model = "gpt-5.4"
	group := grp(1, PlatformNewAPI, 1, false)
	group.AllowMessagesDispatch = true
	account := cursorCandidateAccount(model)
	resolver, _, key := globalCandidateFixture([]Group{group}, []Account{*account})
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`)
	ctx, request, err := resolver.PrepareCandidateRequest(context.Background(), key,
		ShapeAnthropicMessages, "/v1/messages", model, body, "", "")
	require.NoError(t, err)
	oldCache := request.cursorContent
	image := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}]}`)
	require.Error(t, request.RevalidateTurn(ctx, account.ID, model, image), "a new turn cannot inherit text eligibility")
	require.NotSame(t, oldCache, request.cursorContent, "long WebSocket sessions must release previous turn outcomes")
	require.Nil(t, request.current)
	require.NoError(t, request.RevalidateTurn(ctx, account.ID, model, body))
	require.Equal(t, account.ID, request.current.account.ID)
}

func TestCandidateDiscoveryCursorContentResetsPerShape(t *testing.T) {
	const model = "claude-sonnet-4-6"
	group := grp(1, PlatformNewAPI, 1, false)
	account := cursorCandidateAccount(model)
	resolver, _, key := globalCandidateFixture([]Group{group}, []Account{*account})
	request := &CandidateRequest{resolver: resolver, key: key, shape: ShapeOpenAIChat,
		path: "/v1/responses", model: model, body: []byte(`{"input":"hi"}`)}
	prepare := candidateDiscoveryPathPreparer(request)
	ctx, resolved, _, err := prepare(context.Background(), &group)
	require.NoError(t, err)
	_, _, err = protocolPlanForAccount(ctx, account, resolved)
	require.NoError(t, err)
	oldCache := request.cursorContent
	request.path = "/v1/chat/completions"
	request.body = []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`)
	prepare = candidateDiscoveryPathPreparer(request)
	ctx, resolved, _, err = prepare(context.Background(), &group)
	require.NoError(t, err)
	_, _, err = protocolPlanForAccount(ctx, account, resolved)
	require.ErrorIs(t, err, protocolrouter.ErrNoLegalRoute)
	require.NotSame(t, oldCache, request.cursorContent)
}

func benchmarkCandidateMappedCursorContent(b *testing.B, size, passes int) {
	const model = "claude-sonnet-4-6"
	group := grp(1, PlatformNewAPI, 1, false)
	group.AllowMessagesDispatch = true
	group.MessagesDispatchModelConfig.ExactModelMappings = map[string]string{"alias": model}
	account := cursorCandidateAccount(model)
	resolver, _, key := globalCandidateFixture([]Group{group}, []Account{*account})
	key.RoutingMode, key.Group, key.GroupID = RoutingModeDirect, &group, &group.ID
	body := []byte(fmt.Sprintf(`{"model":"alias","input":%q}`, strings.Repeat("x", size)))
	profile, ok := candidateRequestProfile(ShapeOpenAIChat, "/v1/responses", "alias", body)
	if !ok {
		b.Fatal("invalid request profile")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := resolver.WithRequestProfile(context.Background(), ShapeOpenAIChat, "/v1/responses", "alias", body, profile)
		request := &CandidateRequest{resolver: resolver, key: key, shape: ShapeOpenAIChat,
			path: "/v1/responses", model: "alias", body: body, requestProfile: profile, requestProfileValid: true}
		// Admission, reservation and failover can each prepare the mapped path
		// from the original context. Every pass still needs fresh account facts.
		for pass := 0; pass < passes; pass++ {
			mapped, resolved, _, err := request.pathContext(ctx, &group)
			if err != nil {
				b.Fatal(err)
			}
			if _, _, err := protocolPlanForAccount(mapped, account, resolved); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkCandidateMappedCursorContent(b *testing.B) {
	for _, size := range []int{4 << 10, 128 << 10} {
		for _, passes := range []int{1, 3} {
			b.Run(fmt.Sprintf("%dKiB/%dpasses", size>>10, passes), func(b *testing.B) {
				benchmarkCandidateMappedCursorContent(b, size, passes)
			})
		}
	}
}
