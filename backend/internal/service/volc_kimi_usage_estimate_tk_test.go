//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// cn-prod 2026-11-04: Volc Agent Plan kimi-k3 accounts returned HTTP 200 with
// correct content but usage all zero (non-stream) or no usage chunk (stream).
const (
	volcKimiZeroUsageNonStream = `{"id":"x","object":"chat.completion","model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"The answer is two hundred and five.","reasoning_content":"Add the two numbers carefully."},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}}`
	volcKimiNoUsageStream      = "data: {\"id\":\"s1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"Add the two numbers carefully.\"}}]}\n\n" +
		"data: {\"id\":\"s1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"The answer is two hundred and five.\"}}]}\n\n" +
		"data: {\"id\":\"s1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	volcKimiZeroUsageChunkStream = "data: {\"id\":\"s2\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"The answer is two hundred and five.\"}}]}\n\n" +
		"data: {\"id\":\"s2\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"s2\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k3\",\"choices\":[],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"total_tokens\":0}}\n\n" +
		"data: [DONE]\n\n"
	volcKimiEstimateRequest = `{"model":"kimi-k3","stream":%s,"messages":[{"role":"system","content":"You are a careful calculator."},{"role":"user","content":"What is one hundred and two plus one hundred and three?"}],"tools":[{"type":"function","function":{"name":"add","description":"Add numbers","parameters":{"type":"object","properties":{"a":{"type":"number"}}}}}]}`
)

func forwardVolcKimiEstimateCase(t *testing.T, account *Account, stream bool, payload string) (*OpenAIForwardResult, *httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	flag, contentType := "false", "application/json"
	if stream {
		flag, contentType = "true", "text/event-stream"
	}
	body := []byte(strings.Replace(volcKimiEstimateRequest, "%s", flag, 1))
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {contentType}},
		Body:       io.NopCloser(strings.NewReader(payload)),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	return result, rec, c
}

func volcKimiEstimateEvents(c *gin.Context) []*OpsUpstreamErrorEvent {
	v, _ := c.Get(OpsUpstreamErrorsKey)
	events, _ := v.([]*OpsUpstreamErrorEvent)
	var out []*OpsUpstreamErrorEvent
	for _, ev := range events {
		if ev != nil && ev.Kind == "volc_kimi_usage_estimated" {
			out = append(out, ev)
		}
	}
	return out
}

func lastSSEUsage(t *testing.T, body string) gjson.Result {
	t.Helper()
	var usage gjson.Result
	for _, line := range strings.Split(body, "\n") {
		payload, ok := extractOpenAISSEDataLine(line)
		if !ok || strings.TrimSpace(payload) == "[DONE]" {
			continue
		}
		if u := gjson.Get(payload, "usage"); u.IsObject() {
			usage = u
		}
	}
	return usage
}

func TestVolcKimiNonStreamZeroUsageBillsEstimate(t *testing.T) {
	account := volcAgentPlanKimiAccountForTest()
	result, rec, c := forwardVolcKimiEstimateCase(t, account, false, volcKimiZeroUsageNonStream)

	require.Greater(t, result.Usage.InputTokens, 0)
	require.Greater(t, result.Usage.OutputTokens, 0)
	require.Zero(t, result.Usage.CacheReadInputTokens)
	require.Equal(t, "volc-kimi-estimated", result.BillingTier)

	// Client-visible usage equals what is billed.
	client := gjson.Get(rec.Body.String(), "usage")
	require.EqualValues(t, result.Usage.InputTokens, client.Get("prompt_tokens").Int())
	require.EqualValues(t, result.Usage.OutputTokens, client.Get("completion_tokens").Int())
	require.EqualValues(t, result.Usage.InputTokens+result.Usage.OutputTokens, client.Get("total_tokens").Int())
	require.Equal(t, "The answer is two hundred and five.", gjson.Get(rec.Body.String(), "choices.0.message.content").String())

	events := volcKimiEstimateEvents(c)
	require.Len(t, events, 1)
	require.Equal(t, account.ID, events[0].AccountID)
}

func TestVolcKimiStreamWithoutUsageChunkBillsEstimate(t *testing.T) {
	account := volcAgentPlanKimiAccountForTest()
	result, rec, c := forwardVolcKimiEstimateCase(t, account, true, volcKimiNoUsageStream)

	require.Greater(t, result.Usage.InputTokens, 0)
	require.Greater(t, result.Usage.OutputTokens, 0)
	require.Equal(t, "volc-kimi-estimated", result.BillingTier)

	out := rec.Body.String()
	usage := lastSSEUsage(t, out)
	require.True(t, usage.IsObject(), "an estimated usage chunk must reach the client")
	require.EqualValues(t, result.Usage.InputTokens, usage.Get("prompt_tokens").Int())
	require.EqualValues(t, result.Usage.OutputTokens, usage.Get("completion_tokens").Int())
	require.Less(t, strings.Index(out, `"usage"`), strings.LastIndex(out, "[DONE]"), "usage chunk precedes [DONE]")
	require.Contains(t, out, "The answer is two hundred and five.")
	require.Len(t, volcKimiEstimateEvents(c), 1)
}

