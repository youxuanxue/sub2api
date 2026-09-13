//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Tests for pricing_catalog_membership_tk.go (IsModelPriced).
//
// These predicates are load-bearing for the upstream-discovery filter (Goal 1)
// and the client model-list filter (Goal 2). Low-severity review finding R-003
// from docs/review-20260507 requested isolation tests.

func TestIsModelPriced_NilReceiver(t *testing.T) {
	var s *PricingCatalogService
	require.False(t, s.IsModelPriced("claude-3-opus-20240229", "anthropic"),
		"nil receiver must return false (fail-open, not panic)")
}

func TestIsModelPriced_EmptyModelID(t *testing.T) {
	svc := NewPricingCatalogService(nil)
	require.False(t, svc.IsModelPriced("", "anthropic"),
		"empty modelID must return false")
	require.False(t, svc.IsModelPriced("   ", "anthropic"),
		"whitespace-only modelID must return false")
}

func TestIsModelPriced_EmbeddedRegistryWithoutConfig(t *testing.T) {
	// Embedded registry remains authoritative without a sensor config.
	svc := NewPricingCatalogService(nil)
	require.True(t, svc.IsModelPriced("claude-3-opus-20240229", "anthropic"))
}

func TestIsModelPriced_WithFixtureData(t *testing.T) {
	svc := NewPricingCatalogService(nil)

	require.True(t, svc.IsModelPriced("claude-3-opus-20240229", "anthropic"))
	require.True(t, svc.IsModelPriced("gpt-4o", "openai"))
	// Platform parameter is currently ignored (catalog is platform-agnostic v1)
	require.True(t, svc.IsModelPriced("claude-3-opus-20240229", ""))
	require.False(t, svc.IsModelPriced("gpt-5-unknown", "openai"),
		"model absent from catalog must return false")
}

// TestIsModelPriced_VendorPrefixFallback exercises the OpenRouter / Azure /
// Bedrock-style "<vendor>/<model>" naming that the LiteLLM catalog does not
// store directly. Without the fallback, every OpenRouter model id returned
// from upstream /v1/models would be tagged "missing" in the admin "fetch
// upstream models" UI.
func TestIsModelPriced_VendorPrefixFallback(t *testing.T) {
	svc := NewPricingCatalogService(nil)

	// Vendor prefix + date-suffix prefix match.
	require.True(t, svc.IsModelPriced("anthropic/claude-3-haiku", "newapi"),
		"vendor/model should match catalog id sharing the family prefix")

	// Vendor prefix + dot→dash normalization (anthropic-style versioning).
	require.True(t, svc.IsModelPriced("anthropic/claude-opus-4.5", "newapi"),
		"dotted version in vendor/model should normalize to dashes before lookup")

	// Vendor prefix + tail already matches catalog id verbatim.
	require.True(t, svc.IsModelPriced("openai/gpt-4o-mini", "newapi"),
		"vendor/tail equal to a catalog id must be priced")

	// Tail with no "/" boundary still uses literal match.
	require.True(t, svc.IsModelPriced("gpt-4o-mini", "newapi"),
		"bare id literal match should still work after fallback was added")

	// Truly absent model — vendor strip and prefix match both fail.
	require.False(t, svc.IsModelPriced("ai21/jamba-large-1.7", "newapi"),
		"model family absent from catalog must return false")
	require.False(t, svc.IsModelPriced("amazon/nova-pro-v1", "newapi"),
		"unrelated vendor namespace must not pull false positives from catalog")
}

// TestIsModelPriced_VendorPrefixDoesNotLeakAcrossFamilies guards the
// boundary semantics: tail without any "-" must NOT prefix-match a longer
// catalog id. Otherwise "openai/gpt" would be reported priced because
// "gpt-4o-mini" exists, which is wrong (the vendor namespace is not a
// concrete model id).
func TestIsModelPriced_VendorPrefixDoesNotLeakAcrossFamilies(t *testing.T) {
	svc := NewPricingCatalogService(nil)

	// Family-only tails (no "-") must not prefix-match any catalog id.
	require.False(t, svc.IsModelPriced("openai/gpt", "newapi"),
		"family-only vendor tail must not prefix-match concrete catalog ids")
	require.False(t, svc.IsModelPriced("anthropic/claude", "newapi"),
		"family-only vendor tail must not prefix-match concrete catalog ids")

	// Multi-segment vendor paths are ambiguous → no fallback.
	require.False(t, svc.IsModelPriced("foo/bar/gpt-4o-mini", "newapi"),
		"multi-slash ids must skip the vendor-prefix fallback")

	// Empty tail after stripping vendor.
	require.False(t, svc.IsModelPriced("openai/", "newapi"),
		"empty tail must not match anything")
	require.False(t, svc.IsModelPriced("/gpt-4o-mini", "newapi"),
		"empty vendor must not enable the fallback")
}

func TestIsModelPriced_RejectsUnlistedNewAPILongTail(t *testing.T) {

	svc := &PricingCatalogService{}

	require.True(t, svc.IsModelPriced("deepseek-v4-pro", "newapi"))
	require.False(t, svc.IsModelPriced("deepseek-v3-2-251201", "newapi"),
		"litellm residue must not count as priced without manifest listing")
	require.False(t, svc.IsModelPriced("glm-4-32b-0414-128k", "newapi"),
		"withdrawn GLM SKU must not count as priced without manifest listing")
}
