//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const officialOpenAIAPIKeyHelpText = `{"error":{"message":"Incorrect API key provided: sk-test. You can find your API key at https://platform.openai.com/account/api-keys."}}`

func TestAccountUsesOfficialOpenAIUpstream_DerivedInventory(t *testing.T) {
	for _, platform := range officialOpenAIUpstreamPlatformInventory() {
		account := &Account{Platform: platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-test"}}
		got := AccountUsesOfficialOpenAIUpstream(account)
		require.False(t, got, "platform %s without explicit base_url must not send credentials to api.openai.com", platform)
	}

	for ct := 1; ct < newapiconstant.ChannelTypeDummy; ct++ {
		account := &Account{
			Platform:    PlatformNewAPI,
			Type:        AccountTypeAPIKey,
			ChannelType: ct,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": "https://dashscope.aliyuncs.com",
			},
		}
		require.False(t, AccountUsesOfficialOpenAIUpstream(account), "newapi channel_type=%d must stay off the official OpenAI host", ct)
	}
}

func TestAccountUsesOfficialOpenAIUpstream_OfficialAllowlistAndBoundaries(t *testing.T) {
	require.True(t, AccountUsesOfficialOpenAIUpstream(&Account{
		Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth"},
	}))
	require.True(t, AccountUsesOfficialOpenAIUpstream(&Account{
		Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.openai.com"},
	}))
	require.True(t, AccountUsesOfficialOpenAIUpstream(&Account{
		Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.openai.com/v1"},
	}))
	require.False(t, AccountUsesOfficialOpenAIUpstream(&Account{
		Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
	}))

	// Boundary samples the owner cannot derive: OpenAI-platform relays with a foreign host.
	require.False(t, AccountUsesOfficialOpenAIUpstream(&Account{
		Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.cloudwise.ai/api"},
	}))
	require.False(t, AccountUsesOfficialOpenAIUpstream(&Account{
		Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://agent.tokensea.ai"},
	}))
	require.False(t, AccountUsesOfficialOpenAIUpstream(&Account{
		Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://relay.example/v1"},
	}))
	require.False(t, AccountUsesOfficialOpenAIUpstream(nil))
}

func TestAccountShouldLocalEstimateCountTokens_DerivedForeignAccounts(t *testing.T) {
	for _, platform := range officialOpenAIUpstreamPlatformInventory() {
		account := &Account{Platform: platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-test"}}
		if platform == PlatformGrok {
			// Grok has its own count_tokens handler / native URL resolver.
			continue
		}
		require.True(t, AccountShouldLocalEstimateCountTokens(account), "platform %s count_tokens must not default to api.openai.com", platform)
	}

	for ct := 1; ct < newapiconstant.ChannelTypeDummy; ct++ {
		account := &Account{
			Platform:    PlatformNewAPI,
			Type:        AccountTypeAPIKey,
			ChannelType: ct,
			Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://dashscope.aliyuncs.com"},
		}
		require.True(t, AccountShouldLocalEstimateCountTokens(account), "newapi channel_type=%d must local-estimate count_tokens", ct)
	}

	require.False(t, AccountShouldLocalEstimateCountTokens(&Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Credentials: map[string]any{"api_key": "ark-test", "base_url": newapiintegration.VolcEngineAgentPlanBaseURL},
	}), "Agent Plan keeps its dedicated Ark input_tokens URL")
	require.False(t, AccountShouldLocalEstimateCountTokens(&Account{
		Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.cloudwise.ai/api"},
	}), "CloudWise still probes its own host, never api.openai.com")
}

func TestIsOfficialOpenAIAPIKeyHelpText(t *testing.T) {
	require.True(t, IsOfficialOpenAIAPIKeyHelpText(http.StatusUnauthorized, []byte(officialOpenAIAPIKeyHelpText)))
	require.False(t, IsOfficialOpenAIAPIKeyHelpText(http.StatusUnauthorized, []byte(`{"error":{"message":"Invalid API key"}}`)))
	require.False(t, IsOfficialOpenAIAPIKeyHelpText(http.StatusForbidden, []byte(officialOpenAIAPIKeyHelpText)))
}

