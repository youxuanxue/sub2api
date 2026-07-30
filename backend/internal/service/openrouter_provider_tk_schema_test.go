package service

import (
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

func TestOpenRouterProviderBuildPricing_IncludesZeroSKUFields(t *testing.T) {
	pricing := openRouterProviderBuildPricing(nil, nil, 0.000002, 0.000006, 0, 1)
	if pricing.Image != "0" || pricing.Request != "0" {
		t.Fatalf("pricing=%+v", pricing)
	}
	if pricing.Prompt == "" || pricing.Completion == "" {
		t.Fatalf("pricing=%+v", pricing)
	}
}

func TestOpenRouterProviderBuildPricing_PeakOverrideUsesBaseOffPeak(t *testing.T) {
	group := &Group{
		PeakRateEnabled:    true,
		PeakStart:          "09:00",
		PeakEnd:            "12:00",
		PeakRateMultiplier: 2,
		SubscriptionType:   SubscriptionTypeSubscription,
	}
	pricing := openRouterProviderBuildPricing(group, nil, 0.000002, 0.000006, 0.000001, 1.5)
	if pricing.Prompt != formatOpenRouterUSDPerToken(0.000003) {
		t.Fatalf("off-peak prompt=%q", pricing.Prompt)
	}
	if len(pricing.Overrides) != 1 {
		t.Fatalf("overrides=%+v", pricing.Overrides)
	}
	if pricing.Overrides[0].Prompt != formatOpenRouterUSDPerToken(0.000006) {
		t.Fatalf("peak prompt=%q", pricing.Overrides[0].Prompt)
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

func TestOpenRouterProviderSupportedFeatures_FromCapabilities(t *testing.T) {
	meta := &PublicCatalogModel{
		Capabilities: []string{"tool_use", "reasoning", "response_schema"},
	}
	features := openRouterProviderSupportedFeatures(meta)
	if len(features) < 3 {
		t.Fatalf("features=%v", features)
	}
}

func TestOpenRouterProviderSupportedFeatures_NoCapabilitiesReturnsNil(t *testing.T) {
	if got := openRouterProviderSupportedFeatures(nil); got != nil {
		t.Fatalf("got=%v want nil", got)
	}
	if got := openRouterProviderSupportedFeatures(&PublicCatalogModel{}); got != nil {
		t.Fatalf("got=%v want nil", got)
	}
}

func TestOpenRouterProviderBuildPricing_VideoBillingUsesRequestSKU(t *testing.T) {
	meta := &PublicCatalogModel{
		Pricing: PublicCatalogPricing{
			BillingMode:         "video",
			OutputCostPerSecond: 0.05,
		},
	}
	pricing := openRouterProviderBuildPricing(nil, meta, 0, 0, 0, 1)
	want := formatOpenRouterUSDPerToken(0.05 * openRouterProviderDefaultVideoQuoteSeconds)
	if pricing.Request != want {
		t.Fatalf("request=%q want %q", pricing.Request, want)
	}
}

func TestOpenRouterProviderBuildPricing_ImageBillingUsesImageSKU(t *testing.T) {
	meta := &PublicCatalogModel{
		Pricing: PublicCatalogPricing{
			BillingMode:        "image",
			OutputCostPerImage: 0.04,
		},
	}
	pricing := openRouterProviderBuildPricing(nil, meta, 0, 0, 0, 1)
	if pricing.Image != formatOpenRouterUSDPerToken(0.04) {
		t.Fatalf("image=%q", pricing.Image)
	}
}

func TestOpenRouterProviderBuildPricing_TierOverridesPreferPeak(t *testing.T) {
	meta := &PublicCatalogModel{
		Pricing: PublicCatalogPricing{
			Tiers: []PublicCatalogTier{
				{MinTokens: 0, InputPer1KTokens: 1, OutputPer1KTokens: 2},
				{MinTokens: 200000, InputPer1KTokens: 2, OutputPer1KTokens: 4},
			},
		},
	}
	group := &Group{PeakRateEnabled: true, PeakStart: "09:00", PeakEnd: "12:00", PeakRateMultiplier: 2}
	pricing := openRouterProviderBuildPricing(group, meta, 0.000002, 0.000006, 0, 1)
	if len(pricing.Overrides) != 1 || pricing.Overrides[0].MinPromptTokens != 200000 {
		t.Fatalf("overrides=%+v", pricing.Overrides)
	}
}

func TestOpenRouterProviderInputModalities_VisionAddsImage(t *testing.T) {
	meta := &PublicCatalogModel{Capabilities: []string{"vision"}}
	got := openRouterProviderInputModalities(meta)
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

func TestOpenRouterProviderCatalogExcluded_KnownIDs(t *testing.T) {
	for _, id := range []string{"claude-fable-5", "claude-opus-4-1", "gemini-3.1-pro"} {
		if !openRouterProviderCatalogExcluded(id) {
			t.Fatalf("expected excluded: %q", id)
		}
	}
	if openRouterProviderCatalogExcluded("claude-sonnet-4-6") {
		t.Fatal("claude-sonnet-4-6 must not be excluded")
	}
}

func TestOpenRouterProviderEnrichCatalogItem_StreamOnlyMetadata(t *testing.T) {
	item := OpenRouterProviderModel{
		ID:          "tokenkey/glm-4.5",
		Description: "glm via TokenKey",
		OpenRouter:  map[string]string{"slug": "tokenkey/glm-4.5"},
	}
	openRouterProviderEnrichCatalogItem(&item, "glm-4.5")
	if item.OpenRouter["stream_required"] != "true" {
		t.Fatalf("openrouter=%+v", item.OpenRouter)
	}
	if !strings.Contains(item.Description, "stream=true") {
		t.Fatalf("description=%q", item.Description)
	}
}

func TestOpenRouterProviderEnrichCatalogItem_NonStreamUnchanged(t *testing.T) {
	item := OpenRouterProviderModel{Description: "plain"}
	openRouterProviderEnrichCatalogItem(&item, "qwen-max")
	if item.OpenRouter != nil {
		t.Fatalf("openrouter=%+v", item.OpenRouter)
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