func TestVolcKimiStreamZeroUsageChunkIsPatched(t *testing.T) {
	account := volcAgentPlanKimiAccountForTest()
	result, rec, c := forwardVolcKimiEstimateCase(t, account, true, volcKimiZeroUsageChunkStream)

	require.Greater(t, result.Usage.InputTokens, 0)
	require.Greater(t, result.Usage.OutputTokens, 0)
	out := rec.Body.String()
	require.Equal(t, 1, strings.Count(out, `"usage"`), "patch in place, do not add a second usage chunk")
	usage := lastSSEUsage(t, out)
	require.EqualValues(t, result.Usage.InputTokens, usage.Get("prompt_tokens").Int())
	require.EqualValues(t, result.Usage.OutputTokens, usage.Get("completion_tokens").Int())
	require.Len(t, volcKimiEstimateEvents(c), 1)
}

func TestVolcKimiRealUsageIsNeverEstimated(t *testing.T) {
	account := volcAgentPlanKimiAccountForTest()
	for _, stream := range []bool{false, true} {
		payload := volcKimiUsageNonStream
		if stream {
			payload = volcKimiUsageStream
		}
		result, rec, c := forwardVolcKimiEstimateCase(t, account, stream, payload)
		require.Equal(t, 20, result.Usage.InputTokens)
		require.Equal(t, 30, result.Usage.OutputTokens)
		require.Equal(t, 4, result.Usage.CacheReadInputTokens)
		require.Empty(t, result.BillingTier)
		require.Empty(t, volcKimiEstimateEvents(c))
		require.Contains(t, rec.Body.String(), `"prompt_tokens":20`)
	}
}

func TestVolcKimiEstimateScopedToVolcAgentPlanKimiK3(t *testing.T) {
	// Same zero-usage body from a generic OpenAI-compatible account: unchanged.
	other := rawChatCompletionsTestAccount()
	other.Extra = map[string]any{openai_compat.ExtraKeyResponsesSupported: false}
	result, rec, c := forwardVolcKimiEstimateCase(t, other, false, volcKimiZeroUsageNonStream)
	require.Zero(t, result.Usage.InputTokens)
	require.Zero(t, result.Usage.OutputTokens)
	require.Empty(t, result.BillingTier)
	require.Empty(t, volcKimiEstimateEvents(c))
	require.EqualValues(t, 0, gjson.Get(rec.Body.String(), "usage.prompt_tokens").Int())

	// Volc Agent Plan but a different model: unchanged.
	require.False(t, requiresVolcKimiUsageEstimate(volcAgentPlanKimiAccountForTest(), "deepseek-v4.1-flash"))
	require.True(t, requiresVolcKimiUsageEstimate(volcAgentPlanKimiAccountForTest(), "kimi-k3"))
	require.False(t, requiresVolcKimiUsageEstimate(other, "kimi-k3"))
}

func TestVolcKimiEstimatedUsageLogTier(t *testing.T) {
	require.Equal(t, "volc-kimi-estimated", *openAIUsageLogBillingTier(&OpenAIForwardResult{BillingTier: "volc-kimi-estimated"}))
	// Other OpenAI-path tiers keep their historical (unpersisted) behavior.
	require.Nil(t, openAIUsageLogBillingTier(&OpenAIForwardResult{BillingTier: "kiro-estimated"}))
	require.Nil(t, openAIUsageLogBillingTier(&OpenAIForwardResult{}))
}

func TestVolcKimiEstimatedUsageIsBilledAndTagged(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	usage := OpenAIUsage{InputTokens: 120, OutputTokens: 40}

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:   "volc_kimi_estimated",
			Usage:       usage,
			BillingTier: VolcKimiEstimatedBillingTier,
			Model:       "gpt-5.1",
			Duration:    time.Second,
		},
		APIKey:        &APIKey{ID: 1000, Quota: 100, Group: &Group{RateMultiplier: 1}},
		User:          &User{ID: 2000},
		Account:       &Account{ID: 3000, Type: AccountTypeAPIKey},
		APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, 120, usageRepo.lastLog.InputTokens)
	require.Equal(t, 40, usageRepo.lastLog.OutputTokens)
	require.NotNil(t, usageRepo.lastLog.BillingTier)
	require.Equal(t, VolcKimiEstimatedBillingTier, *usageRepo.lastLog.BillingTier)
	require.Greater(t, usageRepo.lastLog.ActualCost, 0.0)
	require.Greater(t, billingRepo.lastCmd.BalanceCost, 0.0)
}
