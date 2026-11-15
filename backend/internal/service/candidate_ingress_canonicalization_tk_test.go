//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPrepareCandidateIngressReusesCanonicalRequestProfileAndDigest(t *testing.T) {
	group := grp(10, PlatformOpenAI, 1, false)
	r, _, key := globalCandidateFixture([]Group{group}, []Account{globalCandidateAccount(1, 1, group.ID)})
	body := []byte(`{"model":"gpt-5.4","stream":true,"tools":[{"type":"function","function":{"name":"lookup"}}],"messages":[{"role":"user","content":"hi"}]}`)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	state, err := r.PrepareCandidateIngress(c, key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body, "")
	require.NoError(t, err)
	require.NotNil(t, state)

	request, ok := ProtocolRoutingRequest(c.Request.Context())
	require.True(t, ok)
	expected, err := protocolrouter.ParseCanonicalRequest(
		protocolrouter.ProtocolChatCompletions,
		protocolrouter.ResponsesPathNone,
		"gpt-5.4",
		true,
		body,
	)
	require.NoError(t, err)
	require.Equal(t, expected.Profile(), request.Profile())
	require.Equal(t, expected.Digest(), request.Digest())
	require.Equal(t, expected.Digest(), state.current.ctx.Value(protocolRoutingContextKey{}).(protocolRoutingContextValue).request.Digest())
	require.True(t, state.requestProfileValid)
	require.Equal(t, expected.Profile(), state.requestProfile)
}

func TestPrepareCandidateRequestAttachesCanonicalRequestForLaterReuse(t *testing.T) {
	group := grp(10, PlatformOpenAI, 1, false)
	r, _, key := globalCandidateFixture([]Group{group}, []Account{globalCandidateAccount(1, 1, group.ID)})
	body := []byte(`{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	ctx, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body, "", "")
	require.NoError(t, err)
	require.NotNil(t, state)
	first, ok := ProtocolRoutingRequest(ctx)
	require.True(t, ok)

	reused := r.WithRequestProfile(ctx, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body, state.requestProfile)
	second, ok := ProtocolRoutingRequest(reused)
	require.True(t, ok)
	require.Equal(t, first.Digest(), second.Digest())
}

func TestPrepareCandidateRequestKeepsAlreadyRoutedRequestAuthoritative(t *testing.T) {
	group := grp(10, PlatformOpenAI, 1, false)
	r, _, key := globalCandidateFixture([]Group{group}, []Account{globalCandidateAccount(1, 1, group.ID)})
	routedBody := []byte(`{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"routed"}]}`)
	candidateBody := []byte(`{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"candidate copy"}]}`)

	routedCtx := r.WithRequest(context.Background(), ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", routedBody)
	routed, ok := ProtocolRoutingRequest(routedCtx)
	require.True(t, ok)

	ctx, state, err := r.PrepareCandidateRequest(routedCtx, key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", candidateBody, "", "")
	require.NoError(t, err)
	require.NotNil(t, state)
	require.True(t, state.requestProfileValid)
	got, ok := ProtocolRoutingRequest(ctx)
	require.True(t, ok)
	require.Equal(t, routed.Digest(), got.Digest())
}
