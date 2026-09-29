package service

import (
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/integration/geminiweb"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tokenestimate"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func geminiWebTestAccount() *Account {
	return &Account{
		ID:       28,
		Platform: PlatformGemini,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			GeminiWebRelayCredentialKey: true,
			"api_key":                   "test-key",
		},
	}
}

func TestApplyGeminiWebTextUsageEstimateFillsEmptyUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)

	req := []byte(`{"contents":[{"parts":[{"text":"bill me please"}]}]}`)
	resp := []byte(`{"candidates":[{"content":{"parts":[{"text":"sure thing"}]}]}`)
	stashGeminiWebEstimateResponseBody(c, resp)

	result := &ForwardResult{Model: "gemini-3.8-flash", UpstreamModel: "gemini-web-flash"}
	applyGeminiWebTextUsageEstimate(c, geminiWebTestAccount(), result, req, nil)

	require.Equal(t, geminiweb.EstimatedBillingTier, result.BillingTier)
	require.Equal(t, tokenestimate.Count("bill me please"), result.Usage.InputTokens)
	require.Equal(t, tokenestimate.Count("sure thing"), result.Usage.OutputTokens)
	require.Greater(t, result.Usage.InputTokens, 0)
	require.Greater(t, result.Usage.OutputTokens, 0)
}

func TestApplyGeminiWebTextUsageEstimateKeepsReportedUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	stashGeminiWebEstimateResponseBody(c, []byte(`{"candidates":[{"content":{"parts":[{"text":"ignored"}]}}]}`))

	result := &ForwardResult{
		Usage: ClaudeUsage{InputTokens: 11, OutputTokens: 7},
	}
	applyGeminiWebTextUsageEstimate(c, geminiWebTestAccount(), result, []byte(`{"contents":[{"parts":[{"text":"x"}]}]}`), nil)

	require.Empty(t, result.BillingTier)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
}

func TestApplyGeminiWebTextUsageEstimateSkipsImageBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	stashGeminiWebEstimateResponseBody(c, []byte(`{"candidates":[{"content":{"parts":[{"text":"img"}]}}]}`))

	result := &ForwardResult{
		ImageCount: 1,
		ImageSize:  "1K",
		Usage:      ClaudeUsage{},
	}
	applyGeminiWebTextUsageEstimate(c, geminiWebTestAccount(), result, []byte(`{"contents":[{"parts":[{"text":"draw"}]}]}`), nil)

	require.Empty(t, result.BillingTier)
	require.Equal(t, 0, result.Usage.InputTokens)
	require.Equal(t, 0, result.Usage.OutputTokens)
}

func TestApplyGeminiWebTextUsageEstimateSkipsForwardError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	stashGeminiWebEstimateResponseBody(c, []byte(`{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`))

	result := &ForwardResult{}
	applyGeminiWebTextUsageEstimate(c, geminiWebTestAccount(), result, []byte(`{"contents":[{"parts":[{"text":"Draw an apple"}]}]}`), errors.New("incomplete Gemini stream"))

	require.Empty(t, result.BillingTier)
	require.Equal(t, 0, result.Usage.InputTokens)
	require.Equal(t, 0, result.Usage.OutputTokens)
}

func TestApplyGeminiWebTextUsageEstimateIgnoresNonWebAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	stashGeminiWebEstimateResponseBody(c, []byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`))

	plain := &Account{ID: 1, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "k"}}
	result := &ForwardResult{}
	applyGeminiWebTextUsageEstimate(c, plain, result, []byte(`{"contents":[{"parts":[{"text":"x"}]}]}`), nil)
	require.Empty(t, result.BillingTier)
	require.Equal(t, 0, result.Usage.InputTokens)
}
