package service

import (
	"strings"
	"testing"
)

func TestParseOpenRouterProviderConfig_Defaults(t *testing.T) {
	cfg, err := ParseOpenRouterProviderConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelIDPrefix != "tokenkey/" {
		t.Fatalf("prefix = %q", cfg.ModelIDPrefix)
	}
	if cfg.Slug != "tokenkey" {
		t.Fatalf("slug = %q", cfg.Slug)
	}
	if cfg.PrivacyPolicyURL == "" || cfg.TermsOfServiceURL == "" {
		t.Fatalf("legal urls missing: privacy=%q terms=%q", cfg.PrivacyPolicyURL, cfg.TermsOfServiceURL)
	}
	if !cfg.CatalogExcluded("claude-fable-5") || !cfg.CatalogExcluded("gemini-3.1-pro") {
		t.Fatalf("default catalog excludes missing: %v", cfg.CatalogExcludedModelIDs)
	}
	if !cfg.StreamOnly("glm-4.5") || !cfg.StreamOnly("glm-4.5-air") {
		t.Fatalf("default stream-only missing: %v", cfg.StreamOnlyModelIDs)
	}
}

func TestParseOpenRouterProviderConfig_ExplicitEmptyExcludeList(t *testing.T) {
	raw := `{"catalog_excluded_model_ids":[],"stream_only_model_ids":[]}`
	cfg, err := ParseOpenRouterProviderConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CatalogExcludedModelIDs) != 0 || cfg.CatalogExcluded("claude-fable-5") {
		t.Fatalf("explicit empty exclude must clear defaults: %v", cfg.CatalogExcludedModelIDs)
	}
	if len(cfg.StreamOnlyModelIDs) != 0 || cfg.StreamOnly("glm-4.5") {
		t.Fatalf("explicit empty stream-only must clear defaults: %v", cfg.StreamOnlyModelIDs)
	}
}

func TestOpenRouterProviderConfig_CatalogExcludedAndStreamOnly(t *testing.T) {
	cfg := OpenRouterProviderConfig{
		CatalogExcludedModelIDs: []string{"claude-fable-5", "gemini-3.1-pro"},
		StreamOnlyModelIDs:      []string{"glm-4.5"},
	}
	if !cfg.CatalogExcluded("claude-fable-5") || cfg.CatalogExcluded("claude-sonnet-4-6") {
		t.Fatal("catalog exclude mismatch")
	}
	if !cfg.StreamOnly("glm-4.5") || cfg.StreamOnly("qwen-max") {
		t.Fatal("stream-only mismatch")
	}
}

func TestOpenRouterProviderEnrichCatalogItem_StreamOnlyMetadata(t *testing.T) {
	cfg := DefaultOpenRouterProviderConfig()
	item := OpenRouterProviderModel{
		ID:          "tokenkey/glm-4.5",
		Description: "glm via TokenKey",
		OpenRouter:  map[string]string{"slug": "tokenkey/glm-4.5"},
	}
	openRouterProviderEnrichCatalogItem(&item, cfg, "glm-4.5")
	if item.OpenRouter["stream_required"] != "true" {
		t.Fatalf("openrouter=%+v", item.OpenRouter)
	}
	if !strings.Contains(item.Description, "stream=true") {
		t.Fatalf("description=%q", item.Description)
	}
}

func TestOpenRouterProviderEnrichCatalogItem_NonStreamUnchanged(t *testing.T) {
	cfg := DefaultOpenRouterProviderConfig()
	item := OpenRouterProviderModel{Description: "plain"}
	openRouterProviderEnrichCatalogItem(&item, cfg, "qwen-max")
	if item.OpenRouter != nil {
		t.Fatalf("openrouter=%+v", item.OpenRouter)
	}
}

func TestOpenRouterProviderConfig_CatalogAndInferenceURLs(t *testing.T) {
	cfg := DefaultOpenRouterProviderConfig()
	base := "https://api.tokenkey.dev"
	if got := cfg.CatalogBaseURL(base); got != base+"/openrouter/v1/models" {
		t.Fatalf("catalog url=%q", got)
	}
	if got := cfg.InferenceBaseURL(base); got != base+"/v1/chat/completions" {
		t.Fatalf("inference url=%q", got)
	}
	if got := cfg.ImagesBaseURL(base); got != base+"/openrouter/v1/images" {
		t.Fatalf("images url=%q", got)
	}
	if got := cfg.VideosBaseURL(base); got != base+"/openrouter/v1/videos" {
		t.Fatalf("videos url=%q", got)
	}
}

func TestOpenRouterProviderConfig_AllowsAPIKey(t *testing.T) {
	cfg := OpenRouterProviderConfig{
		Enabled:          true,
		AllowedAPIKeyIDs: []int64{42},
		BillingUserID:    7,
	}
	if !cfg.AllowsAPIKey(42, 99) {
		t.Fatal("allowed api key id must pass")
	}
	if cfg.AllowsAPIKey(43, 7) {
		t.Fatal("non-allowlisted key must fail when allowlist present")
	}

	cfg = OpenRouterProviderConfig{Enabled: true, BillingUserID: 7}
	if !cfg.AllowsAPIKey(100, 7) {
		t.Fatal("billing user keys must pass when allowlist empty")
	}
	if cfg.AllowsAPIKey(100, 8) {
		t.Fatal("other user keys must fail")
	}
}

func TestOpenRouterProviderConfig_PublicModelID(t *testing.T) {
	cfg := DefaultOpenRouterProviderConfig()
	if got := cfg.PublicModelID("deepseek-v4-pro"); got != "tokenkey/deepseek-v4-pro" {
		t.Fatalf("got %q", got)
	}
	if got := cfg.PublicModelID("tokenkey/deepseek-v4-pro"); got != "tokenkey/deepseek-v4-pro" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatOpenRouterUSDPerToken(t *testing.T) {
	if formatOpenRouterUSDPerToken(0) != "0" {
		t.Fatal("zero must stringify to 0")
	}
	if formatOpenRouterUSDPerToken(0.000002) == "" {
		t.Fatal("expected non-empty price string")
	}
}
