//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCursorRelayConversionsRetainBillingProvenance(t *testing.T) {
	for _, protocol := range []string{"chat", "responses"} {
		for _, stream := range []bool{false, true} {
			for _, tier := range []string{"cursor-oauth-reported", "cursor-oauth-estimated", "untrusted-tier"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", protocol, stream, tier), func(t *testing.T) {
					usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
					billingRepo := &openAIRecordUsageBillingRepoStub{}
					svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
					frames := []string{
						`{"type":"message_start","message":{"id":"msg-relay","type":"message","role":"assistant","model":"composer-2.5","content":[],"usage":{"input_tokens":10,"output_tokens":0,"cache_read_input_tokens":3}}}`,
						`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
						`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"RELAY_OK"}}`,
						`{"type":"content_block_stop","index":0}`,
						fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2,"tk_billing_tier":%q}}`, tier),
						`{"type":"message_stop"}`,
					}
					var body strings.Builder
					for _, frame := range frames {
						fmt.Fprintf(&body, "event: message\ndata: %s\n\n", frame)
					}
					// An Edge response has no in-process MessagesBody. Provenance must
					// survive the wire parser and conversion into durable settlement.
					resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body.String()))}
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, nil)
					account := cursorCandidateAccount("composer-2.5")
					var result *OpenAIForwardResult
					var err error
					const model = "composer-2.5"
					if protocol == "chat" && stream {
						result, err = svc.handleCCStreamingFromNativeAnthropic(resp, c, account, model, model, model, nil, time.Now(), true)
					} else if protocol == "chat" {
						result, err = svc.handleCCBufferedFromNativeAnthropic(resp, c, account, model, model, model, nil, time.Now())
					} else if stream {
						result, err = svc.handleResponsesStreamingFromNativeAnthropic(resp, c, account, model, model, model, nil, time.Now(), apicompat.ResponsesClientToolMapping{})
					} else {
						result, err = svc.handleResponsesBufferedFromNativeAnthropic(resp, c, account, model, model, model, nil, time.Now(), apicompat.ResponsesClientToolMapping{})
					}
					require.NoError(t, err)
					require.Equal(t, tier, result.BillingTier)
					require.Contains(t, recorder.Body.String(), "RELAY_OK")
					result.RequestID = "relay-provenance"
					require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: account}))
					require.Equal(t, 1, billingRepo.calls)
					require.Equal(t, 10, usageRepo.lastLog.InputTokens)
					require.Equal(t, 3, usageRepo.lastLog.CacheReadTokens)
					require.Equal(t, 2, usageRepo.lastLog.OutputTokens)
					if tier == "untrusted-tier" {
						require.Nil(t, usageRepo.lastLog.BillingTier)
					} else {
						require.Equal(t, tier, *usageRepo.lastLog.BillingTier)
					}
				})
			}
		}
	}
}

func TestCursorRelayFailuresNeverCompleteOrSettle(t *testing.T) {
	for _, protocol := range []string{"chat", "responses"} {
		for _, stream := range []bool{false, true} {
			for _, terminal := range []string{"error", "eof", "cyber_policy", "usage_policy"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", protocol, stream, terminal), func(t *testing.T) {
					frames := []string{
						`{"type":"message_start","message":{"id":"msg-relay","type":"message","role":"assistant","content":[],"usage":{"input_tokens":0,"output_tokens":0}}}`,
						`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
						`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
					}
					if terminal == "error" {
						frames = append(frames, `{"type":"error","error":{"type":"rate_limit_error","message":"upstream unavailable"}}`)
					}
					if terminal == "cyber_policy" || terminal == "usage_policy" {
						frames = append(frames, fmt.Sprintf(`{"type":"error","error":{"type":"invalid_request_error","code":%q,"message":"blocked by upstream usage policy"}}`, terminal))
					}
					var wire strings.Builder
					for _, frame := range frames {
						fmt.Fprintf(&wire, "event: message\ndata: %s\n\n", frame)
					}
					resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire.String()))}
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, nil)
					account := cursorCandidateAccount("composer-2.5")
					svc := protocolTargetTestService(nil)
					var result *OpenAIForwardResult
					var err error
					switch {
					case protocol == "chat" && stream:
						result, err = svc.handleCCStreamingFromNativeAnthropic(resp, c, account, "composer-2.5", "composer-2.5", "composer-2.5", nil, time.Now(), true)
					case protocol == "chat":
						result, err = svc.handleCCBufferedFromNativeAnthropic(resp, c, account, "composer-2.5", "composer-2.5", "composer-2.5", nil, time.Now())
					case stream:
						result, err = svc.handleResponsesStreamingFromNativeAnthropic(resp, c, account, "composer-2.5", "composer-2.5", "composer-2.5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
					default:
						result, err = svc.handleResponsesBufferedFromNativeAnthropic(resp, c, account, "composer-2.5", "composer-2.5", "composer-2.5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
					}
					require.Error(t, err)
					require.Nil(t, result, "no native settlement evidence")
					if terminal == "cyber_policy" || terminal == "usage_policy" {
						require.ErrorIs(t, err, errOpenAICyberPolicyForwarded)
						require.True(t, IsResponseCommitted(c))
						if terminal == "cyber_policy" {
							require.NotNil(t, GetOpsCyberPolicy(c))
							require.Nil(t, GetOpsUsagePolicy(c))
						} else {
							require.NotNil(t, GetOpsUsagePolicy(c))
						}
					} else if !stream {
						require.False(t, c.Writer.Written(), "buffered failure must leave error response ownership with the handler")
					}
				})
			}
		}
	}
}

