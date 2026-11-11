//go:build unit

package service

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/stretchr/testify/require"
)

// Regression anchors for the New API bridge → TokenKey image billing handoff.
//
// New API's dto.Usage has no output_tokens_details field, so
// output_tokens_details.image_tokens never survives the adaptor conversion.
// TokenKey's token-priced image owners (gpt-image-2 / 2.5 / 2.6) bill from
// ImageOutputTokens and fail closed when it is zero
// (billing_service_tk_image_token_settlement.go). Live evidence: tokensea
// newapi accounts returned 200 images while
// openai_images_generations.record_usage_failed logged
// "image output tokens unavailable for token-priced image owner".

func TestOpenAIUsageFromNewAPIDTO_DropsImageTokens(t *testing.T) {
	upstream := &dto.Usage{PromptTokens: 12, CompletionTokens: 196, TotalTokens: 208}
	got := openAIUsageFromNewAPIDTO(upstream)
	require.Equal(t, 12, got.InputTokens)
	require.Equal(t, 196, got.OutputTokens)
	require.Zero(t, got.ImageOutputTokens,
		"dto.Usage cannot carry image tokens; mergeBridgeImageUsage must supply them")
}

func TestMergeBridgeImageUsage_FeedsTokenPricedOwner(t *testing.T) {
	usage := openAIUsageFromNewAPIDTO(&dto.Usage{PromptTokens: 12, CompletionTokens: 196, TotalTokens: 208})
	mergeBridgeImageUsage(&usage, &bridge.ImageUsageDetails{ImageOutputTokens: 196, TextOutputTokens: 0})

	require.Equal(t, 196, usage.OutputTokens)
	require.Equal(t, 196, usage.ImageOutputTokens)

	tokens := UsageTokens{
		InputTokens:       usage.InputTokens,
		OutputTokens:      usage.OutputTokens,
		ImageOutputTokens: usage.ImageOutputTokens,
	}
	require.Greater(t, tokens.ImageOutputTokens, 0,
		"token-priced image settlement requires a non-zero image output token count")
}

func TestMergeBridgeImageUsage_TextOnlyOutputFillsOutputTokens(t *testing.T) {
	usage := openAIUsageFromNewAPIDTO(&dto.Usage{})
	mergeBridgeImageUsage(&usage, &bridge.ImageUsageDetails{ImageOutputTokens: 196, TextOutputTokens: 8})

	require.Equal(t, 8, usage.OutputTokens)
	require.Equal(t, 196, usage.ImageOutputTokens)
}

func TestMergeBridgeImageUsage_NilObservedIsNoOp(t *testing.T) {
	usage := openAIUsageFromNewAPIDTO(&dto.Usage{PromptTokens: 3, CompletionTokens: 4})
	mergeBridgeImageUsage(&usage, nil)

	require.Equal(t, 3, usage.InputTokens)
	require.Equal(t, 4, usage.OutputTokens)
	require.Zero(t, usage.ImageOutputTokens)
}

// Observed image input tokens (image edits) must survive the merge too.
func TestMergeBridgeImageUsage_CarriesImageInputTokens(t *testing.T) {
	usage := openAIUsageFromNewAPIDTO(&dto.Usage{PromptTokens: 5, CompletionTokens: 196})
	mergeBridgeImageUsage(&usage, &bridge.ImageUsageDetails{ImageInputTokens: 42, ImageOutputTokens: 196})

	require.Equal(t, 42, usage.ImageInputTokens)
	require.Equal(t, 196, usage.ImageOutputTokens)
}

// End-to-end: the live tokensea image usage shape, after the bridge merge, must
// settle a positive amount for a token-priced image owner instead of failing
// closed. Values mirror a real 2026-11-11 /v1/images/generations response from
// agent.tokensea.ai (input 12 text tokens, output 196 image tokens).
func TestBridgeImageUsage_SettlesTokenPricedOwner(t *testing.T) {
	svc := activeRegistryBillingService(t)

	// Upstream JSON usage as tokensea returned it.
	upstream := &dto.Usage{PromptTokens: 12, CompletionTokens: 196, TotalTokens: 208}
	usage := openAIUsageFromNewAPIDTO(upstream)
	require.Zero(t, usage.ImageOutputTokens)

	// Before the fix: zero image output tokens -> fail closed, no charge.
	_, err := svc.TkCalculateImageTokenCost("gpt-image-2.5-flare", UsageTokens{
		InputTokens:       usage.InputTokens,
		OutputTokens:      usage.OutputTokens,
		ImageOutputTokens: usage.ImageOutputTokens,
	}, 1)
	require.ErrorIs(t, err, ErrImageUsageTokensUnavailable)

	// After the merge with the observed breakdown: positive settlement.
	mergeBridgeImageUsage(&usage, &bridge.ImageUsageDetails{ImageOutputTokens: 196})
	cost, err := svc.TkCalculateImageTokenCost("gpt-image-2.5-flare", UsageTokens{
		InputTokens:       usage.InputTokens,
		OutputTokens:      usage.OutputTokens,
		ImageOutputTokens: usage.ImageOutputTokens,
	}, 1)
	require.NoError(t, err)
	require.NotNil(t, cost)
	require.Positive(t, cost.TotalCost)
	require.Positive(t, cost.ImageOutputCost)
	require.Equal(t, string(BillingModeImage), cost.BillingMode)
}
