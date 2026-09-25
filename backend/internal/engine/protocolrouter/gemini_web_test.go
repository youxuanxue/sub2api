package protocolrouter

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeminiWebPlanSingleTurnAndGeneralProviderRegression(t *testing.T) {
	router := New(allTestAdapters())
	for _, tc := range []struct {
		name     string
		protocol Protocol
		body     string
		accepted bool
	}{
		{"chat", ProtocolChatCompletions, `{"messages":[{"role":"user","content":"Draw a cube"}]}`, true},
		{"messages_missing_required_limit", ProtocolMessages, `{"messages":[{"role":"user","content":[{"type":"text","text":"Draw a cube"}]}]}`, false},
		{"responses", ProtocolResponses, `{"input":"Draw a cube"}`, true},
		{"responses_message", ProtocolResponses, `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Draw a cube"}]}]}`, true},
		{"native", ProtocolGeminiGenerateContent, `{"contents":[{"parts":[{"text":"Draw a cube"}]}]}`, true},
		{"chat_history", ProtocolChatCompletions, `{"messages":[{"role":"user","content":"one"},{"role":"assistant","content":"two"},{"role":"user","content":"three"}]}`, false},
		{"chat_system", ProtocolChatCompletions, `{"messages":[{"role":"system","content":"instruction"},{"role":"user","content":"hello"}]}`, false},
		{"messages_system", ProtocolMessages, `{"system":"instruction","messages":[{"role":"user","content":"hello"}]}`, false},
		{"messages_explicit_limit", ProtocolMessages, `{"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`, true},
		{"responses_instructions", ProtocolResponses, `{"instructions":"instruction","input":"hello"}`, false},
		{"responses_explicit_limit", ProtocolResponses, `{"max_output_tokens":64,"input":"hello"}`, true},
		{"responses_continuation", ProtocolResponses, `{"previous_response_id":"resp_1","input":"hello"}`, false},
		{"native_system", ProtocolGeminiGenerateContent, `{"systemInstruction":{"parts":[{"text":"instruction"}]},"contents":[{"parts":[{"text":"hello"}]}]}`, false},
		{"native_image", ProtocolGeminiGenerateContent, `{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}]}`, false},
		{"native_tools", ProtocolGeminiGenerateContent, `{"tools":[],"contents":[{"parts":[{"text":"hello"}]}]}`, false},
		{"native_cache", ProtocolGeminiGenerateContent, `{"cachedContent":"cached/1","contents":[{"parts":[{"text":"hello"}]}]}`, false},
		{"chat_image_config", ProtocolChatCompletions, `{"messages":[{"role":"user","content":"Draw a cube"}],"generationConfig":{"imageConfig":{"aspectRatio":"4:3"},"responseModalities":["TEXT","IMAGE"]}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := ParseCanonicalRequest(tc.protocol, ResponsesPathRoot, "nano-2", false, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			web := testAccount(t, ProtocolGeminiGenerateContent)
			web.geminiProfile = GeminiEndpointNativeAPIKey
			web.providerCapability = ProviderCapabilityGeminiWeb
			web.imageOutput = true
			for _, stream := range []bool{false, true} {
				request.profile.Stream = stream
				plan, err := router.Plan(request, web)
				if tc.accepted {
					if err != nil {
						t.Fatal(err)
					}
					if plan.TargetProtocol() != ProtocolGeminiGenerateContent {
						t.Fatalf("target = %s", plan.TargetProtocol())
					}
				} else if !errors.Is(err, ErrNoLegalRoute) {
					t.Fatalf("unsupported request err = %v", err)
				}
			}
			// The Web profile must not impose history/system restrictions on general Gemini.
			if tc.name == "chat_history" || tc.name == "chat_system" || tc.name == "messages_system" || tc.name == "native_system" {
				web.providerCapability = ProviderCapabilityGeneral
				if _, err := router.Plan(request, web); err != nil {
					t.Fatalf("general Gemini regressed: %v", err)
				}
			}
		})
	}
}

func TestGeminiWebBestEffortBudgetsUseImmutableAuditedPlan(t *testing.T) {
	for _, tc := range []struct {
		protocol    Protocol
		body, field string
	}{
		{ProtocolMessages, `{"max_tokens":128,"messages":[{"role":"user","content":"draw"}],"generationConfig":{"imageConfig":{"aspectRatio":"4:3"},"responseModalities":["TEXT","IMAGE"]}}`, "max_tokens"},
		{ProtocolChatCompletions, `{"max_tokens":128,"messages":[{"role":"user","content":"draw"}]}`, "max_tokens"},
		{ProtocolChatCompletions, `{"max_completion_tokens":128,"messages":[{"role":"user","content":"draw"}]}`, "max_completion_tokens"},
		{ProtocolResponses, `{"max_output_tokens":128,"input":"draw"}`, "max_output_tokens"},
		{ProtocolGeminiGenerateContent, `{"contents":[{"parts":[{"text":"draw"}]}],"generationConfig":{"maxOutputTokens":128,"imageConfig":{"aspectRatio":"4:3"},"responseModalities":["TEXT","IMAGE"]}}`, "generationConfig.maxOutputTokens"},
	} {
		t.Run(string(tc.protocol)+"/"+tc.field, func(t *testing.T) {
			request, err := ParseCanonicalRequest(tc.protocol, ResponsesPathRoot, "nano-2", false, []byte(tc.body))
			require.NoError(t, err)
			digest := request.Digest()
			for _, web := range []bool{false, true} {
				account := testAccount(t, ProtocolGeminiGenerateContent)
				account.imageOutput = true
				if web {
					account.providerCapability = ProviderCapabilityGeminiWeb
				}
				adapter := &recordingAdapter{}
				router := New(AdapterCatalog{AdapterMessagesToGemini: adapter, AdapterChatToGemini: adapter, AdapterResponsesToGemini: adapter, AdapterGeminiIdentity: adapter})
				plan, err := router.Plan(request, account)
				require.NoError(t, err)
				ctx := WithExecutionAccountState(context.Background(), ExecutionAccountState{AccountID: account.accountID, CapabilityKey: account.capabilityKey, CredentialPresent: true})
				_, err = router.Execute(ctx, plan, request)
				require.NoError(t, err)
				effective := adapter.execution.Request()
				require.Equal(t, []byte(tc.body), request.Body())
				require.Equal(t, digest, request.Digest())
				require.Equal(t, digest, plan.RequestDigest())
				if web {
					require.Equal(t, GeminiWebBestEffortTokenLimit, plan.Adjustment())
					require.Equal(t, 1, plan.CompatibilityRank())
					require.False(t, gjson.GetBytes(effective.Body(), tc.field).Exists())
					require.NotEqual(t, digest, plan.EffectiveRequestDigest())
				} else {
					require.Empty(t, plan.Adjustment())
					require.Equal(t, 0, plan.CompatibilityRank())
					require.Equal(t, int64(128), gjson.GetBytes(effective.Body(), tc.field).Int())
					require.Equal(t, digest, plan.EffectiveRequestDigest())
				}
				for _, preserved := range []string{"messages", "input", "contents", "generationConfig.imageConfig", "generationConfig.responseModalities"} {
					require.Equal(t, gjson.GetBytes(request.Body(), preserved).Raw, gjson.GetBytes(effective.Body(), preserved).Raw)
				}
			}
		})
	}
}

func TestGeminiWebBestEffortDoesNotAdmitInvalidBudgetsOrOtherControls(t *testing.T) {
	account := testAccount(t, ProtocolGeminiGenerateContent)
	account.providerCapability = ProviderCapabilityGeminiWeb
	router := New(allTestAdapters())
	for _, budget := range []string{"0", "-1", "null", `"128"`, "1.5", "true", "9223372036854775808"} {
		body := []byte(`{"max_tokens":` + budget + `,"messages":[{"role":"user","content":"hello"}]}`)
		request, err := ParseCanonicalRequest(ProtocolMessages, "", "model", false, body)
		require.NoError(t, err)
		_, err = router.Plan(request, account)
		require.ErrorIs(t, err, ErrNoLegalRoute)
	}
	for _, extra := range []string{`"temperature":0.5`, `"system":"instruction"`, `"tools":[]`, `"thinking":{"type":"enabled","budget_tokens":128}`} {
		body := []byte(`{"max_tokens":128,"messages":[{"role":"user","content":"hello"}],` + extra + `}`)
		request, err := ParseCanonicalRequest(ProtocolMessages, "", "model", false, body)
		require.NoError(t, err)
		_, err = router.Plan(request, account)
		require.ErrorIs(t, err, ErrNoLegalRoute)
	}
}
