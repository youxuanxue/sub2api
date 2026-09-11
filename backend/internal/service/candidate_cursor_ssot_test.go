//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func cursorCandidateAccount(model string) *Account {
	a := cursorTestAccount()
	a.Status, a.Schedulable, a.GroupIDs = StatusActive, true, []int64{1}
	a.Credentials["model_mapping"] = map[string]any{model: model}
	a.Credentials[CursorModelParametersKey] = map[string]any{model: nil}
	a.Credentials[CursorWireModelsKey] = map[string]any{model: model}
	attachTestProtocolCapability(a, protocolrouter.ProtocolMessages)
	return a
}

func TestCursorCandidateCatalogAdmissionMatchesTransport(t *testing.T) {
	for _, model := range []string{"composer-2.5", "claude-sonnet-4-6", "gpt-5.4", "gemini-3.1-pro", "grok-4.6"} {
		for _, protocol := range []protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses} {
			t.Run(model+"/"+string(protocol), func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"input":"hi"}`, model))
				path := protocolrouter.ResponsesPathNone
				if protocol == protocolrouter.ProtocolResponses {
					path = protocolrouter.ResponsesPathRoot
				}
				request, err := protocolrouter.ParseCanonicalRequest(protocol, path, model, false, body)
				require.NoError(t, err)
				a := cursorCandidateAccount(model)
				ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), request)
				plan, governed, err := protocolPlanForAccount(ctx, a, model)
				require.NoError(t, err)
				require.True(t, governed)
				require.Equal(t, protocolrouter.ProtocolMessages, plan.TargetProtocol())
				require.Equal(t, model, plan.ResolvedModel())
				delete(a.Credentials, CursorModelParametersKey)
				ctx = WithProtocolRouting(context.Background(), NewProtocolRouter(), request)
				_, _, err = protocolPlanForAccount(ctx, a, model)
				require.ErrorIs(t, err, protocolrouter.ErrNoLegalRoute, "an account the transport cannot serve must not win Plan")
			})
		}
	}
}

func TestCursorCandidateRuntimeAndCapabilityGates(t *testing.T) {
	const model = "gpt-5.4"
	for _, test := range []struct {
		name   string
		mutate func(*Account)
		want   error
	}{
		{"healthy", func(*Account) {}, nil},
		{"expired", func(a *Account) { past := time.Now().Add(-time.Hour); a.ExpiresAt = &past; a.AutoPauseOnExpired = true }, ErrUniversalCapacityUnavailable},
		{"expired_auto_pause_off", func(a *Account) {
			past := time.Now().Add(-time.Hour)
			a.ExpiresAt = &past
			a.AutoPauseOnExpired = false
		}, ErrUniversalCapacityUnavailable},
		{"disabled", func(a *Account) { a.Schedulable = false }, ErrUniversalCapacityUnavailable},
		{"cooldown", func(a *Account) { until := time.Now().Add(time.Hour); a.RateLimitResetAt = &until }, ErrUniversalCapacityUnavailable},
		{"no_credential", func(a *Account) { delete(a.Credentials, "api_key") }, ErrUniversalCapacityUnavailable},
		{"no_protocol", func(a *Account) { attachTestProtocolCapability(a) }, ErrProtocolCapabilityUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := cursorCandidateAccount(model)
			test.mutate(a)
			resolver := NewUniversalRoutingResolver(&stubSpanLister{groups: []Group{grp(1, PlatformNewAPI, 1, false)}})
			wireCandidateTestResolver(resolver, &GatewayService{}, []Account{*a})
			group, err := resolveCandidateTest(resolver, protocolRoutingTestRequest(t, protocolrouter.ProtocolChatCompletions), "")
			if test.want != nil {
				require.Nil(t, group)
				require.ErrorIs(t, err, test.want)
			} else {
				require.NoError(t, err)
				require.Equal(t, int64(1), group.ID)
			}
		})
	}
}

func TestCursorForcedToolsCannotBecomeCompatibilityFallback(t *testing.T) {
	const model = "claude-fable-5-1"
	for _, protocol := range []protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses} {
		for _, mode := range []string{"auto", "none", "required", "named"} {
			t.Run(fmt.Sprintf("%s/%s", protocol, mode), func(t *testing.T) {
				forced := mode == "required" || mode == "named"
				choice := fmt.Sprintf("%q", mode)
				if protocol == protocolrouter.ProtocolMessages {
					choice = fmt.Sprintf(`{"type":%q}`, mode)
					if mode == "required" {
						choice = `{"type":"any"}`
					} else if mode == "named" {
						choice = `{"type":"tool","name":"lookup"}`
					}
				} else if mode == "named" {
					choice = `{"type":"function","name":"lookup"}`
					if protocol == protocolrouter.ProtocolChatCompletions {
						choice = `{"type":"function","function":{"name":"lookup"}}`
					}
				}
				tools := `[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]`
				if protocol == protocolrouter.ProtocolMessages {
					tools = `[{"name":"lookup","input_schema":{"type":"object"}}]`
				} else if protocol == protocolrouter.ProtocolResponses {
					tools = `[{"type":"function","name":"lookup","parameters":{"type":"object"}}]`
				}
				body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"input":"hi","tool_choice":%s,"tools":%s}`, model, choice, tools))
				path := protocolrouter.ResponsesPathNone
				if protocol == protocolrouter.ProtocolResponses {
					path = protocolrouter.ResponsesPathRoot
				}
				request, err := protocolrouter.ParseCanonicalRequest(protocol, path, model, false, body)
				require.NoError(t, err)
				ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), request)
				_, _, err = protocolPlanForAccount(ctx, cursorCandidateAccount(model), model)
				if forced {
					require.ErrorIs(t, err, protocolrouter.ErrNoLegalRoute)
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestCursorPlanRejectsUnsupportedNativeContent(t *testing.T) {
	const model = "claude-sonnet-4-6"
	for _, tc := range []struct {
		name     string
		protocol protocolrouter.Protocol
		payload  string
	}{
		{"messages_image", protocolrouter.ProtocolMessages, `"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}]}]`},
		{"messages_thinking", protocolrouter.ProtocolMessages, `"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior thought","signature":"s"}]},{"role":"user","content":"continue"}]`},
		{"messages_redacted_thinking", protocolrouter.ProtocolMessages, `"messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"s"}]},{"role":"user","content":"continue"}]`},
		{"messages_tool_result_image", protocolrouter.ProtocolMessages, `"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_a","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}]}]}]`},
		{"chat_image", protocolrouter.ProtocolChatCompletions, `"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGk="}}]}]`},
		{"responses_image", protocolrouter.ProtocolResponses, `"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,aGk="}]}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,%s}`, model, tc.payload))
			path := protocolrouter.ResponsesPathNone
			if tc.protocol == protocolrouter.ProtocolResponses {
				path = protocolrouter.ResponsesPathRoot
			}
			request, err := protocolrouter.ParseCanonicalRequest(tc.protocol, path, model, false, body)
			require.NoError(t, err)
			account := cursorCandidateAccount(model)
			ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), request)
			_, governed, err := protocolPlanForAccount(ctx, account, model)
			require.True(t, governed)
			require.ErrorIs(t, err, protocolrouter.ErrNoLegalRoute)
			require.Equal(t, body, request.Body(), "planning must preserve caller content")
			require.Equal(t, []protocolrouter.Protocol{protocolrouter.ProtocolMessages}, account.ProtocolEndpointCapability.SupportedProtocols)
		})
	}
}

