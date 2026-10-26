//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func withGatewayBodyParseStats(ctx context.Context, stats *gatewayBodyParseStats) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, gatewayBodyParseStatsKey{}, stats)
}

func TestChatAdmissionReusesSnapshotAndSkipsClaudeDocument(t *testing.T) {
	group := grp(10, PlatformOpenAI, 1, false)
	accounts := []Account{globalCandidateAccount(1, 1, group.ID), globalCandidateAccount(2, 2, group.ID)}
	resolver, repo, key := globalCandidateFixture([]Group{group}, accounts)
	loadReads := 0
	resolver.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			1: {AccountID: 1},
			2: {AccountID: 2},
		},
		loadReads: &loadReads,
	})
	body := []byte(`{"model":"gpt-5.4","stream":false,"messages":[{"role":"user","content":"` + strings.Repeat("x", 64*1024) + `"}]}`)
	stats := &gatewayBodyParseStats{}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/1.2.3")
	c.Request = c.Request.WithContext(withGatewayBodyParseStats(c.Request.Context(), stats))

	state, err := resolver.PrepareCandidateIngress(c, key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body, "")
	require.NoError(t, err)
	require.Equal(t, int64(1), state.current.account.ID)
	require.True(t, state.admissionPathsValid, "ingress admission should retain static candidate paths for the immediate acquiring pass")
	require.True(t, IsClaudeCodeClient(c.Request.Context()))
	require.Zero(t, stats.claudeDocument, "chat completions must not unmarshal the body for Claude Code detection")
	require.Equal(t, 1, stats.canonical, "admission decodes the canonical profile once")
	require.Equal(t, 1, repo.listCalls)
	require.Equal(t, 1, loadReads)
	reused, ok := ReuseParsedCanonicalRequest(c.Request.Context(), protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone, "gpt-5.4", false, body)
	require.True(t, ok)
	require.True(t, reused.BodyEquals(body))
	require.True(t, CanonicalRequestBodyValidated(c.Request.Context(), protocolrouter.ProtocolChatCompletions, body))
	_, ok = ReuseParsedCanonicalRequest(c.Request.Context(), protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone, "gpt-5.4", false, append(append([]byte(nil), body...), ' '))
	require.False(t, ok)

	selected, handled, err := selectCandidateFromContext(c.Request.Context(), candidateSelectOptions{
		transport:  OpenAIUpstreamTransportAny,
		capability: OpenAIEndpointCapabilityChatCompletions,
		acquire:    true,
	})
	require.NoError(t, err)
	require.True(t, handled)
	require.NotNil(t, selected)
	require.True(t, selected.Acquired)
	require.Equal(t, int64(1), selected.Account.ID)
	require.True(t, state.admissionPathsUsed, "the immediate acquiring pass should reuse the admission candidate projection")
	require.Equal(t, 1, repo.listCalls, "scheduler select must reuse the admission account list")
	require.Equal(t, 2, loadReads, "the acquiring select reads load again after the admission gap")
	require.Equal(t, 1, stats.canonical)
	require.Zero(t, stats.claudeDocument)
	selected.ReleaseFunc()

	failover, err := state.selectAccount(c.Request.Context(), candidateSelectOptions{
		excluded:   map[int64]struct{}{1: {}},
		transport:  OpenAIUpstreamTransportAny,
		capability: OpenAIEndpointCapabilityChatCompletions,
		acquire:    true,
	})
	require.NoError(t, err)
	require.True(t, failover.Acquired)
	require.Equal(t, int64(2), failover.Account.ID, "failover must leave the failed account and take the next slot")
	require.Equal(t, 1, repo.listCalls)
	require.Equal(t, 3, loadReads, "failover reads load again for the reduced account set")
	failover.ReleaseFunc()
}

func TestChatAdmissionRejectsUnsupportedModel(t *testing.T) {
	group := grp(10, PlatformOpenAI, 1, false)
	account := globalCandidateAccount(1, 1, group.ID)
	account.Credentials["model_mapping"] = map[string]any{"gpt-4.1": "gpt-4.1"}
	resolver, _, key := globalCandidateFixture([]Group{group}, []Account{account})
	var acquired []int64
	resolver.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquiredIDs: &acquired})
	_, _, err := resolver.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`), "", "")
	require.ErrorIs(t, err, ErrUniversalUnsupportedModel)
	require.Empty(t, acquired, "an ineligible model must not take a slot")
}

func TestChatSchedulerFullSlotStillReturnsWaitPlan(t *testing.T) {
	group := grp(10, PlatformOpenAI, 1, false)
	account := globalCandidateAccount(1, 1, group.ID)
	resolver, repo, key := globalCandidateFixture([]Group{group}, []Account{account})
	loadReads := 0
	resolver.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
		loadMap:   map[int64]*AccountLoadInfo{account.ID: {AccountID: account.ID, CurrentConcurrency: account.Concurrency}},
		loadReads: &loadReads,
	})
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`)
	ctx, state, err := resolver.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body, "", "")
	require.NoError(t, err)
	require.Equal(t, account.ID, state.current.account.ID)

	selected, handled, err := selectCandidateFromContext(ctx, candidateSelectOptions{
		transport:  OpenAIUpstreamTransportAny,
		capability: OpenAIEndpointCapabilityChatCompletions,
		acquire:    true,
	})
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, selected.Acquired)
	require.NotNil(t, selected.WaitPlan)
	require.Equal(t, account.ID, selected.WaitPlan.AccountID)
	require.Equal(t, 1, repo.listCalls)
	require.Equal(t, 2, loadReads, "a full pool still rechecks live load before returning WaitPlan")
}

func TestMessagesClaudeValidationStillDecodesDocument(t *testing.T) {
	group := grp(10, PlatformAnthropic, 1, false)
	resolver, _, key := globalCandidateFixture([]Group{group}, []Account{globalCandidateAccount(1, 1, group.ID)})
	stats := &gatewayBodyParseStats{}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/1.2.3")
	c.Request = c.Request.WithContext(withGatewayBodyParseStats(c.Request.Context(), stats))
	body := []byte(`{"model":"claude-sonnet-4","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	_, _ = resolver.PrepareCandidateIngress(c, key, ShapeAnthropicMessages, "/v1/messages", "claude-sonnet-4", body, "")
	require.Equal(t, 1, stats.claudeDocument, "messages validation still decodes the document it inspects")
}
