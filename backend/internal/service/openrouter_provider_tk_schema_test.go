package service

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

func TestOpenRouterProviderBuildPricing_IncludesZeroSKUFields(t *testing.T) {
	pricing := openRouterProviderBuildPricing(nil, 0.000002, 0.000006, 0, 1)
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
	pricing := openRouterProviderBuildPricing(group, 0.000002, 0.000006, 0.000001, 1.5)
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

func TestOpenRouterProviderInputModalities_VisionAddsImage(t *testing.T) {
	meta := &PublicCatalogModel{Capabilities: []string{"vision"}}
	got := openRouterProviderInputModalities(meta)
	if len(got) != 2 || got[1] != "image" {
		t.Fatalf("modalities=%v", got)
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