func TestHandleUpstreamError_ForeignCredentialOfficialOpenAIReject_Derived(t *testing.T) {
	for _, account := range foreignCredentialOfficialOpenAIRejectFixtures() {
		t.Run(account.Name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			svc := &RateLimitService{accountRepo: repo}
			shouldDisable := svc.HandleUpstreamError(
				context.Background(), account, http.StatusUnauthorized, http.Header{}, []byte(officialOpenAIAPIKeyHelpText))
			require.False(t, shouldDisable)
			require.Zero(t, repo.setErrorCalls, "official OpenAI help text on a foreign credential is a routing defect")
		})
	}
}

func TestHandleUpstreamError_OfficialOpenAIHelpTextStillDisablesOfficialAccount(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := &Account{
		ID: 1, Name: "openai-official", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.openai.com"},
		Status:      StatusActive, Schedulable: true,
	}
	shouldDisable := svc.HandleUpstreamError(
		context.Background(), account, http.StatusUnauthorized, http.Header{}, []byte(officialOpenAIAPIKeyHelpText))
	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
}

func TestHandleUpstreamError_ForeignGenuineAuthFailureStillDisables(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := &Account{
		ID: 60, Name: "Qwen", Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://dashscope.aliyuncs.com"},
		Status:      StatusActive, Schedulable: true,
	}
	shouldDisable := svc.HandleUpstreamError(
		context.Background(), account, http.StatusUnauthorized, http.Header{},
		[]byte(`{"error":{"message":"Invalid API key"}}`))
	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
}

func TestForwardCountTokensAsAnthropic_NewAPIAliWithAPIKeyNeverHitsOfficialOpenAI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hello"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(officialOpenAIAPIKeyHelpText)),
	}}
	repo := &countTokensRuntimeStateRepo{}
	svc := &OpenAIGatewayService{
		httpUpstream:     upstream,
		rateLimitService: &RateLimitService{accountRepo: repo},
	}
	account := &Account{
		ID:          60,
		Name:        "Qwen",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeAli,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-6f753test",
			"base_url": "https://dashscope.aliyuncs.com",
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	err := svc.ForwardCountTokensAsAnthropic(context.Background(), c, account, body, "qwen3.7-max")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Greater(t, int(gjson.GetBytes(rec.Body.Bytes(), "input_tokens").Int()), 0)
	require.Nil(t, upstream.lastReq, "foreign newapi credentials must never reach api.openai.com")
	require.Zero(t, repo.setErrorCalls)
}

func officialOpenAIUpstreamPlatformInventory() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, platform := range append(append([]string{}, AllowedQuotaPlatforms...), AllSchedulingPlatforms()...) {
		if _, ok := seen[platform]; ok {
			continue
		}
		seen[platform] = struct{}{}
		out = append(out, platform)
	}
	sort.Strings(out)
	return out
}

func foreignCredentialOfficialOpenAIRejectFixtures() []*Account {
	var accounts []*Account
	for _, platform := range officialOpenAIUpstreamPlatformInventory() {
		if platform == PlatformOpenAI {
			continue
		}
		accounts = append(accounts, &Account{
			ID: 100, Name: "platform-" + platform, Platform: platform, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "sk-test"},
			Status:      StatusActive, Schedulable: true,
		})
	}
	accounts = append(accounts,
		&Account{
			ID: 60, Name: "newapi-ali", Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
			ChannelType: newapiconstant.ChannelTypeAli,
			Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://dashscope.aliyuncs.com"},
			Status:      StatusActive, Schedulable: true,
		},
		&Account{
			ID: 95, Name: "cloudwise", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.cloudwise.ai/api"},
			Status:      StatusActive, Schedulable: true,
		},
		&Account{
			ID: 92, Name: "tokensea", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://agent.tokensea.ai"},
			Status:      StatusActive, Schedulable: true,
		},
	)
	return accounts
}
