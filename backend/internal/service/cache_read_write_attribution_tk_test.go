package service

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func cacheAttributionBilling(t *testing.T) *BillingService {
	t.Helper()
	billing := NewBillingService(&config.Config{}, nil)
	billing.fallbackPrices["gpt-6.1-sol"] = &ModelPricing{
		InputPricePerToken: 2e-6, CacheReadPricePerToken: 0.1e-6,
		CacheCreationPricePerToken: 2.5e-6, OutputPricePerToken: 10e-6,
	}
	billing.fallbackPrices["gpt-6-astra"] = &ModelPricing{
		InputPricePerToken: 2e-6, CacheReadPricePerToken: 0.1e-6,
		CacheCreationPricePerToken: 2.5e-6, OutputPricePerToken: 10e-6,
	}
	billing.fallbackPrices["qwen3.8-flash"] = &ModelPricing{
		InputPricePerToken: 0.8e-6, CacheReadPricePerToken: 0.1e-6,
		CacheCreationPricePerToken: 1.25e-6, OutputPricePerToken: 2.7e-6,
	}
	billing.fallbackPrices["qwen3.7-plus"] = billing.fallbackPrices["qwen3.8-flash"]
	billing.fallbackPrices["kimi-k3"] = &ModelPricing{
		InputPricePerToken: 20e-6, CacheReadPricePerToken: 2e-6,
		OutputPricePerToken: 100e-6,
	}
	billing.fallbackPrices["deepseek-v4-flash"] = &ModelPricing{
		InputPricePerToken: 1e-6, CacheReadPricePerToken: 0.02e-6,
		OutputPricePerToken: 4e-6,
	}
	billing.fallbackPrices["claude-opus-5-5"] = &ModelPricing{
		InputPricePerToken: 5e-6, CacheReadPricePerToken: 0.5e-6,
		CacheCreationPricePerToken: 10e-6, OutputPricePerToken: 25e-6,
	}
	billing.fallbackPrices["gemini-4-argon"] = &ModelPricing{
		InputPricePerToken: 1e-6, CacheReadPricePerToken: 0.1e-6,
		OutputPricePerToken: 5e-6,
	}
	return billing
}

func TestApplyOpenAIUpstreamCacheReadWriteAttribution(t *testing.T) {
	billing := cacheAttributionBilling(t)
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	newAPI := &Account{Platform: PlatformNewAPI}

	t.Run("OpenAI OAuth attributes uncached prompt and preserves total", func(t *testing.T) {
		usage := OpenAIUsage{InputTokens: 143818, CacheReadInputTokens: 142720}
		ApplyOpenAIUpstreamCacheReadWriteAttribution(billing, oauth, "gpt-6.1-sol", &usage)
		require.Equal(t, 142720, usage.CacheReadInputTokens)
		require.Equal(t, 1098, usage.CacheCreationInputTokens)
		require.Equal(t, 143818, usage.InputTokens)

		cost, err := billing.CalculateCost("gpt-6.1-sol", UsageTokens{
			InputTokens:         usage.InputTokens - usage.CacheReadInputTokens - usage.CacheCreationInputTokens,
			CacheReadTokens:     usage.CacheReadInputTokens,
			CacheCreationTokens: usage.CacheCreationInputTokens,
		}, 1)
		require.NoError(t, err)
		require.InDelta(t, 142720*0.1e-6+1098*2.5e-6, cost.InputCost+cost.CacheReadCost+cost.CacheCreationCost, 1e-12)
	})

	t.Run("below minimum remains ordinary input", func(t *testing.T) {
		usage := OpenAIUsage{InputTokens: 142720 + 500, CacheReadInputTokens: 142720}
		ApplyOpenAIUpstreamCacheReadWriteAttribution(billing, oauth, "gpt-6-astra", &usage)
		require.Equal(t, 0, usage.CacheCreationInputTokens)
		require.Equal(t, 142720+500, usage.InputTokens)
	})

	t.Run("NewAPI Qwen attributes cache write", func(t *testing.T) {
		usage := OpenAIUsage{InputTokens: 14244, CacheReadInputTokens: 10240}
		ApplyOpenAIUpstreamCacheReadWriteAttribution(billing, newAPI, "qwen3.8-flash", &usage)
		require.Equal(t, 4004, usage.CacheCreationInputTokens)
	})

	for _, model := range []string{"kimi-k3", "deepseek-v4-flash"} {
		t.Run(model+" keeps uncached input", func(t *testing.T) {
			usage := OpenAIUsage{InputTokens: 12000, CacheReadInputTokens: 10240}
			ApplyOpenAIUpstreamCacheReadWriteAttribution(billing, newAPI, model, &usage)
			require.Equal(t, 0, usage.CacheCreationInputTokens)
			require.Equal(t, 12000, usage.InputTokens)
		})
	}
}

func TestApplyClaudeUpstreamCacheReadWriteAttribution(t *testing.T) {
	billing := cacheAttributionBilling(t)
	account := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth}

	usage := ClaudeUsage{InputTokens: 4004, CacheReadInputTokens: 10240}
	ApplyClaudeUpstreamCacheReadWriteAttribution(billing, account, "claude-opus-5-5", &usage)
	require.Equal(t, 0, usage.InputTokens)
	require.Equal(t, 4004, usage.CacheCreationInputTokens)
	require.Equal(t, 10240, usage.CacheReadInputTokens)

	gemini := ClaudeUsage{InputTokens: 4004, CacheReadInputTokens: 10240}
	ApplyClaudeUpstreamCacheReadWriteAttribution(billing, account, "gemini-4-argon", &gemini)
	require.Equal(t, 4004, gemini.InputTokens)
	require.Equal(t, 0, gemini.CacheCreationInputTokens)
}

func TestApplyCacheReadWriteAttributionEligibility(t *testing.T) {
	billing := cacheAttributionBilling(t)
	require.True(t, billing.ModelDistinguishesCacheReadAndWrite("gpt-6.1-sol"))
	require.False(t, billing.ModelDistinguishesCacheReadAndWrite("kimi-k3"))
	require.False(t, shouldAttributeUpstreamCacheWrite(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}))
	require.True(t, shouldAttributeUpstreamCacheWrite(&Account{Platform: PlatformOpenAI, Type: AccountTypeSetupToken}))
}

func TestNewAPIChatToAnthropicBridgePreservesCacheWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, details := range []string{
		`"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":9}`,
		`"prompt_tokens_details":{"cached_tokens":20,"cache_creation_tokens":7}`,
	} {
		t.Run(details, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			body := bytes.NewBufferString("data: " + `{"choices":[{"delta":{"content":"ok"}}],"usage":{"prompt_tokens":100,"completion_tokens":10,` + details + "}}\n")
			result, err := convertBufferedChatCompletionsToAnthropicJSON(c, body, "claude-opus-5-5", "claude-opus-5-5", "upstream", time.Now())
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 20, result.Usage.CacheReadInputTokens)
			wantWrite := 9
			if details[0] == '"' {
				wantWrite = 9
			}
			if bytes.Contains([]byte(details), []byte("cache_creation_tokens")) {
				wantWrite = 7
			}
			require.Equal(t, wantWrite, result.Usage.CacheCreationInputTokens)

			var response apicompat.AnthropicResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			require.Equal(t, 100-20-wantWrite, response.Usage.InputTokens)
			require.Equal(t, result.Usage.CacheCreationInputTokens, response.Usage.CacheCreationInputTokens)
			require.Equal(t, result.Usage.CacheReadInputTokens, response.Usage.CacheReadInputTokens)
		})
	}
}
