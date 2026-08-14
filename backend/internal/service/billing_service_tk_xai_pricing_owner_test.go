//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func expectedGrokPricingOwner(canonical string) (string, bool) {
	switch canonical {
	case "grok-4.6", "grok-4.5", "grok-4.3", "grok-build-0.1":
		return canonical, true
	case "grok-composer-2.5-fast":
		return "grok-build-0.1", true
	case "grok-4.20-0309-reasoning", "grok-4.20-0309-non-reasoning", "grok-4.20-multi-agent-0309":
		return canonical, true
	case "grok-3-mini", "grok-3-mini-fast":
		return "", true
	default:
		return "", false
	}
}

func TestGrokPricingOwnersFollowRoutingSSOT(t *testing.T) {
	mapping := xai.ModelMappingWithOptions(xai.ModelMappingOptions{
		DefaultText:          xai.DefaultTextModel,
		EnableCrossClientMap: false,
	})

	for alias := range mapping {
		if !xai.IsGrokTextResponsesModelID(alias) {
			continue
		}
		canonical := xai.ResolveGrokTextResponsesModelID(alias, xai.DefaultTextModel)
		expected, covered := expectedGrokPricingOwner(canonical)
		require.Truef(t, covered, "routing canonical %q for alias %q has no pricing policy", canonical, alias)
		owner, known := resolveGrokTextPricingOwner(alias)
		require.Truef(t, known, "routing-known alias %q was not recognized", alias)
		require.Equalf(t, expected, owner, "alias=%q canonical=%q", alias, canonical)

		if !strings.Contains(alias, "/") {
			for _, prefix := range []string{"xai/", "x-ai/", "grok/"} {
				prefixedOwner, prefixedKnown := resolveGrokTextPricingOwner(prefix + alias)
				require.True(t, prefixedKnown, prefix+alias)
				require.Equal(t, expected, prefixedOwner, prefix+alias)
			}
		}
	}

	required := map[string]string{
		"grok-latest":       "grok-4.3",
		"grok-build-latest": "grok-4.5",
		"grok-composer":     "grok-build-0.1",
	}
	for alias, expected := range required {
		owner, known := resolveGrokTextPricingOwner(alias)
		require.True(t, known, alias)
		require.Equal(t, expected, owner, alias)
	}
}

func TestGrokPricingOwnerKnownUnpricedFailsClosed(t *testing.T) {
	svc := newTestBillingService()
	for _, model := range []string{
		"grok-3-mini", "grok-3-mini-fast", "xai/grok-3-mini", "x-ai/grok-3-mini-fast",
	} {
		owner, known := resolveGrokTextPricingOwner(model)
		require.True(t, known, model)
		require.Empty(t, owner, model)
		require.Nil(t, svc.getFallbackPricing(model), model)
	}

	for _, model := range []string{"grok-5", "grok-5-latest", "x-ai/grok-7", "grok-4.7-beta"} {
		owner, known := resolveGrokTextPricingOwner(model)
		require.False(t, known, model)
		require.Empty(t, owner, model)
		require.NotNil(t, svc.getFallbackPricing(model), model)
	}
}

func TestGrokRegistryAliasesUseCanonicalOwners(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	billing := NewBillingService(&config.Config{}, NewPricingService(&config.Config{}, nil))

	cases := map[string]string{
		"grok-latest":             "grok-latest", // direct registry row remains the data owner
		"grok-build-latest":       "grok-build-latest",
		"xai/grok-latest":         "grok-4.3",
		"x-ai/grok-build-latest":  "grok-4.5",
		"grok/grok-composer":      "grok-build-0.1",
		"grok-4.20-reasoning":     "grok-4.20-0309-reasoning",
		"grok-4.20-non-reasoning": "grok-4.20-0309-non-reasoning",
		"grok-4.20-multi-agent":   "grok-4.20-multi-agent-0309",
		"grok-composer-2.5-fast":  "grok-build-0.1",
	}
	for alias, expectedOwner := range cases {
		pricing := billing.getRegistryAliasPricing(alias)
		require.NotNil(t, pricing, alias)
		require.Equal(t, expectedOwner, pricing.registryOwner, alias)
		ownerPricing := tkRegistryAliasOwnerPricing(expectedOwner)
		require.NotNil(t, ownerPricing, expectedOwner)
		require.Equal(t, ownerPricing.InputPricePerToken, pricing.InputPricePerToken, alias)
		require.Equal(t, ownerPricing.CacheReadPricePerToken, pricing.CacheReadPricePerToken, alias)
		require.Equal(t, ownerPricing.OutputPricePerToken, pricing.OutputPricePerToken, alias)
		require.Equal(t, ownerPricing.LongContextInputThreshold, pricing.LongContextInputThreshold, alias)
		require.Equal(t, ownerPricing.LongContextThresholdInclusive, pricing.LongContextThresholdInclusive, alias)
		require.Equal(t, ownerPricing.LongContextInputMultiplier, pricing.LongContextInputMultiplier, alias)
		require.Equal(t, ownerPricing.LongContextOutputMultiplier, pricing.LongContextOutputMultiplier, alias)
	}

	for _, model := range []string{"grok-3-mini", "xai/grok-3-mini-fast"} {
		require.Nil(t, billing.getRegistryAliasPricing(model), model)
	}
}

