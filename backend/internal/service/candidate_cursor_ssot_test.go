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
