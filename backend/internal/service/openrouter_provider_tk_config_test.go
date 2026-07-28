package service

import "testing"

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
