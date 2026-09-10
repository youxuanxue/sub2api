package protocolrouter

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicpolicy"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestThinkingToolPlanExecution(t *testing.T) {
	for _, choice := range []string{`"required"`, `{"type":"function","function":{"name":"lookup"}}`, `"none"`} {
		t.Run(choice, func(t *testing.T) {
			body := []byte(`{"model":"alias","thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hi","cache_control":{"type":"ephemeral","ttl":"1h"}}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":` + choice + `}`)
			request, err := ParseCanonicalRequest(ProtocolChatCompletions, ResponsesPathNone, "alias", false, body)
			require.NoError(t, err)
			account := testAccount(t, ProtocolMessages)
			adapter := &recordingAdapter{}
			router := New(AdapterCatalog{AdapterChatToMessages: adapter})
			plan, err := router.Plan(request, account)
			require.NoError(t, err)
			ctx := WithExecutionAccountState(context.Background(), ExecutionAccountState{AccountID: account.accountID, CapabilityKey: account.capabilityKey, CredentialPresent: true})
			_, err = router.Execute(ctx, plan, request)
			require.NoError(t, err)
			effective := adapter.execution.Request().Body()
			require.Equal(t, body, request.Body(), "retry input remains immutable")
			for _, field := range []string{"thinking", "messages", "tools"} {
				require.Equal(t, gjson.GetBytes(body, field).Raw, gjson.GetBytes(effective, field).Raw)
			}
			if choice == `"none"` {
				require.Equal(t, 0, plan.CompatibilityRank())
				require.Equal(t, "none", gjson.GetBytes(effective, "tool_choice").String())
			} else {
				require.Equal(t, 1, plan.CompatibilityRank())
				require.Equal(t, "auto", gjson.GetBytes(effective, "tool_choice").String())
			}
		})
	}
}

func TestThinkingCapabilitiesUseResolvedTargetAndEndpointEvidence(t *testing.T) {
	body := []byte(`{"model":"alias","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":"required"}`)
	request, err := ParseCanonicalRequest(ProtocolChatCompletions, ResponsesPathNone, "alias", false, body)
	require.NoError(t, err)
	account := testAccount(t, ProtocolMessages)
	account.resolvedModel = "claude-fable-5-1"
	router := New(allTestAdapters())
	plan, err := router.Plan(request, account)
	require.NoError(t, err)
	require.Equal(t, 1, plan.CompatibilityRank(), "implicit always-on thinking also conflicts")
	account.modelCapabilities = map[Protocol]anthropicpolicy.Capabilities{ProtocolMessages: {AlwaysThinking: true, ForcedToolsWithThinking: true}}
	plan, err = router.Plan(request, account)
	require.NoError(t, err)
	require.Equal(t, 0, plan.CompatibilityRank(), "endpoint evidence can support the full request")
	account = testAccount(t, ProtocolChatCompletions)
	account.resolvedModel = "claude-fable-5-1"
	plan, err = router.Plan(request, account)
	require.NoError(t, err)
	require.Equal(t, 0, plan.CompatibilityRank(), "Messages restrictions do not imply Chat restrictions")
}

func TestPlanPrefersExactConversionOverAdjustedIdentity(t *testing.T) {
	request, err := ParseCanonicalRequest(ProtocolMessages, ResponsesPathNone, "alias", false, []byte(`{"model":"alias","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"any"}}`))
	require.NoError(t, err)
	account := testAccount(t, ProtocolMessages, ProtocolResponses)
	account.resolvedModel = "claude-fable-5"
	plan, err := New(allTestAdapters()).Plan(request, account)
	require.NoError(t, err)
	require.Equal(t, ProtocolResponses, plan.TargetProtocol())
	require.Empty(t, plan.Adjustment())
	plan, err = New(allTestAdapters()).PlanNative(request, account)
	require.NoError(t, err)
	require.Equal(t, ProtocolMessages, plan.TargetProtocol())
	require.Equal(t, 1, plan.CompatibilityRank())
	_, err = New(allTestAdapters()).PlanNative(request, testAccount(t, ProtocolResponses))
	require.ErrorIs(t, err, ErrNoLegalRoute, "conversion-only accounts cannot bypass native-only permission")
}

func TestPlanDoesNotLoseThinkingThroughResponses(t *testing.T) {
	body := []byte(`{"model":"alias","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"},"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":"required"}`)
	request, err := ParseCanonicalRequest(ProtocolChatCompletions, ResponsesPathNone, "alias", false, body)
	require.NoError(t, err)
	account := testAccount(t, ProtocolResponses, ProtocolMessages)
	plan, err := New(allTestAdapters()).Plan(request, account)
	require.NoError(t, err)
	require.Equal(t, ProtocolMessages, plan.TargetProtocol())
	require.Equal(t, 1, plan.CompatibilityRank())
}

func TestNativeReasoningCapabilities(t *testing.T) {
	for _, protocol := range []Protocol{ProtocolChatCompletions, ProtocolResponses} {
		for _, effort := range []string{"high", "none"} {
			t.Run(string(protocol)+"/"+effort, func(t *testing.T) {
				body := []byte(`{"model":"alias","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"required"}`)
				field := "reasoning_effort"
				if protocol == ProtocolResponses {
					body = []byte(`{"model":"alias","input":"hi","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":"required"}`)
					field = "reasoning.effort"
				}
				body, err := sjson.SetBytes(body, field, effort)
				require.NoError(t, err)
				request, err := ParseCanonicalRequest(protocol, ResponsesPathNone, "alias", false, body)
				require.NoError(t, err)
				account := testAccount(t, protocol)
				account.modelCapabilities = map[Protocol]anthropicpolicy.Capabilities{protocol: {}}
				adapter := &recordingAdapter{}
				router := New(AdapterCatalog{AdapterChatIdentity: adapter, AdapterResponsesIdentity: adapter})
				plan, err := router.Plan(request, account)
				require.NoError(t, err)
				ctx := WithExecutionAccountState(context.Background(), ExecutionAccountState{AccountID: account.accountID, CapabilityKey: account.capabilityKey, CredentialPresent: true})
				_, err = router.Execute(ctx, plan, request)
				require.NoError(t, err)
				effective := adapter.execution.Request().Body()
				require.Equal(t, effort, gjson.GetBytes(effective, field).String())
				require.Equal(t, gjson.GetBytes(body, "tools").Raw, gjson.GetBytes(effective, "tools").Raw)
				require.Equal(t, body, request.Body())
				if effort == "none" {
					require.Equal(t, 0, plan.CompatibilityRank())
					require.Equal(t, "required", gjson.GetBytes(effective, "tool_choice").String())
				} else {
					require.Equal(t, 1, plan.CompatibilityRank())
					require.Equal(t, "auto", gjson.GetBytes(effective, "tool_choice").String())
				}
			})
		}
	}
}