func TestCursorNativeContentFailureDoesNotWinCandidateSelection(t *testing.T) {
	const model = "claude-sonnet-4-6"
	body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior thought","signature":"s"}]},{"role":"user","content":"continue"}]}`, model))
	cursorAccount := cursorCandidateAccount(model)
	peer := protocolRoutingOpenAIAccount(43, "messages")
	peer.Platform = PlatformAnthropic
	peer.GroupIDs = []int64{2}
	peer.Credentials["model_mapping"] = map[string]any{model: model}
	attachTestProtocolCapability(peer, protocolrouter.ProtocolMessages)
	groups := []Group{grp(1, PlatformNewAPI, 0, false), grp(2, PlatformAnthropic, 1, false)}
	resolver := NewUniversalRoutingResolver(&stubSpanLister{groups: groups})
	wireCandidateTestResolver(resolver, &GatewayService{}, []Account{*cursorAccount, *peer})
	ctx := resolver.WithRequest(context.Background(), ShapeAnthropicMessages, "/v1/messages", model, body)
	group, err := resolver.Resolve(ctx, universalKey(1), ShapeAnthropicMessages, model, "")
	require.NoError(t, err)
	require.Equal(t, int64(2), group.ID, "the legal peer must win before a Cursor transport attempt")
	resolver = NewUniversalRoutingResolver(&stubSpanLister{groups: groups[:1]})
	wireCandidateTestResolver(resolver, &GatewayService{}, []Account{*cursorAccount})
	ctx = resolver.WithRequest(context.Background(), ShapeAnthropicMessages, "/v1/messages", model, body)
	_, err = resolver.Resolve(ctx, universalKey(1), ShapeAnthropicMessages, model, "")
	require.Error(t, err, "a Cursor-only pool must fail at planning")
}

