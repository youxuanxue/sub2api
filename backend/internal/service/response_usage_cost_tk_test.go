//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestInjectUsageCostJSON_SetsCostOnUsage(t *testing.T) {
	body := []byte(`{"id":"chatcmpl_1","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	got := InjectUsageCostJSON(body, 0.00123)
	require.InDelta(t, 0.00123, gjson.GetBytes(got, "usage.cost").Float(), 1e-12)
	require.Equal(t, int64(10), gjson.GetBytes(got, "usage.prompt_tokens").Int())
}

func TestInjectUsageCostJSON_NoUsageLeavesBodyUnchanged(t *testing.T) {
	body := []byte(`{"id":"chatcmpl_1","choices":[]}`)
	got := InjectUsageCostJSON(body, 0.5)
	require.Equal(t, string(body), string(got))
	require.False(t, gjson.GetBytes(got, "usage.cost").Exists())
}

func TestInjectUsageCostJSON_NegativeCostNoOp(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":1}}`)
	got := InjectUsageCostJSON(body, -1)
	require.Equal(t, string(body), string(got))
}

func TestInjectUsageCostJSON_AnthropicMessageUsage(t *testing.T) {
	body := []byte(`{"type":"message","usage":{"input_tokens":8,"output_tokens":2}}`)
	got := InjectUsageCostJSON(body, 0.42)
	require.InDelta(t, 0.42, gjson.GetBytes(got, "usage.cost").Float(), 1e-12)
}

func TestInjectUsageCostSSEDataLine_UsageChunk(t *testing.T) {
	line := `data: {"id":"x","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	got := InjectUsageCostSSEDataLine(line, 0.01)
	require.True(t, stringsHasPrefixData(got))
	payload := stringsTrimDataPrefix(got)
	require.InDelta(t, 0.01, gjson.Get(payload, "usage.cost").Float(), 1e-12)
}

func TestInjectUsageCostSSEDataLine_DoneUnchanged(t *testing.T) {
	require.Equal(t, "data: [DONE]", InjectUsageCostSSEDataLine("data: [DONE]", 0.01))
}

func TestInjectUsageCostSSEBlock_MessageDelta(t *testing.T) {
	block := "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":12}}\n\n"
	got := InjectUsageCostSSEBlock(block, 0.07)
	require.Contains(t, got, `"cost":0.07`)
	require.Contains(t, got, "event: message_delta")
}

func TestHandleNonStreamingResponse_InjectsUsageCost(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	gid := int64(42)
	apiKey := &APIKey{
		ID:      1,
		GroupID: &gid,
		User:    &User{ID: 9},
		Group:   &Group{ID: gid, Platform: PlatformAnthropic, RateMultiplier: 1.0},
	}
	c.Set("api_key", apiKey)

	body := []byte(`{"id":"msg_cost","type":"message","usage":{"input_tokens":100,"output_tokens":50}}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{})
	svc.rateLimitService = &RateLimitService{}

	usage, err := svc.handleNonStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, "claude-sonnet-4", "claude-sonnet-4")
	require.NoError(t, err)
	require.NotNil(t, usage)

	cost := gjson.GetBytes(rec.Body.Bytes(), "usage.cost")
	require.True(t, cost.Exists(), "response must include usage.cost: %s", rec.Body.String())
	require.Greater(t, cost.Float(), 0.0)
	require.Equal(t, int64(100), gjson.GetBytes(rec.Body.Bytes(), "usage.input_tokens").Int())
	stashed := TakePrecomputedResponseUsageCost(c)
	require.NotNil(t, stashed)
	require.InDelta(t, cost.Float(), stashed.ActualCost, 1e-12)

	// Validation: response cost must match RecordUsage actual_cost when reused.
	err = svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:       "resp_usage_cost_reuse",
			Usage:           *usage,
			Model:           "claude-sonnet-4",
			UpstreamModel:   "claude-sonnet-4",
			PrecomputedCost: stashed,
			Duration:        time.Second,
		},
		APIKey:  apiKey,
		User:    apiKey.User,
		Account: &Account{ID: 1},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.InDelta(t, cost.Float(), usageRepo.lastLog.ActualCost, 1e-12)
}