func TestGrokDirectRegistryRowsMatchSemanticOwners(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	cases := map[string]string{
		"grok-latest":           "grok-4.3",
		"grok-4.3-latest":       "grok-4.3",
		"grok-4.5-latest":       "grok-4.5",
		"grok-build-latest":     "grok-4.5",
		"grok-code-fast":        "grok-build-0.1",
		"grok-code-fast-1":      "grok-build-0.1",
		"grok-code-fast-1-0825": "grok-build-0.1",
		"grok-4-fast-reasoning": "grok-4.3",
	}
	for directOwner, semanticOwner := range cases {
		direct := tkRegistryAliasOwnerPricing(directOwner)
		semantic := tkRegistryAliasOwnerPricing(semanticOwner)
		require.NotNil(t, direct, directOwner)
		require.NotNil(t, semantic, semanticOwner)
		direct.registryOwner = ""
		semantic.registryOwner = ""
		require.Equal(t, semantic, direct, directOwner)
	}
}

func TestGrokLongContextInclusiveBoundaryCosts(t *testing.T) {
	svc := newTestBillingService()
	card := &ModelPricing{
		InputPricePerToken:            2e-6,
		OutputPricePerToken:           6e-6,
		CacheCreationPricePerToken:    2e-6,
		CacheReadPricePerToken:        0.5e-6,
		LongContextInputThreshold:     200000,
		LongContextThresholdInclusive: true,
		LongContextInputMultiplier:    2,
		LongContextOutputMultiplier:   2,
	}

	cases := []struct {
		name     string
		tokens   UsageTokens
		elevated bool
	}{
		{"199999", UsageTokens{InputTokens: 199999, OutputTokens: 7}, false},
		{"200000", UsageTokens{InputTokens: 200000, OutputTokens: 7}, true},
		{"200001", UsageTokens{InputTokens: 200001, OutputTokens: 7}, true},
		{"cache_creation_reaches_200000", UsageTokens{InputTokens: 100000, CacheCreationTokens: 100000, OutputTokens: 7}, true},
		{"cache_read_reaches_200000", UsageTokens{InputTokens: 50000, CacheCreationTokens: 50000, CacheReadTokens: 100000, OutputTokens: 7}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := svc.computeTokenBreakdown(card, tc.tokens, 1, "", false, true)
			multiplier := 1.0
			if tc.elevated {
				multiplier = 2
			}
			require.InDelta(t, float64(tc.tokens.InputTokens)*card.InputPricePerToken*multiplier, got.InputCost, 1e-12)
			require.InDelta(t, float64(tc.tokens.CacheCreationTokens)*card.CacheCreationPricePerToken*multiplier, got.CacheCreationCost, 1e-12)
			require.InDelta(t, float64(tc.tokens.CacheReadTokens)*card.CacheReadPricePerToken*multiplier, got.CacheReadCost, 1e-12)
			require.InDelta(t, float64(tc.tokens.OutputTokens)*card.OutputPricePerToken*multiplier, got.OutputCost, 1e-12)
			require.Equal(t, tc.elevated, got.LongContextBillingApplied)
		})
	}
}

func TestGrokLongContextLegacyStrictBoundary(t *testing.T) {
	svc := newTestBillingService()
	card := &ModelPricing{
		InputPricePerToken:          2e-6,
		OutputPricePerToken:         6e-6,
		LongContextInputThreshold:   200000,
		LongContextInputMultiplier:  2,
		LongContextOutputMultiplier: 2,
	}
	require.False(t, svc.computeTokenBreakdown(card, UsageTokens{InputTokens: 199999, OutputTokens: 1}, 1, "", false, true).LongContextBillingApplied)
	require.False(t, svc.computeTokenBreakdown(card, UsageTokens{InputTokens: 200000, OutputTokens: 1}, 1, "", false, true).LongContextBillingApplied)
	require.True(t, svc.computeTokenBreakdown(card, UsageTokens{InputTokens: 200001, OutputTokens: 1}, 1, "", false, true).LongContextBillingApplied)
}

func TestGrokLongContextGroupOptOut(t *testing.T) {
	svc := newTestBillingService()
	resolver := NewModelPricingResolver(nil, svc)
	for _, inputTokens := range []int{200000, 200001} {
		off := &Group{LongContextPricingEnabled: false}
		got, err := svc.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: "grok-4.6", Group: off,
			Tokens:         UsageTokens{InputTokens: inputTokens, OutputTokens: 1},
			RateMultiplier: 1, Resolver: resolver,
		})
		require.NoError(t, err)
		require.False(t, got.LongContextBillingApplied)
		require.InDelta(t, float64(inputTokens)*2e-6, got.InputCost, 1e-12)
		require.InDelta(t, 6e-6, got.OutputCost, 1e-12)
	}
}

func TestGrokLongContextInclusiveSchemaPropagation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		want  bool
	}{
		{"absent", "", false},
		{"explicit_false", `,"long_context_threshold_inclusive":false`, false},
		{"explicit_true", `,"long_context_threshold_inclusive":true`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"grok-test":{"mode":"chat","input_cost_per_token":0.000001,` +
				`"output_cost_per_token":0.000002,"long_context_input_token_threshold":200000,` +
				`"long_context_input_cost_multiplier":2,"long_context_output_cost_multiplier":2` + tc.field + `}}`
			doc, err := parseTKOverlayDocument([]byte(raw))
			require.NoError(t, err)
			immutable := doc.Models["grok-test"]
			require.NotNil(t, immutable)
			require.Equal(t, tc.want, immutable.LongContextThresholdInclusive)

			got := tkModelPricingFromLiteLLM(immutable)
			require.NotNil(t, got)
			require.Equal(t, tc.want, got.LongContextThresholdInclusive)
		})
	}
}
