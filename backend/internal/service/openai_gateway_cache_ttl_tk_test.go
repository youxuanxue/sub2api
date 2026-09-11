//go:build unit

package service

import (
	"context"
	"encoding/json"
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

func TestNativeAnthropicCacheTTLReachesSettlement(t *testing.T) {
	// Incident fixture: usage 9637895 reported 85 one-hour writes, but charged
	// $0.0181075 instead of $0.018745 after the TTL breakdown was discarded.
	const model = "claude-fable-5"
	for _, sample := range []struct {
		name                        string
		usage                       string
		fiveMin, oneHour, aggregate int
	}{
		{"live_one_hour", `"cache_creation_input_tokens":85,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":85}`, 0, 85, 85},
		{"mixed_ttl", `"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":15,"ephemeral_1h_input_tokens":85}`, 15, 85, 100},
		{"details_only", `"cache_creation":{"ephemeral_5m_input_tokens":15,"ephemeral_1h_input_tokens":85}`, 15, 85, 100},
		{"aggregate_only", `"cache_creation_input_tokens":85`, 0, 0, 85},
	} {
		for _, endpoint := range []string{"messages", "chat", "responses"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", sample.name, endpoint, stream), func(t *testing.T) {
					usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
					userRepo := &openAIRecordUsageUserRepoStub{}
					svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
					svc.cfg.Default.RateMultiplier = 1
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, nil)
					usage := `{"input_tokens":2,"output_tokens":0,"cache_read_input_tokens":16625,` + sample.usage + `}`
					wire := "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_cache_ttl","type":"message","role":"assistant","model":"` + model + `","content":[],"usage":` + usage + `}}` + "\n\n" +
						"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
						"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"TK217_OK"}}` + "\n\n" +
						"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\n" +
						"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}` + "\n\n" +
						"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"
					if endpoint == "messages" && !stream {
						wire = `{"type":"message","content":[{"type":"text","text":"TK217_OK"}],"usage":` + strings.Replace(usage, `"output_tokens":0`, `"output_tokens":8`, 1) + `}`
					}
					resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Request-Id": []string{"ttl-settlement"}}, Body: io.NopCloser(strings.NewReader(wire))}
					account := &Account{ID: 124, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 14}
					var result *OpenAIForwardResult
					var err error
					switch endpoint {
					case "messages":
						if stream {
							result, err = svc.streamNativeAnthropicMessages(c, resp, model, model, model, time.Now())
						} else {
							result, err = svc.bufferNativeAnthropicMessages(c, resp, model, model, model, time.Now())
						}
					case "chat":
						if stream {
							result, err = svc.handleCCStreamingFromNativeAnthropic(resp, c, model, model, model, nil, time.Now(), true)
						} else {
							result, err = svc.handleCCBufferedFromNativeAnthropic(resp, c, account, model, model, model, nil, time.Now())
						}
					case "responses":
						if stream {
							result, err = svc.handleResponsesStreamingFromNativeAnthropic(resp, c, model, model, model, nil, time.Now(), apicompat.ResponsesClientToolMapping{})
						} else {
							result, err = svc.handleResponsesBufferedFromNativeAnthropic(resp, c, account, model, model, model, nil, time.Now(), apicompat.ResponsesClientToolMapping{})
						}
					}
					require.NoError(t, err)
					require.Contains(t, recorder.Body.String(), "TK217_OK")
					require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 340}, User: &User{ID: 1}, Account: account}))
					logged := usageRepo.lastLog
					require.NotNil(t, logged)
					require.Equal(t, 2, logged.InputTokens)
					require.Equal(t, 8, logged.OutputTokens)
					require.Equal(t, 16625, logged.CacheReadTokens)
					require.Equal(t, sample.aggregate, logged.CacheCreationTokens)
					require.Equal(t, sample.fiveMin, logged.CacheCreation5mTokens)
					require.Equal(t, sample.oneHour, logged.CacheCreation1hTokens)
					expected, err := svc.billingService.CalculateCost(model, UsageTokens{InputTokens: 2, OutputTokens: 8, CacheReadTokens: 16625, CacheCreationTokens: sample.aggregate, CacheCreation5mTokens: sample.fiveMin, CacheCreation1hTokens: sample.oneHour}, 1)
					require.NoError(t, err)
					require.InDelta(t, expected.CacheCreationCost, logged.CacheCreationCost, 1e-12)
					require.InDelta(t, expected.TotalCost, logged.TotalCost, 1e-12)
					require.Equal(t, 1, userRepo.deductCalls)
					require.Equal(t, 1, usageRepo.calls)
					require.InDelta(t, expected.ActualCost, userRepo.lastAmount, 1e-12)
					if sample.name == "live_one_hour" {
						require.InDelta(t, 0.018745, logged.ActualCost, 1e-12)
					}
				})
			}
		}
	}
}

func TestMergeAnthropicUsageCacheTTLPreservesPresence(t *testing.T) {
	usage := &ClaudeUsage{}
	for _, body := range []string{
		`{"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":15,"ephemeral_1h_input_tokens":85}}`,
		`{"output_tokens":8}`,
	} {
		var incoming apicompat.AnthropicUsage
		require.NoError(t, json.Unmarshal([]byte(body), &incoming))
		mergeAnthropicUsage(usage, incoming)
	}
	require.Equal(t, 15, usage.CacheCreation5mTokens)
	require.Equal(t, 85, usage.CacheCreation1hTokens)
	var correction apicompat.AnthropicUsage
	require.NoError(t, json.Unmarshal([]byte(`{"cache_creation":{"ephemeral_1h_input_tokens":0}}`), &correction))
	mergeAnthropicUsage(usage, correction)
	require.Equal(t, 15, usage.CacheCreation5mTokens)
	require.Zero(t, usage.CacheCreation1hTokens)
	require.Equal(t, 15, usage.CacheCreationInputTokens)
	for _, sample := range []struct {
		body string
		want int
	}{
		{`{"cache_creation":{}}`, 15},
		{`{"cache_creation":{"ephemeral_5m_input_tokens":0}}`, 0},
	} {
		var incoming apicompat.AnthropicUsage
		require.NoError(t, json.Unmarshal([]byte(sample.body), &incoming))
		mergeAnthropicUsage(usage, incoming)
		require.Equal(t, sample.want, usage.CacheCreationInputTokens)
		require.Equal(t, sample.want, usage.CacheCreation5mTokens)
		require.Zero(t, usage.CacheCreation1hTokens)
	}
}
