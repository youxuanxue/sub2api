package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

func TestOpenRouterProviderBuildModelDocument_SchemaVersionAndTextShape(t *testing.T) {
	cfg := DefaultOpenRouterProviderConfig()
	meta := &PublicCatalogModel{
		Capabilities: []string{"tool_use", "reasoning"},
		Pricing: PublicCatalogPricing{
			InputPer1KTokens:  2,
			OutputPer1KTokens: 6,
		},
		ContextWindow:   200000,
		MaxOutputTokens: 8192,
	}
	item := openRouterProviderBuildModelDocument(
		cfg, nil, meta,
		"tokenkey/demo", "demo", "tokenkey/demo",
		1,
		0.002, 0.006, 0, 1,
	)
	if item.SchemaVersion != openRouterProviderSchemaVersion {
		t.Fatalf("schema_version=%q", item.SchemaVersion)
	}
	if len(item.InputModalities) != 1 || item.InputModalities[0].Type != "text" {
		t.Fatalf("input=%+v", item.InputModalities)
	}
	if len(item.OutputModalities) != 1 || item.OutputModalities[0].Type != "text" {
		t.Fatalf("output=%+v", item.OutputModalities)
	}
	inPricing := item.InputModalities[0].Pricing
	if len(inPricing) != 1 || inPricing[0].Type != "prompt" || inPricing[0].CostUSD != formatOpenRouterUSDPerToken(0.002) {
		t.Fatalf("input pricing=%+v", inPricing)
	}
	outPricing := item.OutputModalities[0].Pricing
	if len(outPricing) != 1 || outPricing[0].Type != "completion" {
		t.Fatalf("output pricing=%+v", outPricing)
	}
	params := item.OutputModalities[0].SupportedParameters
	if params["tools"].Type != "boolean" || params["reasoning"].Type != "boolean" {
		t.Fatalf("params=%+v", params)
	}
	// 2.4 must not zero-stuff unused root SKUs.
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	if _, ok := generic["pricing"]; ok {
		t.Fatalf("flat pricing must not appear: %s", raw)
	}
	if _, ok := generic["context_length"]; ok {
		t.Fatalf("flat context_length must not appear: %s", raw)
	}
}

func TestOpenRouterProviderBuildModelDocument_OmitsZeroPriceEntries(t *testing.T) {
	cfg := DefaultOpenRouterProviderConfig()
	item := openRouterProviderBuildModelDocument(
		cfg, nil, nil,
		"tokenkey/demo", "demo", "tokenkey/demo",
		1,
		0.001, 0, 0, 1,
	)
	if len(item.InputModalities[0].Pricing) != 1 {
		t.Fatalf("pricing=%+v", item.InputModalities[0].Pricing)
	}
	if len(item.OutputModalities[0].Pricing) != 0 {
		t.Fatalf("zero completion must be omitted: %+v", item.OutputModalities[0].Pricing)
	}
}

func TestOpenRouterProviderBuildModelDocument_PeakWindowEntries(t *testing.T) {
	cfg := DefaultOpenRouterProviderConfig()
	group := &Group{
		PeakRateEnabled:    true,
		PeakStart:          "09:00",
		PeakEnd:            "12:00",
		PeakRateMultiplier: 2,
		SubscriptionType:   SubscriptionTypeSubscription,
	}
	item := openRouterProviderBuildModelDocument(
		cfg, group, nil,
		"tokenkey/demo", "demo", "tokenkey/demo",
		1,
		0.000002, 0.000006, 0.000001, 1.5,
	)
	in := item.InputModalities[0].Pricing
	if len(in) < 2 {
		t.Fatalf("expected base+peak prompt entries, got %+v", in)
	}
	basePrompt := in[0]
	if basePrompt.Type != "prompt" || basePrompt.UTCStart != nil {
		t.Fatalf("base prompt=%+v", basePrompt)
	}
	if basePrompt.CostUSD != formatOpenRouterUSDPerToken(0.000003) {
		t.Fatalf("off-peak prompt=%q", basePrompt.CostUSD)
	}
	peak := in[1]
	if peak.UTCStart == nil || peak.UTCEnd == nil || peak.CostUSD != formatOpenRouterUSDPerToken(0.000006) {
		t.Fatalf("peak prompt=%+v", peak)
	}
}

func TestOpenRouterProviderLocalHMToUTCMinutes(t *testing.T) {
	start, ok := openRouterProviderLocalHMToUTCMinutes("09:00", timezone.Location(), mustParseDate(t, "2026-07-28"))
	if !ok {
		t.Fatal("expected conversion")
	}
	if start <= 0 {
		t.Fatalf("start=%d", start)
	}
}

func TestOpenRouterProviderSupportedFeatureFlags(t *testing.T) {
	meta := &PublicCatalogModel{
		Capabilities: []string{"tool_use", "reasoning", "response_schema"},
	}
	features := openRouterProviderSupportedFeatureFlags(meta)
	if len(features) < 3 {
		t.Fatalf("features=%v", features)
	}
	if got := openRouterProviderSupportedFeatureFlags(nil); got != nil {
		t.Fatalf("got=%v want nil", got)
	}
}

