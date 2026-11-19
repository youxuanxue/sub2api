//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Synthetic accounts share an ordinary OpenAI group; no provider traffic is sent.
func passthroughModelFilterAccounts(passthrough bool, oauthPriority int) []Account {
	return []Account{
		{
			ID: 1, Name: "bailian-compatible", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 1,
			GroupIDs: []int64{17}, RateMultiplier: f64p(1),
			Credentials: map[string]any{
				"api_key": "synthetic-key", "base_url": "https://bailian.example",
				"model_mapping": map[string]any{"qwen3.8-max": "qwen3.8-max"},
			},
			Extra: map[string]any{"openai_passthrough": passthrough},
		},
		{
			ID: 2, Name: "openai-oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: oauthPriority,
			GroupIDs: []int64{17}, RateMultiplier: f64p(1),
			Credentials: map[string]any{
				"access_token":  "synthetic-token",
				"model_mapping": map[string]any{"gpt-6.1-sol": "gpt-6.1-sol"},
			},
		},
	}
}

func TestOpenAIPassthroughModelFilterEligibility(t *testing.T) {
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		t.Run(accountType, func(t *testing.T) {
			for _, tc := range []struct {
				name  string
				flag  any
				model string
				want  bool
			}{
				{"default ignores leftover mapping", nil, "gpt-6.1-sol", true},
				{"disabled ignores leftover mapping", false, "gpt-6.1-sol", true},
				{"invalid flag does not opt in", "true", "gpt-6.1-sol", true},
				{"opt in rejects other provider", true, "gpt-6.1-sol", false},
				{"opt in accepts exact source key", true, "qwen3.8-max", true},
				{"opt in accepts wildcard source key", true, "qwen3.8-plus", true},
				{"mapping targets do not grant access", true, "upstream-only", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					account := &Account{
						Platform: PlatformOpenAI, Type: accountType,
						Extra: map[string]any{"openai_passthrough": true, "openai_passthrough_model_filter": tc.flag},
						Credentials: map[string]any{"model_mapping": map[string]any{
							"qwen3.8-max": "upstream-only", "qwen3.8-*": "wildcard-target",
						}},
					}
					require.Equal(t, tc.want, account.IsModelSupported(tc.model))
					// TokenKey scheduling preserves its mapping whitelist even when the
					// direct legacy account method is permissive by default.
					wantScheduled := tc.model == "qwen3.8-max" || tc.model == "qwen3.8-plus"
					require.Equal(t, wantScheduled, (&GatewayService{}).isModelSupportedByAccount(account, tc.model))
				})
			}
			account := &Account{Platform: PlatformOpenAI, Type: accountType,
				Extra: map[string]any{"openai_passthrough": true, "openai_passthrough_model_filter": true}}
			require.True(t, account.IsModelSupported("qwen3.8-max"), "empty restrictions still allow all models")
		})
	}
}

func TestOpenAIPassthroughModelFilterMixedPoolSelection(t *testing.T) {
	groupID := int64(17)
	for _, mode := range []string{"legacy", "load-batch"} {
		t.Run(mode, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				filter   bool
				model    string
				excluded map[int64]struct{}
				wantID   int64
			}{
				{"default preserves scheduling whitelist", false, "gpt-6.1-sol", nil, 2},
				{"filter chooses GPT account", true, "gpt-6.1-sol", nil, 2},
				{"filter keeps Qwen account", true, "qwen3.8-max", nil, 1},
				{"failover never leaks GPT to Qwen", true, "gpt-6.1-sol", map[int64]struct{}{2: {}}, 0},
			} {
				t.Run(tc.name, func(t *testing.T) {
					accounts := passthroughModelFilterAccounts(true, 2)
					accounts[0].Extra["openai_passthrough_model_filter"] = tc.filter
					repo := schedulerTestOpenAIAccountRepo{accounts: accounts}
					cfg := &config.Config{}
					cfg.Gateway.Scheduling.LoadBatchEnabled = mode == "load-batch"
					svc := &OpenAIGatewayService{accountRepo: repo, cfg: cfg}
					var selected *Account
					var err error
					if mode == "legacy" {
						selected, err = svc.SelectAccountForModelWithExclusions(context.Background(), &groupID, "", tc.model, tc.excluded)
					} else {
						svc.concurrencyService = NewConcurrencyService(stubConcurrencyCache{})
						selection, selectErr := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", tc.model, tc.excluded)
						err = selectErr
						if selection != nil {
							selected = selection.Account
							if selection.ReleaseFunc != nil {
								defer selection.ReleaseFunc()
							}
						}
					}
					if tc.wantID == 0 {
						require.Error(t, err)
						require.Nil(t, selected)
						return
					}
					require.NoError(t, err)
					require.NotNil(t, selected)
					require.Equal(t, tc.wantID, selected.ID)
				})
			}
		})
	}
}

func TestOpenAIPassthroughModelFilterPreservesForwardBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts := passthroughModelFilterAccounts(true, 2)
	accounts[0].Extra["openai_passthrough_model_filter"] = true
	accounts[0].Credentials["model_mapping"] = map[string]any{"qwen3.8-max": "must-not-rewrite"}
	repo := schedulerTestOpenAIAccountRepo{accounts: accounts}
	body := []byte(`{ "model": "qwen3.8-max", "input":"synthetic query", "tools":[{"type":"web_search"}], "stream":false }`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}}
	svc := &OpenAIGatewayService{accountRepo: repo, cfg: &config.Config{}, httpUpstream: upstream}
	groupID := int64(17)
	account, err := svc.SelectAccountForModelWithExclusions(context.Background(), &groupID, "", "qwen3.8-max", nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), account.ID)
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "qwen3.8-max", gjson.GetBytes(upstream.lastBody, "model").String(), "filtering must never apply the mapping target")
	require.NotEmpty(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String(), "TokenKey still injects a stable prompt cache key")
	forwardedWithoutCache, delErr := sjson.DeleteBytes(upstream.lastBody, "prompt_cache_key")
	require.NoError(t, delErr)
	require.JSONEq(t, string(body), string(forwardedWithoutCache))
	require.Equal(t, "https://bailian.example/v1/responses", upstream.lastReq.URL.String())
}

func TestOpenAIPassthroughModelFilterGroupCatalog(t *testing.T) {
	accounts := passthroughModelFilterAccounts(true, 2)
	accounts[0].Extra["openai_passthrough_model_filter"] = true
	svc := &GatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}}
	groupID := int64(17)
	models := svc.GetAvailableModels(context.Background(), &groupID, PlatformOpenAI)
	require.ElementsMatch(t, []string{"qwen3.8-max", "gpt-6.1-sol"}, models,
		"a restricted passthrough account must not contribute the unrestricted default catalog")
}