func TestSettleClaudeCustomerFacingCost_SharedByPreviewAndRecordUsage(t *testing.T) {
	gid := int64(7)
	apiKey := &APIKey{
		ID:      1,
		GroupID: &gid,
		User:    &User{ID: 3},
		Group:   &Group{ID: gid, Platform: PlatformAnthropic, RateMultiplier: 1.0},
	}
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	result := &ForwardResult{
		RequestID:     "settle_ssot_claude",
		Usage:         ClaudeUsage{InputTokens: 100, OutputTokens: 50},
		Model:         "claude-sonnet-4",
		UpstreamModel: "claude-sonnet-4",
		Duration:      time.Second,
	}
	settled, err := svc.settleClaudeCustomerFacingCost(context.Background(), &claudeCustomerFacingCostInput{
		Result:  result,
		APIKey:  apiKey,
		User:    apiKey.User,
		Account: &Account{ID: 1},
	})
	require.NoError(t, err)
	require.NotNil(t, settled)
	require.NotNil(t, settled.Cost)
	require.Greater(t, settled.Cost.ActualCost, 0.0)

	// RecordUsage without PrecomputedCost must bill the same ActualCost as settle.
	err = svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:     "settle_ssot_claude_record",
			Usage:         ClaudeUsage{InputTokens: 100, OutputTokens: 50},
			Model:         "claude-sonnet-4",
			UpstreamModel: "claude-sonnet-4",
			Duration:      time.Second,
		},
		APIKey:  apiKey,
		User:    apiKey.User,
		Account: &Account{ID: 1},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.InDelta(t, settled.Cost.ActualCost, usageRepo.lastLog.ActualCost, 1e-12)
}

func TestSettleOpenAICustomerFacingCost_SharedByPreviewAndRecordUsage(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	apiKey := openAIRecordUsageAPIKeyWithGroup(svc, 11, false)
	apiKey.User = &User{ID: 9, Balance: 100}
	apiKey.GroupID = &apiKey.Group.ID
	apiKey.Group.Platform = PlatformOpenAI
	apiKey.Group.RateMultiplier = 1.0
	account := &Account{ID: 2, Platform: PlatformOpenAI}

	result := &OpenAIForwardResult{
		RequestID:     "settle_ssot_openai",
		Model:         "gpt-4o",
		UpstreamModel: "gpt-4o",
		Usage: OpenAIUsage{
			InputTokens:  80,
			OutputTokens: 40,
		},
		Duration: time.Second,
	}
	settled, err := svc.settleOpenAICustomerFacingCost(context.Background(), &openAICustomerFacingCostInput{
		Result:         result,
		APIKey:         apiKey,
		User:           apiKey.User,
		Account:        account,
		BillingAccount: account,
	})
	require.NoError(t, err)
	require.NotNil(t, settled)
	require.NotNil(t, settled.Cost)
	require.Greater(t, settled.Cost.ActualCost, 0.0)

	err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:     "settle_ssot_openai_record",
			Model:         "gpt-4o",
			UpstreamModel: "gpt-4o",
			Usage: OpenAIUsage{
				InputTokens:  80,
				OutputTokens: 40,
			},
			Duration: time.Second,
		},
		APIKey:  apiKey,
		User:    apiKey.User,
		Account: account,
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.InDelta(t, settled.Cost.ActualCost, usageRepo.lastLog.ActualCost, 1e-12)
}

func TestPreviewClaudeForceCacheBilling_MatchesRecordUsagePrecomputed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx := WithForceCacheBilling(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)

	gid := int64(42)
	apiKey := &APIKey{
		ID:      1,
		GroupID: &gid,
		User:    &User{ID: 9},
		Group:   &Group{ID: gid, Platform: PlatformAnthropic, RateMultiplier: 1.0},
	}
	c.Set("api_key", apiKey)
	BindResponseUsageCostBillingWithTier(c, time.Time{}, ChannelUsageFields{}, "")

	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	account := &Account{ID: 1}
	usage := ClaudeUsage{InputTokens: 100, OutputTokens: 50}

	preview := svc.previewClaudeClientUsageCost(ctx, c, account, usage, "claude-sonnet-4", "claude-sonnet-4")
	require.NotNil(t, preview)
	require.Greater(t, preview.ActualCost, 0.0)

	// Without ForceCacheBilling the same tokens would price higher (input vs cache_read).
	plain := svc.previewClaudeClientUsageCost(context.Background(), c, account, usage, "claude-sonnet-4", "claude-sonnet-4")
	require.NotNil(t, plain)
	require.Greater(t, plain.ActualCost, preview.ActualCost)

	err := svc.RecordUsage(ctx, &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:       "force_cache_cost_ssot",
			Usage:           usage,
			Model:           "claude-sonnet-4",
			UpstreamModel:   "claude-sonnet-4",
			PrecomputedCost: preview,
			Duration:        time.Second,
		},
		APIKey:            apiKey,
		User:              apiKey.User,
		Account:           account,
		ForceCacheBilling: true,
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.InDelta(t, preview.ActualCost, usageRepo.lastLog.ActualCost, 1e-12)
	require.Equal(t, 0, usageRepo.lastLog.InputTokens)
	require.Equal(t, 100, usageRepo.lastLog.CacheReadTokens)
}

func stringsHasPrefixData(s string) bool {
	return len(s) >= 5 && s[:5] == "data:"
}

func stringsTrimDataPrefix(s string) string {
	if len(s) >= 6 && s[:6] == "data: " {
		return s[6:]
	}
	if len(s) >= 5 && s[:5] == "data:" {
		return s[5:]
	}
	return s
}