func TestOpenRouterProviderBuildModelDocument_VideoBillingUsesSecondSKU(t *testing.T) {
	cfg := DefaultOpenRouterProviderConfig()
	meta := &PublicCatalogModel{
		Pricing: PublicCatalogPricing{
			BillingMode:         "video",
			OutputCostPerSecond: 0.05,
		},
	}
	item := openRouterProviderBuildModelDocument(
		cfg, nil, meta,
		"tokenkey/veo", "veo", "tokenkey/veo",
		1, 0, 0, 0, 1,
	)
	if len(item.OutputModalities) != 1 || item.OutputModalities[0].Type != "video" {
		t.Fatalf("output=%+v", item.OutputModalities)
	}
	pricing := item.OutputModalities[0].Pricing
	if len(pricing) != 1 || pricing[0].Unit != "second" || pricing[0].CostUSD != formatOpenRouterUSDPerToken(0.05) {
		t.Fatalf("pricing=%+v", pricing)
	}
}

func TestOpenRouterProviderBuildModelDocument_ImageBillingUsesImageSKU(t *testing.T) {
	cfg := DefaultOpenRouterProviderConfig()
	meta := &PublicCatalogModel{
		Pricing: PublicCatalogPricing{
			BillingMode:        "image",
			OutputCostPerImage: 0.04,
		},
	}
	item := openRouterProviderBuildModelDocument(
		cfg, nil, meta,
		"tokenkey/imagen", "imagen", "tokenkey/imagen",
		1, 0, 0, 0, 1,
	)
	pricing := item.OutputModalities[0].Pricing
	if len(pricing) != 1 || pricing[0].Unit != "image" || pricing[0].CostUSD != formatOpenRouterUSDPerToken(0.04) {
		t.Fatalf("pricing=%+v", pricing)
	}
}

func TestOpenRouterProviderBuildModelDocument_TierOverridesPreferPeak(t *testing.T) {
	cfg := DefaultOpenRouterProviderConfig()
	meta := &PublicCatalogModel{
		Pricing: PublicCatalogPricing{
			Tiers: []PublicCatalogTier{
				{MinTokens: 0, InputPer1KTokens: 1, OutputPer1KTokens: 2},
				{MinTokens: 200000, InputPer1KTokens: 2, OutputPer1KTokens: 4},
			},
		},
	}
	group := &Group{PeakRateEnabled: true, PeakStart: "09:00", PeakEnd: "12:00", PeakRateMultiplier: 2}
	item := openRouterProviderBuildModelDocument(
		cfg, group, meta,
		"tokenkey/demo", "demo", "tokenkey/demo",
		1, 0.000002, 0.000006, 0, 1,
	)
	in := item.InputModalities[0].Pricing
	if len(in) != 1 {
		t.Fatalf("tier models must not emit peak window entries: %+v", in)
	}
	if len(in[0].Overrides) != 1 {
		t.Fatalf("overrides=%+v", in[0].Overrides)
	}
	when, _ := in[0].Overrides[0].When["prompt_tokens"].(map[string]any)
	if when["gte"] != 200000 {
		t.Fatalf("when=%+v", in[0].Overrides[0].When)
	}
}

func TestOpenRouterProviderInputModalityTypes_VisionAddsImage(t *testing.T) {
	meta := &PublicCatalogModel{Capabilities: []string{"vision"}}
	got := openRouterProviderInputModalityTypes(meta)
	if len(got) != 2 || got[1] != "image" {
		t.Fatalf("modalities=%v", got)
	}
}

func TestOpenRouterProviderModelHasListedPrice_TokenPriced(t *testing.T) {
	if !openRouterProviderModelHasListedPrice(nil, 0.000002, 0) {
		t.Fatal("token prompt price should qualify")
	}
}

func TestOpenRouterProviderModelHasListedPrice_ImagePriced(t *testing.T) {
	meta := &PublicCatalogModel{
		Pricing: PublicCatalogPricing{
			BillingMode:        "image",
			OutputCostPerImage: 0.04,
		},
	}
	if !openRouterProviderModelHasListedPrice(meta, 0, 0) {
		t.Fatal("per-image price should qualify")
	}
}

func TestOpenRouterProviderModelHasListedPrice_VideoPriced(t *testing.T) {
	meta := &PublicCatalogModel{
		Pricing: PublicCatalogPricing{
			BillingMode:         "video",
			OutputCostPerSecond: 0.6,
		},
	}
	if !openRouterProviderModelHasListedPrice(meta, 0, 0) {
		t.Fatal("per-second video price should qualify")
	}
}

func TestOpenRouterProviderModelHasListedPrice_UnpricedSkipped(t *testing.T) {
	if openRouterProviderModelHasListedPrice(nil, 0, 0) {
		t.Fatal("unpriced model must not qualify")
	}
	if openRouterProviderModelHasListedPrice(&PublicCatalogModel{}, 0, 0) {
		t.Fatal("empty meta must not qualify")
	}
}

func mustParseDate(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
