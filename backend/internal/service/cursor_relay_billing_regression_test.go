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
						result, err = svc.handleCCStreamingFromNativeAnthropic(resp, c, model, model, model, nil, time.Now(), true)
					} else if protocol == "chat" {
						result, err = svc.handleCCBufferedFromNativeAnthropic(resp, c, account, model, model, model, nil, time.Now())
					} else if stream {
						result, err = svc.handleResponsesStreamingFromNativeAnthropic(resp, c, model, model, model, nil, time.Now(), apicompat.ResponsesClientToolMapping{})
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