func TestCursorChatDisconnectStillSettlesTerminalUsage(t *testing.T) {
	svc := newNativeAnthropicHangTestService(5)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Writer = &failingGinWriter{ResponseWriter: c.Writer, failAfter: 0}
	wire := strings.Replace(miniAnthropicSSEStream(), `"output_tokens":5`, `"output_tokens":5,"tk_billing_tier":"cursor-oauth-reported"`, 1)
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
	result, err := svc.handleCCStreamingFromNativeAnthropic(resp, c, cursorCandidateAccount("composer-2.5"), "composer-2.5", "composer-2.5", "composer-2.5", nil, time.Now(), true)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 5, result.Usage.OutputTokens)
}

func TestNativeMessagesPolicyRelayTerminatesWithoutSettlement(t *testing.T) {
	for _, path := range []string{"native", "passthrough"} {
		for _, stream := range []bool{false, true} {
			for _, kind := range []string{"cyber_policy", "usage_policy"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", path, stream, kind), func(t *testing.T) {
					payload := fmt.Sprintf(`{"type":"error","error":{"type":"invalid_request_error","code":%q,"message":"blocked by upstream usage policy"}}`, kind)
					wire := payload
					if stream {
						wire = "event: error\ndata: " + payload + "\n\n"
					}
					resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
					svc := protocolTargetTestService(nil)
					account := cursorCandidateAccount("composer-2.5")
					var result *OpenAIForwardResult
					var err error
					switch {
					case path == "native" && stream:
						result, err = svc.streamNativeAnthropicMessages(c, resp, account, "composer-2.5", "composer-2.5", "composer-2.5", time.Now())
					case path == "native":
						result, err = svc.bufferNativeAnthropicMessages(c, resp, "composer-2.5", "composer-2.5", "composer-2.5", time.Now())
					case stream:
						result, err = svc.handleNativeAnthropicStreamingResponse(t.Context(), resp, c, account, "composer-2.5", "composer-2.5", "composer-2.5", nil, time.Now())
					default:
						result, err = svc.handleNativeAnthropicBufferedResponse(t.Context(), resp, c, account, "composer-2.5", "composer-2.5", "composer-2.5", nil, time.Now())
					}
					require.ErrorIs(t, err, errOpenAICyberPolicyForwarded)
					require.Nil(t, result)
					require.True(t, IsResponseCommitted(c))
					require.Equal(t, 1, strings.Count(recorder.Body.String(), `"error":`))
					require.Contains(t, recorder.Body.String(), `"code":"`+kind+`"`)
					if kind == "cyber_policy" {
						require.NotNil(t, GetOpsCyberPolicy(c))
						require.Nil(t, GetOpsUsagePolicy(c))
					} else {
						require.NotNil(t, GetOpsUsagePolicy(c))
					}
				})
			}
		}
	}
}

func TestCursorMessagesRelayFailureDoesNotSettle(t *testing.T) {
	for _, path := range []string{"native", "passthrough"} {
		for _, terminal := range []string{"error", "eof", "complete"} {
			for _, isCursor := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/cursor=%t", path, terminal, isCursor), func(t *testing.T) {
					wire := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-relay\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
						"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n"
					switch terminal {
					case "error":
						wire += "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"upstream unavailable\"}}\n\n"
					case "complete":
						wire += "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2,\"tk_billing_tier\":\"cursor-oauth-reported\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
					}
					resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
					svc := protocolTargetTestService(nil)
					account := cursorCandidateAccount("composer-2.5")
					if !isCursor {
						delete(account.Extra, CursorSourceExtraKey)
					}
					var result *OpenAIForwardResult
					var err error
					if path == "native" {
						result, err = svc.streamNativeAnthropicMessages(c, resp, account, "composer-2.5", "composer-2.5", "composer-2.5", time.Now())
					} else {
						result, err = svc.handleNativeAnthropicStreamingResponse(t.Context(), resp, c, account, "composer-2.5", "composer-2.5", "composer-2.5", nil, time.Now())
					}
					if terminal == "complete" {
						require.NoError(t, err)
						require.NotNil(t, result)
						require.Equal(t, 10, result.Usage.InputTokens)
						require.Equal(t, 2, result.Usage.OutputTokens)
						require.Equal(t, "cursor-oauth-reported", result.BillingTier)
						return
					}
					if !isCursor {
						require.NotNil(t, result, "ordinary supplies retain their partial-usage settlement contract")
						require.Equal(t, 10, result.Usage.InputTokens)
						return
					}
					require.Error(t, err)
					require.Nil(t, result, "a failed Cursor relay must not submit partial usage for settlement")
					require.NotContains(t, recorder.Body.String(), "message_stop")
					if terminal == "error" {
						require.True(t, IsResponseCommitted(c))
						require.Equal(t, 1, strings.Count(recorder.Body.String(), `"error":`))
					}
				})
			}
		}
	}
}
