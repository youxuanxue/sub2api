//go:build unit

package service

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Regression: a newapi (fifth platform) Qwen/DashScope account must never have
// its channel credential sent to api.openai.com.
//
// Inbound /v1/messages reached ForwardAsAnthropic without the bridge dispatcher,
// so a newapi api-key account fell through tkTryRouteForwardAsAnthropic's last
// predicate into the OpenAI-shaped raw Chat Completions fallback. That path
// resolves its upstream from OpenAI-family accessors, which return "" for
// platform=newapi, and the caller turned "" into the official OpenAI host while
// still sending the account's real DashScope key — producing upstream
// "Incorrect API key provided", the exact shape
// IsForeignCredentialOfficialOpenAIReject already classifies as a routing
// defect rather than a dead credential.
func newapiQwenAccountForLeakTest(name string, id int64) *Account {
	return &Account{
		ID:          id,
		Name:        name,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{
			"api_key":       "sk-dashscope-not-an-openai-key",
			"base_url":      "https://dashscope.aliyuncs.com",
			"model_mapping": map[string]any{"qwen3-max": "qwen3-max"},
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

// The credential does reach the wire on the native path, so the destination is
// the only thing standing between a DashScope key and api.openai.com.
func TestNewAPIQwenCredentialIsWireBoundOnNativePath(t *testing.T) {
	for _, name := range []string{"Qwen", "Qwen-2", "Qwen-4"} {
		t.Run(name, func(t *testing.T) {
			account := newapiQwenAccountForLeakTest(name, 60)
			require.Equal(t, "sk-dashscope-not-an-openai-key", account.GetOpenAIProtocolAPIKey())
			require.False(t, AccountUsesOfficialOpenAIUpstream(account))
			require.False(t, OfficialOpenAIFallbackAllowed(account),
				"a newapi channel credential must never be allowed to default to api.openai.com")
		})
	}
}

// Primary fix: inbound /v1/messages for a bridge-eligible newapi account is
// owned by the NewAPI adaptor, never by an OpenAI-shaped fallback.
func TestNewAPIBridgeOwnsAnthropicMessagesForQwen(t *testing.T) {
	svc := &OpenAIGatewayService{}

	for _, name := range []string{"Qwen", "Qwen-2", "Qwen-4"} {
		require.True(t, svc.newAPIBridgeOwnsAnthropicMessages(newapiQwenAccountForLeakTest(name, 60)),
			"inbound /v1/messages for %s must relay through the NewAPI adaptor", name)
	}

	// An OpenAI api-key account keeps the existing OpenAI-shaped fallbacks.
	require.False(t, svc.newAPIBridgeOwnsAnthropicMessages(&Account{
		ID:          76,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-openai"},
	}))

	// A newapi row with no channel type cannot enter the bridge.
	require.False(t, svc.newAPIBridgeOwnsAnthropicMessages(&Account{
		ID:          61,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 0,
		Credentials: map[string]any{"api_key": "sk-x", "base_url": "https://dashscope.aliyuncs.com"},
	}))
}

// Defense in depth: even a newapi row that cannot enter the bridge (channel_type
// unset, so ShouldDispatchToNewAPIBridge is false) must fail closed rather than
// POST its foreign credential to the official OpenAI host.
func TestChatCompletionsTargetFailsClosedForForeignCredential(t *testing.T) {
	svc := &OpenAIGatewayService{}

	offBridge := &Account{
		ID:          61,
		Name:        "Qwen-no-channel-type",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 0,
		Credentials: map[string]any{
			"api_key":  "sk-dashscope-not-an-openai-key",
			"base_url": "https://dashscope.aliyuncs.com",
		},
	}
	_, err := svc.openAIChatCompletionsTargetURL(offBridge)
	require.ErrorIs(t, err, ErrForeignCredentialOfficialOpenAIFallback,
		"a foreign credential with no OpenAI-resolvable base must not fall back to api.openai.com")

	// An OpenAI api-key account still resolves the official host as before.
	official := &Account{
		ID:          76,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-openai", "base_url": "https://api.openai.com"},
	}
	targetURL, err := svc.openAIChatCompletionsTargetURL(official)
	require.NoError(t, err)
	require.Contains(t, targetURL, "api.openai.com")

	_, err = svc.openAIChatCompletionsTargetURL(&Account{
		ID:          77,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-openai"},
	})
	require.ErrorIs(t, err, ErrForeignCredentialOfficialOpenAIFallback,
		"a configurable OpenAI API-key account must provide an explicit base_url")
}

// Defense in depth for the plan-aware Responses transport: a foreign API key
// with no protocol plan must fail before buildUpstreamRequest can construct an
// api.openai.com request. Governed traffic supplies an explicit plan endpoint;
// this test covers the legacy/non-governed boundary where no such endpoint
// exists.
func TestResponsesTargetFailsClosedForForeignCredentialWithoutPlan(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := newapiQwenAccountForLeakTest("Qwen", 60)
	body := []byte(`{"model":"qwen3-max","input":"hello","stream":false}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	_, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body,
		"sk-dashscope-not-an-openai-key", false, "", false,
	)
	require.ErrorIs(t, err, ErrForeignCredentialOfficialOpenAIFallback,
		"an unresolved foreign Responses credential must fail before api.openai.com is selected")
}

func TestResponsesTargetRequiresPlanOrExplicitOfficialProfile(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":"hello","stream":false}`)

	tests := []struct {
		name        string
		account     *Account
		wantErr     bool
		wantURLPart string
	}{
		{
			name: "foreign api key",
			account: &Account{
				Platform: PlatformNewAPI,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"api_key":  "foreign-api-key",
					"base_url": "https://foreign.example/v1",
				},
			},
			wantErr: true,
		},
		{
			name: "foreign upstream",
			account: &Account{
				Platform: PlatformAntigravity,
				Type:     AccountTypeUpstream,
				Credentials: map[string]any{
					"api_key":  "foreign-upstream-key",
					"base_url": "https://foreign.example/v1",
				},
			},
			wantErr: true,
		},
		{
			name:    "foreign setup token",
			account: &Account{Platform: PlatformGrok, Type: AccountTypeSetupToken},
			wantErr: true,
		},
		{
			name:    "foreign oauth",
			account: &Account{Platform: PlatformGrok, Type: AccountTypeOAuth},
			wantErr: true,
		},
		{
			name:    "openai api key missing base url",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			wantErr: true,
		},
		{
			name: "explicit official api key",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"base_url": "https://api.openai.com",
				},
			},
			wantURLPart: "https://api.openai.com/v1/responses",
		},
		{
			name:        "official oauth",
			account:     &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
			wantURLPart: chatgptCodexURL,
		},
		{
			name:        "official setup token",
			account:     &Account{Platform: PlatformOpenAI, Type: AccountTypeSetupToken},
			wantURLPart: chatgptCodexURL,
		},
		{
			name: "explicit openai upstream",
			account: &Account{
				Platform:    PlatformOpenAI,
				Type:        AccountTypeUpstream,
				Credentials: map[string]any{"base_url": "https://relay.example/v1"},
			},
			wantURLPart: "https://relay.example/v1/responses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

			req, err := (&OpenAIGatewayService{}).buildUpstreamRequest(
				context.Background(), c, tt.account, body, "credential", false, "", false,
			)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrForeignCredentialOfficialOpenAIFallback)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantURLPart, req.URL.String())
		})
	}
}

// The native Messages resolver is another legacy boundary. Without an
// immutable protocol plan, an unresolved foreign credential must not inherit
// the official OpenAI host and become /v1/messages there.
func TestNativeMessagesTargetFailsClosedForForeignCredentialWithoutPlan(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := newapiQwenAccountForLeakTest("Qwen", 60)

	_, err := svc.nativeAnthropicMessagesTargetURL(account)
	require.ErrorIs(t, err, ErrForeignCredentialOfficialOpenAIFallback,
		"an unresolved foreign Messages credential must not default to api.openai.com")

	_, err = svc.nativeAnthropicMessagesTargetURL(&Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
	})
	require.ErrorIs(t, err, ErrForeignCredentialOfficialOpenAIFallback,
		"a configurable OpenAI API-key account must provide an explicit base_url")

	officialURL, err := svc.nativeAnthropicMessagesTargetURL(&Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.openai.com",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "https://api.openai.com/v1/messages", officialURL)
}
