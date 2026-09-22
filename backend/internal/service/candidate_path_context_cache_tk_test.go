//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
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

	prepare := candidatePathContextPreparer(request)
	ctxA, modelA, _, err := prepare(context.Background(), &groups[0])
	require.NoError(t, err)
	ctxAgain, _, _, err := prepare(context.Background(), &groups[0])
	require.NoError(t, err)
	require.Same(t, ctxA, ctxAgain, "production preparer must reuse the cached pathContext")
	_, modelB, _, err := prepare(context.Background(), &groups[1])
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4", modelA)
	require.Equal(t, "gpt-5.4-mini", modelB)
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

	prepare := candidatePathContextPreparer(request)
	var firstCtx context.Context
	for i := range accounts {
		path, err := request.evaluatePathWithPreparation(context.Background(), &accounts[i], &groups[0], prepare)
		require.NoError(t, err)
		require.NotNil(t, path)
		if i == 0 {
			firstCtx = path.ctx
		} else {
			require.Same(t, firstCtx, path.ctx, "all accounts in one selection share one prepared pathContext")
		}
	}

	// A fresh preparer (revalidate / next selectAccount) must allocate a new ctx.
	freshPrepare := candidatePathContextPreparer(request)
	path, err := request.evaluatePathWithPreparation(context.Background(), &accounts[0], &groups[0], freshPrepare)
	require.NoError(t, err)
	require.NotNil(t, path)
	require.NotSame(t, firstCtx, path.ctx, "refresh/revalidate path must see a fresh pathContext")
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

	nullBody := []byte(`{"model":"gpt-5.4","stream":null,"type":null,"messages":[{"role":"user","content":"hi"}]}`)
	ctx = r.WithRequest(context.Background(), ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", nullBody)
	req, ok := ProtocolRoutingRequest(ctx)
	require.True(t, ok, "JSON null stream/type must match encoding/json zero-value Unmarshal")
	require.False(t, req.Profile().Stream)

	okBody := []byte(`{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	ctx = r.WithRequest(context.Background(), ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", okBody)
	req, ok = ProtocolRoutingRequest(ctx)
	require.True(t, ok)
	require.True(t, req.Profile().Stream)
}

func TestWithRequestProfileReusesProfileAcrossModelRewrite(t *testing.T) {
	r := NewUniversalRoutingResolver(&stubSpanLister{})
	r.router = NewProtocolRouter()
	body := []byte(`{"model":"alias","stream":false,"tools":[{"type":"function","function":{"name":"lookup"}}],"reasoning_effort":"low","prompt_cache_key":"cache","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"x"}}]}]}`)
	profile, ok := candidateRequestProfile(ShapeOpenAIChat, "/v1/chat/completions", "alias", body)
	require.True(t, ok)
	groupBody, err := sjson.SetBytes(body, "model", "gpt-5.4")
	require.NoError(t, err)
	ctxOriginal := r.WithRequestProfile(context.Background(), ShapeOpenAIChat, "/v1/chat/completions", "alias", body, profile)
	ctxGroup := r.WithRequestProfile(context.Background(), ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", groupBody, profile)
	original, ok := ProtocolRoutingRequest(ctxOriginal)
	require.True(t, ok)
	group, ok := ProtocolRoutingRequest(ctxGroup)
	require.True(t, ok)
	require.Equal(t, original.Profile(), group.Profile())
	require.Equal(t, "alias", original.RequestedModel())
	require.Equal(t, "gpt-5.4", group.RequestedModel())
	require.NotEqual(t, original.Digest(), group.Digest(), "digest must include the group-specific body/model")
	require.Equal(t, groupBody, group.Body())
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
