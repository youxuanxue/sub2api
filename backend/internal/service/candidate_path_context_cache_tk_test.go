//go:build unit

package service

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCandidatePathContextPreparerCachesPerGroup(t *testing.T) {
	groups := []Group{grp(10, PlatformOpenAI, 1, false), grp(20, PlatformOpenAI, 2, false)}
	groups[0].AllowMessagesDispatch = true
	groups[1].AllowMessagesDispatch = true
	groups[0].MessagesDispatchModelConfig.ExactModelMappings = map[string]string{"alias": "gpt-5.4"}
	groups[1].MessagesDispatchModelConfig.ExactModelMappings = map[string]string{"alias": "gpt-5.4-mini"}
	r, _, key := globalCandidateFixture(groups, nil)
	key.RoutingMode, key.Group, key.GroupID = RoutingModeDirect, &groups[0], &groups[0].ID
	request := &CandidateRequest{
		resolver: r,
		key:      key,
		shape:    ShapeOpenAIChat,
		path:     "/v1/chat/completions",
		model:    "alias",
		body:     []byte(`{"model":"alias","messages":[{"role":"user","content":"hi"}]}`),
	}

	var pathContextCalls atomic.Int64
	prepared := make(map[int64]candidatePreparedPath)
	prepare := func(ctx context.Context, group *Group) (context.Context, string, ChannelMappingResult, error) {
		if result, ok := prepared[group.ID]; ok {
			return result.ctx, result.model, result.channel, result.err
		}
		pathContextCalls.Add(1)
		result := candidatePreparedPath{}
		result.ctx, result.model, result.channel, result.err = request.pathContext(ctx, group)
		prepared[group.ID] = result
		return result.ctx, result.model, result.channel, result.err
	}

	ctxA, modelA, _, err := prepare(context.Background(), &groups[0])
	require.NoError(t, err)
	ctxAgain, _, _, err := prepare(context.Background(), &groups[0])
	require.NoError(t, err)
	require.Same(t, ctxA, ctxAgain)
	_, modelB, _, err := prepare(context.Background(), &groups[1])
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4", modelA)
	require.Equal(t, "gpt-5.4-mini", modelB)
	require.Equal(t, int64(2), pathContextCalls.Load(), "pathContext must run once per group")
}

func TestCandidatesReusesPreparedPathContextAcrossAccounts(t *testing.T) {
	groups := []Group{grp(10, PlatformOpenAI, 1, false)}
	accounts := make([]Account, 12)
	for i := range accounts {
		accounts[i] = globalCandidateAccount(int64(i+1), 1, 10)
	}
	r, _, key := globalCandidateFixture(groups, accounts)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	request := &CandidateRequest{
		resolver: r,
		key:      key,
		groups:   groups,
		shape:    ShapeOpenAIChat,
		path:     "/v1/chat/completions",
		model:    "gpt-5.4",
		body:     body,
	}

	var pathContextCalls atomic.Int64
	prepared := make(map[int64]candidatePreparedPath)
	countingPrepare := func(ctx context.Context, group *Group) (context.Context, string, ChannelMappingResult, error) {
		if hit, ok := prepared[group.ID]; ok {
			return hit.ctx, hit.model, hit.channel, hit.err
		}
		pathContextCalls.Add(1)
		out := candidatePreparedPath{}
		out.ctx, out.model, out.channel, out.err = request.pathContext(ctx, group)
		prepared[group.ID] = out
		return out.ctx, out.model, out.channel, out.err
	}
	for i := range accounts {
		path, err := request.evaluatePathWithPreparation(context.Background(), &accounts[i], &groups[0], countingPrepare)
		require.NoError(t, err)
		require.NotNil(t, path)
	}
	require.Equal(t, int64(1), pathContextCalls.Load(), "selection must not re-parse body per account")

	// A fresh preparer (revalidate / next selectAccount) must parse again.
	pathContextCalls.Store(0)
	_, err := request.evaluatePathWithPreparation(context.Background(), &accounts[0], &groups[0], func(ctx context.Context, group *Group) (context.Context, string, ChannelMappingResult, error) {
		pathContextCalls.Add(1)
		return request.pathContext(ctx, group)
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), pathContextCalls.Load(), "refresh/revalidate path must see fresh pathContext")
}

func TestEvaluatePathBodyModelCandidatesCachedWhenAllowlistEnabled(t *testing.T) {
	group := grp(10, PlatformOpenAI, 1, false)
	group.ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.4", "gpt-5.4-mini"}}
	account := globalCandidateAccount(1, 1, 10)
	r, _, key := globalCandidateFixture([]Group{group}, []Account{account})
	body := []byte(`{"model":"gpt-5.4","MODEL":"gpt-5.4-mini","messages":[{"role":"user","content":"` + strings.Repeat("x", 4096) + `"}]}`)
	request := &CandidateRequest{
		resolver:    r,
		key:         key,
		groups:      []Group{group},
		shape:       ShapeOpenAIChat,
		path:        "/v1/chat/completions",
		model:       "gpt-5.4",
		body:        body,
		contentType: "application/json",
	}
	require.False(t, request.bodyModelsResolved)
	_, err := request.evaluatePath(context.Background(), &account, &group)
	require.NoError(t, err)
	require.True(t, request.bodyModelsResolved)
	first := append([]string(nil), request.bodyModelCandidates...)
	require.NotEmpty(t, first)
	_, err = request.evaluatePath(context.Background(), &account, &group)
	require.NoError(t, err)
	require.Equal(t, first, request.bodyModelCandidates)
}

func TestWithRequestRejectsMistypedStreamWithoutFullUnmarshal(t *testing.T) {
	r := NewUniversalRoutingResolver(&stubSpanLister{})
	r.router = NewProtocolRouter()
	body := []byte(`{"model":"gpt-5.4","stream":"yes","messages":[{"role":"user","content":"hi"}]}`)
	ctx := r.WithRequest(context.Background(), ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body)
	_, ok := ProtocolRoutingRequest(ctx)
	require.False(t, ok, "mistyped stream must fail closed like json.Unmarshal into bool")

	okBody := []byte(`{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	ctx = r.WithRequest(context.Background(), ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", okBody)
	req, ok := ProtocolRoutingRequest(ctx)
	require.True(t, ok)
	require.True(t, req.Profile().Stream)
}

func BenchmarkCandidatePathContextPrepareCachedVsUncached(b *testing.B) {
	groups := []Group{grp(10, PlatformOpenAI, 1, false)}
	accounts := make([]Account, 32)
	for i := range accounts {
		accounts[i] = globalCandidateAccount(int64(i+1), 1, 10)
	}
	r, _, key := globalCandidateFixture(groups, accounts)
	// ~8KiB body approximates live Responses traffic that made Unmarshal dominate.
	payload := `{"model":"gpt-5.4","stream":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("word ", 400) + `"}]}]}`
	body := []byte(payload)
	request := &CandidateRequest{
		resolver: r,
		key:      key,
		groups:   groups,
		shape:    ShapeOpenAIChat,
		path:     "/v1/responses",
		model:    "gpt-5.4",
		body:     body,
	}
	b.Run("uncached", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for j := range accounts {
				if _, err := request.evaluatePathWithPreparation(context.Background(), &accounts[j], &groups[0], request.pathContext); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("cached", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			prepare := candidatePathContextPreparer(request)
			for j := range accounts {
				if _, err := request.evaluatePathWithPreparation(context.Background(), &accounts[j], &groups[0], prepare); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
}