func TestCursorPlanPreservesEmulatedWebSearchHistoryCompatibility(t *testing.T) {
	const model = "claude-sonnet-4-6"
	for _, tc := range []struct {
		name, id string
		legal    bool
	}{
		{"emulated", "srvtoolu_ws_demo", true},
		{"genuine", "srvtoolu_demo", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"assistant","content":[{"type":"server_tool_use","id":%q,"name":"web_search","input":{"query":"weather"}},{"type":"web_search_tool_result","tool_use_id":%q,"content":[]},{"type":"text","text":"The forecast is sunny."}]},{"role":"user","content":"Summarize the forecast."}]}`, model, tc.id, tc.id))
			request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolMessages, protocolrouter.ResponsesPathNone, model, false, body)
			require.NoError(t, err)
			ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), request)
			_, governed, err := protocolPlanForAccount(ctx, cursorCandidateAccount(model), model)
			require.True(t, governed)
			if tc.legal {
				require.NoError(t, err, "execution strips emulated search blocks and retains their text summary")
			} else {
				require.ErrorIs(t, err, protocolrouter.ErrNoLegalRoute, "genuine search blocks remain unsupported for the resolved Claude model")
			}
			require.Equal(t, body, request.Body(), "planning must not mutate retry input")
		})
	}
}

func TestCursorCandidateUsesSharedUniversalOrdering(t *testing.T) {
	for _, cursorFirst := range []bool{false, true} {
		for _, unavailable := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("cursor_first=%t/unavailable=%d", cursorFirst, unavailable), func(t *testing.T) {
				cursor := cursorCandidateAccount("gpt-5.4")
				native := protocolRoutingOpenAIAccount(43, "chat_completions")
				native.GroupIDs = []int64{2}
				groups := []Group{grp(1, PlatformNewAPI, 2, false), grp(2, PlatformOpenAI, 1, false)}
				want := int64(2)
				if cursorFirst {
					groups[0].SortOrder = 0
					want = 1
				}
				if unavailable == 1 {
					cursor.Schedulable = false
					want = 2
				}
				if unavailable == 2 {
					native.Schedulable = false
					want = 1
				}
				resolver := NewUniversalRoutingResolver(&stubSpanLister{groups: groups})
				wireCandidateTestResolver(resolver, &GatewayService{}, []Account{*cursor, *native})
				group, err := resolveCandidateTest(resolver, protocolRoutingTestRequest(t, protocolrouter.ProtocolChatCompletions), "")
				require.NoError(t, err)
				require.Equal(t, want, group.ID, "native and converted Cursor routes use the same ordering and availability gates")
			})
		}
	}
}

func TestCursorToolHistoryUsesOrdinaryCandidateRouting(t *testing.T) {
	for _, test := range []struct {
		name         string
		protocol     protocolrouter.Protocol
		body         string
		continuation bool
	}{
		{"messages_result", protocolrouter.ProtocolMessages, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_bf_local_test","content":"ok"}]}]}`, true},
		{"chat_result", protocolrouter.ProtocolChatCompletions, `{"messages":[{"role":"tool","tool_call_id":"toolu_bf_local_test","content":"ok"}]}`, true},
		{"responses_result", protocolrouter.ProtocolResponses, `{"input":[{"type":"function_call_output","call_id":"toolu_bf_local_test","output":"ok"}]}`, true},
		{"tool_schema_property", protocolrouter.ProtocolChatCompletions, `{"messages":[{"role":"user","content":"hi"}],"metadata":{"call_id":"toolu_bf_local_test"}}`, false},
		{"messages_history", protocolrouter.ProtocolMessages, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_bf_local_test","content":"ok"}]},{"role":"assistant","content":"done"},{"role":"user","content":"next"}]}`, false},
		{"chat_history", protocolrouter.ProtocolChatCompletions, `{"messages":[{"role":"tool","tool_call_id":"toolu_bf_local_test","content":"ok"},{"role":"assistant","content":"done"},{"role":"user","content":"next"}]}`, false},
		{"responses_history", protocolrouter.ProtocolResponses, `{"input":[{"type":"function_call_output","call_id":"toolu_bf_local_test","output":"ok"},{"role":"user","content":"next"}]}`, false},
		{"chat_new_tool_round", protocolrouter.ProtocolChatCompletions, `{"messages":[{"role":"tool","tool_call_id":"toolu_bf_local_test","content":"ok"},{"role":"assistant","tool_calls":[{"id":"call_other","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_other","content":"next"}]}`, false},
		{"responses_new_tool_round", protocolrouter.ProtocolResponses, `{"input":[{"type":"function_call_output","call_id":"toolu_bf_local_test","output":"ok"},{"type":"function_call","call_id":"call_other","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_other","output":"next"}]}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := protocolrouter.ResponsesPathNone
			if test.protocol == protocolrouter.ProtocolResponses {
				path = protocolrouter.ResponsesPathRoot
			}
			request, err := protocolrouter.ParseCanonicalRequest(test.protocol, path, "gpt-5.4", false, []byte(test.body))
			require.NoError(t, err)
			ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), request)
			a := protocolRoutingOpenAIAccount(10, "chat_completions", "responses")
			_, _, err = protocolPlanForAccount(ctx, a, "gpt-5.4")
			require.NoError(t, err, "complete caller history does not pin a request to a parked Cursor run")
		})
	}
}
