//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCursorExpiryCannotBeDisabledAndRenewalPreservesPause(t *testing.T) {
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	for _, cursor := range []bool{false, true} {
		account := cursorCandidateAccount("gpt-5.4")
		if !cursor {
			delete(account.Extra, CursorSourceExtraKey)
		}
		account.AutoPauseOnExpired, account.ExpiresAt = false, &past
		require.Equal(t, !cursor, account.IsSchedulable())
		require.Equal(t, !cursor, account.IsCredentialUsableForShadow())
		account.ExpiresAt = &future
		require.True(t, account.IsSchedulable())
		account.Schedulable = false
		require.False(t, account.IsSchedulable(), "renewal cannot bypass manual pause")
	}
}

func TestNativeMessagesCacheUsageReachesBillingOnce(t *testing.T) {
	for _, cursor := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("cursor=%t/stream=%t", cursor, stream), func(t *testing.T) {
				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				billingRepo := &openAIRecordUsageBillingRepoStub{}
				svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
				body := `{"type":"message","usage":{"input_tokens":100,"cache_read_input_tokens":900,"cache_creation_input_tokens":20,"output_tokens":4}}`
				if stream {
					body = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":900,\"cache_creation_input_tokens\":20,\"output_tokens\":0}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
				var result *OpenAIForwardResult
				var err error
				const model = "claude-sonnet-4-6"
				if stream {
					result, err = svc.streamNativeAnthropicMessages(c, resp, model, model, model, time.Now())
				} else {
					result, err = svc.bufferNativeAnthropicMessages(c, resp, model, model, model, time.Now())
				}
				require.NoError(t, err)
				result.RequestID = "cache-billing-regression"
				account := cursorTestAccount()
				if !cursor {
					delete(account.Extra, CursorSourceExtraKey)
				}
				err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: account})
				require.NoError(t, err)
				require.Equal(t, 1, billingRepo.calls)
				require.Equal(t, 100, usageRepo.lastLog.InputTokens)
				require.Equal(t, 900, usageRepo.lastLog.CacheReadTokens)
				require.Equal(t, 20, usageRepo.lastLog.CacheCreationTokens)
				expected, err := svc.billingService.CalculateCost(model, UsageTokens{InputTokens: 100, OutputTokens: 4, CacheReadTokens: 900, CacheCreationTokens: 20}, 1.1)
				require.NoError(t, err)
				require.Positive(t, expected.ActualCost)
				require.InDelta(t, expected.ActualCost, billingRepo.lastCmd.BalanceCost, 1e-12)
				require.Equal(t, cursor, usageRepo.lastLog.BillingTier != nil)
			})
		}
	}
}

func TestMessagesToolConversionsKeepExistingSupplyAndWire(t *testing.T) {
	// An unconfigured optional bridge must not affect existing Messages supplies.
	t.Setenv("CURSOR_BRIDGE_URL", "")
	t.Setenv("CURSOR_BRIDGE_SECRET", "")
	for _, inbound := range []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses} {
		t.Run(string(inbound), func(t *testing.T) {
			body := []byte(`{"model":"client-model","messages":[{"role":"user","content":"Use lookup"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}}],"tool_choice":"required"}`)
			path := protocolrouter.ResponsesPathNone
			if inbound == protocolrouter.ProtocolResponses {
				body = []byte(`{"model":"client-model","input":"Use lookup","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}],"tool_choice":"required"}`)
				path = protocolrouter.ResponsesPathRoot
			}
			account := protocolTargetTestAccount(protocolrouter.ProtocolMessages)
			account.Credentials["model_mapping"] = map[string]any{"client-model": "claude-sonnet-4-6"}
			attachTestProtocolCapability(account, protocolrouter.ProtocolMessages)
			request, err := protocolrouter.ParseCanonicalRequest(inbound, path, "client-model", false, body)
			require.NoError(t, err)
			router := NewProtocolRouter()
			ctx := WithProtocolRouting(context.Background(), router, request)
			plan, governed, err := protocolPlanForAccount(ctx, account, "client-model")
			require.NoError(t, err)
			require.True(t, governed)
			require.Equal(t, protocolrouter.ProtocolMessages, plan.TargetProtocol())
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, protocolRouteContractInboundPath(inbound), bytes.NewReader(body))
			frames := []string{
				`data: {"type":"message_start","message":{"id":"msg-tool","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_existing","name":"lookup","input":{}}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"test\"}"}}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`,
				`data: {"type":"message_stop"}`,
			}
			var streamBody strings.Builder
			for _, frame := range frames {
				payload := strings.TrimPrefix(frame, "data: ")
				fmt.Fprintf(&streamBody, "event: %s\n%s\n\n", gjson.Get(payload, "type").String(), frame)
			}
			toolStream := streamBody.String()
			upstream := &protocolTargetHTTPUpstream{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(toolStream))}}}
			svc := protocolTargetTestService(upstream)
			_, err = ExecuteSelectedProtocol(ctx, router, &AccountSelectionResult{Account: account, ProtocolPlan: &plan}, account,
				func(context.Context, *Account, string) error { return nil }, protocolExecutionAccountLoaderForTest(account),
				protocolExecutorsForTest(plan, func(ctx context.Context, a *Account, _ protocolrouter.Plan, r protocolrouter.CanonicalRequest) (any, error) {
					if inbound == protocolrouter.ProtocolChatCompletions {
						return svc.ForwardAsChatCompletions(ctx, c, a, r.Body(), "", "")
					}
					return svc.Forward(ctx, c, a, r.Body())
				}))
			require.NoError(t, err)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, "http://upstream.example/v1/messages", upstream.requests[0].URL.String())
			wire := readProtocolTargetRequestBody(t, upstream.requests[0])
			require.Equal(t, "claude-sonnet-4-6", gjson.GetBytes(wire, "model").String())
			require.Equal(t, "lookup", gjson.GetBytes(wire, "tools.0.name").String())
			require.Equal(t, "any", gjson.GetBytes(wire, "tool_choice.type").String())
			require.Empty(t, upstream.requests[0].Header.Get("X-TokenKey-Bridge-Secret"))
			argumentPath := "choices.0.message.tool_calls.0.function.arguments"
			if inbound == protocolrouter.ProtocolResponses {
				argumentPath = `output.#(type=="function_call").arguments`
			}
			arguments := gjson.GetBytes(recorder.Body.Bytes(), argumentPath).String()
			require.JSONEq(t, `{"query":"test"}`, arguments)
		})
	}
}
