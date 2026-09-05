package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const openRouterProviderExampleConfigRel = "ops/pricing/examples/openrouter-provider-config.example.json"

// orConfigBoundaryNotListed is a fixed negative sample — not copied from the example owner list.
const orConfigBoundaryNotListed = "__tk_or_config_boundary_not_listed__"

func mustOpenRouterProviderExampleConfigJSON(t *testing.T) string {
	t.Helper()
	for _, p := range openRouterProviderExampleConfigPaths() {
		raw, err := os.ReadFile(p)
		if err == nil {
			return string(raw)
		}
	}
	t.Fatalf("read %s from repo (tried %v)", openRouterProviderExampleConfigRel, openRouterProviderExampleConfigPaths())
	return ""
}

func mustParseOpenRouterProviderExampleConfig(t *testing.T) OpenRouterProviderConfig {
	t.Helper()
	cfg, err := ParseOpenRouterProviderConfig(mustOpenRouterProviderExampleConfigJSON(t))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func openRouterProviderExampleConfigPaths() []string {
	return []string{
		filepath.Join("../../../", openRouterProviderExampleConfigRel),
		filepath.Join("../../..", openRouterProviderExampleConfigRel),
		filepath.Join("..", openRouterProviderExampleConfigRel),
		openRouterProviderExampleConfigRel,
	}
}

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
	if len(cfg.CatalogExcludedModelIDs) != 0 || len(cfg.StreamOnlyModelIDs) != 0 {
		t.Fatalf("empty config must not inject catalog lists: exclude=%v stream=%v",
			cfg.CatalogExcludedModelIDs, cfg.StreamOnlyModelIDs)
	}
}

func TestParseOpenRouterProviderConfig_ExampleSSOTCatalogLists(t *testing.T) {
	cfg := mustParseOpenRouterProviderExampleConfig(t)
	if len(cfg.CatalogExcludedModelIDs) == 0 {
		t.Fatal("example config must define catalog_excluded_model_ids")
	}
	if len(cfg.StreamOnlyModelIDs) == 0 {
		t.Fatal("example config must define stream_only_model_ids")
	}
	for _, id := range cfg.CatalogExcludedModelIDs {
		if !cfg.CatalogExcluded(id) {
			t.Fatalf("example exclude id not active: %q", id)
		}
	}
	for _, id := range cfg.StreamOnlyModelIDs {
		if !cfg.StreamOnly(id) {
			t.Fatalf("example stream-only id not active: %q", id)
		}
	}
	if cfg.CatalogExcluded(orConfigBoundaryNotListed) {
		t.Fatal("boundary id must not be excluded")
	}
	if cfg.StreamOnly(orConfigBoundaryNotListed) {
		t.Fatal("boundary id must not be stream-only")
	}
}

func TestParseOpenRouterProviderConfig_ExplicitEmptyExcludeList(t *testing.T) {
	raw := `{"catalog_excluded_model_ids":[],"stream_only_model_ids":[]}`
	cfg, err := ParseOpenRouterProviderConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CatalogExcludedModelIDs) != 0 {
		t.Fatalf("explicit empty exclude: %v", cfg.CatalogExcludedModelIDs)
	}
	if len(cfg.StreamOnlyModelIDs) != 0 {
		t.Fatalf("explicit empty stream-only: %v", cfg.StreamOnlyModelIDs)
	}
}

func TestOpenRouterProviderConfig_CatalogExcludedAndStreamOnly(t *testing.T) {
	cfg := mustParseOpenRouterProviderExampleConfig(t)
	if len(cfg.CatalogExcludedModelIDs) == 0 || len(cfg.StreamOnlyModelIDs) == 0 {
		t.Fatal("example config lists required")
	}
	if !cfg.CatalogExcluded(cfg.CatalogExcludedModelIDs[0]) {
		t.Fatalf("first example exclude id inactive: %q", cfg.CatalogExcludedModelIDs[0])
	}
	if cfg.CatalogExcluded(orConfigBoundaryNotListed) {
		t.Fatal("boundary id must not be excluded")
	}
	if !cfg.StreamOnly(cfg.StreamOnlyModelIDs[0]) {
		t.Fatalf("first example stream-only id inactive: %q", cfg.StreamOnlyModelIDs[0])
	}
	if cfg.StreamOnly(orConfigBoundaryNotListed) {
		t.Fatal("boundary id must not be stream-only")
	}
}

func TestOpenRouterProviderEnrichCatalogItem_StreamOnlyMetadata(t *testing.T) {
	cfg := mustParseOpenRouterProviderExampleConfig(t)
	if len(cfg.StreamOnlyModelIDs) == 0 {
		t.Fatal("example stream_only_model_ids required")
	}
	sourceID := cfg.StreamOnlyModelIDs[0]
	item := OpenRouterProviderModel{
		ID:          cfg.PublicModelID(sourceID),
		Description: "stream-only via TokenKey",
		OpenRouter:  map[string]string{"slug": cfg.PublicModelID(sourceID)},
	}
	openRouterProviderEnrichCatalogItem(&item, cfg, sourceID)
	if item.OpenRouter["stream_required"] != "true" {
		t.Fatalf("openrouter=%+v", item.OpenRouter)
	}
	if !strings.Contains(item.Description, "stream=true") {
		t.Fatalf("description=%q", item.Description)
	}
}

func TestOpenRouterProviderEnrichCatalogItem_NonStreamUnchanged(t *testing.T) {
	cfg := mustParseOpenRouterProviderExampleConfig(t)
	item := OpenRouterProviderModel{Description: "plain"}
	openRouterProviderEnrichCatalogItem(&item, cfg, orConfigBoundaryNotListed)
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
	if !cfg.AllowsAPIKey(42, 99, "") {
		t.Fatal("allowed api key id must pass")
	}
	if cfg.AllowsAPIKey(43, 7, "") {
		t.Fatal("non-allowlisted key must fail when allowlist present")
	}
	// Name SSOT must not widen to every billing-user key once id lists are empty.
	cfg = OpenRouterProviderConfig{Enabled: true, BillingUserID: 7}
	if cfg.AllowsAPIKey(100, 7, "") {
		t.Fatal("unnamed billing-user key must not pass when allowlists are empty")
	}
	if cfg.AllowsAPIKey(100, 8, OpenRouterProviderInferenceKeyName) {
		t.Fatal("named inference key on other user must fail")
	}
	if !cfg.AllowsInferenceAPIKey(100, 7, OpenRouterProviderInferenceKeyName) {
		t.Fatal("named inference key on billing user must pass")
	}
	if cfg.AllowsInferenceAPIKey(100, 7, OpenRouterProviderMonitorKeyName) {
		t.Fatal("monitor-named key must not infer even for billing user")
	}
	if cfg.AllowsInferenceAPIKey(100, 7, "debug-scratch") {
		t.Fatal("arbitrary billing-user key name must not infer")
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
