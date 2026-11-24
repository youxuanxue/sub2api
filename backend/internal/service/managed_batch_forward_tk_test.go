//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/batchadapter"
	"github.com/Wei-Shaw/sub2api/internal/batchcontract"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Run the worker's actual Responses runner and online relay against an in-memory
// Chat upstream. Only the repository and HTTP transport are fixtures.
func TestManagedBatchVolcGLMResponsesToChat(t *testing.T) {
	for _, mode := range []string{RoutingModeDirect, RoutingModeUniversal} {
		t.Run(mode, func(t *testing.T) {
			account := volcAgentPlanKimiAccountForTest()
			const model = "glm-5.3-flash"
			account.Credentials["model_mapping"] = map[string]any{model: model}
			attachTestProtocolCapability(account, protocolrouter.ProtocolChatCompletions)
			body := []byte(`{"model":"glm-5.3-flash","input":"hello","stream":false}`)
			group := grp(10, PlatformNewAPI, 0, false)
			group.Name = "china"
			account.GroupIDs = []int64{group.ID}
			owner, _, key := globalCandidateFixture([]Group{group}, []Account{*account})
			cache := &candidateQuotaCache{}
			cache.balance = 100
			billing := NewBillingCacheService(cache, nil, nil, nil, &candidateRPMCache{}, nil, &config.Config{}, nil)
			t.Cleanup(billing.Stop)
			owner.candidateGateway.billingCacheService = billing
			key.RoutingMode = mode
			key.Status = StatusActive
			key.User.Status = StatusActive
			if mode == RoutingModeDirect {
				key.Group = &group
				key.GroupID = &group.ID
			}
			ctx := context.Background()
			resolver := ManagedBatchExecutionResolver{APIKeys: managedItemKeySource{key, owner}, Gateway: owner.candidateOpenAI, Router: owner.router}
			const endpoint = "/v1/responses"
			picked, err := resolver.Resolve(ctx, batchadapter.ExecutionRequest{BatchID: "b", UserID: key.UserID, APIKeyID: key.ID}, batchadapter.ExecutionBatch{ID: "b", UserID: key.UserID, APIKeyID: key.ID, Endpoint: endpoint}, batchadapter.ExecutionItem{ID: 1, BatchID: "b", CustomID: "one"}, batchcontract.RequestLine{CustomID: "one", Method: "POST", URL: endpoint, Body: body})
			require.NoError(t, err)
			defer picked.Release()
			require.Equal(t, batchadapter.ProviderNewAPI, picked.Provider)
			execution, ok := ManagedBatchExecutionFromContext(picked.Context)
			require.True(t, ok)
			sel := execution.Selection
			require.Equal(t, protocolrouter.AdapterResponsesToChat, sel.ProtocolPlan.AdapterID())
			require.Equal(t, group.ID, *execution.APIKey.GroupID)
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"chatcmpl-test","object":"chat.completion","model":"glm-5.3-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`))}}
			gateway := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream, accountRepo: &stubOpenAIAccountRepo{accounts: []Account{*account}}}
			exec := picked.Context
			runner := &ManagedResponsesRunner{Router: owner.router, Gateway: gateway}
			out, err := batchadapter.NewNewAPIResponsesAdapter(runner).Execute(ctx, batchadapter.ManagedRequest{ExecutionContext: exec, AccountID: account.ID, Endpoint: "/v1/responses", Method: "POST", Model: model, Body: body})
			require.NoError(t, err)
			require.Nil(t, out.Error)
			require.Equal(t, 200, out.Response.StatusCode)
			require.Equal(t, "ok", gjson.GetBytes(out.Response.Body, "output.0.content.0.text").String())
			require.Equal(t, 7, out.Usage.InputTokens)
			require.Equal(t, 2, out.Usage.OutputTokens)
			require.Equal(t, "hello", gjson.GetBytes(upstream.lastBody, "messages.0.content").String())
			require.True(t, strings.HasSuffix(upstream.lastReq.URL.Path, "/chat/completions"))
			// No successful fallback to a different account when the frozen selection is lost.
			sel.Account = &Account{ID: account.ID + 1, Platform: PlatformNewAPI}
			require.Empty(t, managedBatchPlanProvider(sel))
			_, err = runner.Run(ctx, batchadapter.ProviderRequest{ExecutionContext: exec, AccountID: account.ID})
			require.ErrorIs(t, err, ErrManagedBatchExecutionUnavailable)
		})
	}
}
