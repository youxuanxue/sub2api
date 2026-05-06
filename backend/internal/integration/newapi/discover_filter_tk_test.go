//go:build unit

package newapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubPricing implements PricingCatalogLookup. priced[modelID] = true →
// IsModelPriced returns true.
type stubPricing struct {
	priced map[string]bool
}

func (s *stubPricing) IsModelPriced(modelID, _ string) bool {
	return s != nil && s.priced[modelID]
}

// stubAvailability implements AvailabilityLookup. unreachable[platform+"::"+modelID] = true.
type stubAvailability struct {
	unreachable map[string]bool
}

func (s *stubAvailability) IsUnreachable(_ context.Context, platform, modelID string) bool {
	return s != nil && s.unreachable[platform+"::"+modelID]
}

func TestDiscoveryFilterApply_TagsPricingStatus(t *testing.T) {
	pricing := &stubPricing{priced: map[string]bool{
		"gpt-4o":     true,
		"o1-preview": true,
	}}
	avail := &stubAvailability{} // nothing unreachable
	f := NewDiscoveryFilter(pricing, avail)

	out := f.Apply(context.Background(), "openai", []rawDiscoveredModel{
		{ID: "gpt-4o"},
		{ID: "gpt-5-experimental"}, // not priced
		{ID: "o1-preview"},
	})

	require.Equal(t, []DiscoveredModel{
		{ID: "gpt-4o", PricingStatus: PricingStatusPriced},
		{ID: "gpt-5-experimental", PricingStatus: PricingStatusMissing},
		{ID: "o1-preview", PricingStatus: PricingStatusPriced},
	}, out)
}

func TestDiscoveryFilterApply_DropsProviderUnavailable(t *testing.T) {
	pricing := &stubPricing{priced: map[string]bool{"text-embedding-3-large": true}}
	avail := &stubAvailability{}
	f := NewDiscoveryFilter(pricing, avail)

	// Gemini-style: embedding-only model has ProviderUnavailable=true even
	// though it's priced. Filter MUST drop it before tagging.
	out := f.Apply(context.Background(), "gemini", []rawDiscoveredModel{
		{ID: "text-embedding-3-large", ProviderUnavailable: true},
		{ID: "gemini-2.5-flash", ProviderUnavailable: false},
	})

	// Embedding model dropped; remaining gemini-2.5-flash tagged missing
	// (not in stub pricing).
	require.Equal(t, []DiscoveredModel{
		{ID: "gemini-2.5-flash", PricingStatus: PricingStatusMissing},
	}, out)
}

func TestDiscoveryFilterApply_DropsUnreachableFromAvailabilityTable(t *testing.T) {
	pricing := &stubPricing{priced: map[string]bool{
		"gpt-4o": true, "gpt-3.5-turbo": true,
	}}
	avail := &stubAvailability{unreachable: map[string]bool{
		"openai::gpt-3.5-turbo": true, // recently observed unreachable
	}}
	f := NewDiscoveryFilter(pricing, avail)

	out := f.Apply(context.Background(), "openai", []rawDiscoveredModel{
		{ID: "gpt-4o"},
		{ID: "gpt-3.5-turbo"},
	})

	require.Equal(t, []DiscoveredModel{
		{ID: "gpt-4o", PricingStatus: PricingStatusPriced},
	}, out)
}

func TestDiscoveryFilterApply_NilDepsAreFailOpen(t *testing.T) {
	// nil DiscoveryFilter (extreme degraded path) → tags everything priced.
	out := (*DiscoveryFilter)(nil).Apply(context.Background(), "openai", []rawDiscoveredModel{
		{ID: "gpt-4o"},
	})
	require.Equal(t, []DiscoveredModel{{ID: "gpt-4o", PricingStatus: PricingStatusPriced}}, out)

	// nil pricing + nil availability inside a DiscoveryFilter → step [2]
	// no-op, step [3] tags everything as missing (no priced lookup).
	f := NewDiscoveryFilter(nil, nil)
	out = f.Apply(context.Background(), "openai", []rawDiscoveredModel{
		{ID: "gpt-4o"},
		{ID: "gpt-3.5-turbo", ProviderUnavailable: true}, // still dropped at [1]
	})
	require.Equal(t, []DiscoveredModel{{ID: "gpt-4o", PricingStatus: PricingStatusMissing}}, out)
}

func TestDiscoveryFilterApply_TrimsAndDropsEmptyIDs(t *testing.T) {
	f := NewDiscoveryFilter(&stubPricing{priced: map[string]bool{"gpt-4o": true}}, &stubAvailability{})

	out := f.Apply(context.Background(), "openai", []rawDiscoveredModel{
		{ID: ""},          // dropped
		{ID: "  gpt-4o  "}, // trimmed → priced
		{ID: "   "},       // dropped (trims to empty)
	})
	require.Equal(t, []DiscoveredModel{{ID: "gpt-4o", PricingStatus: PricingStatusPriced}}, out)
}

func TestGeminiSupportsGenerateContent(t *testing.T) {
	cases := []struct {
		methods []string
		want    bool
	}{
		// Empty list = absent field → defensive true (don't strip).
		{[]string{}, true},
		// generateContent present → true.
		{[]string{"generateContent", "countTokens"}, true},
		// case-insensitive.
		{[]string{"GenerateContent"}, true},
		// embedding-only → false (this is the gemini-pa real-world failure case).
		{[]string{"embedContent"}, false},
		// Other generative method without generateContent → false.
		{[]string{"streamGenerateContent"}, false},
	}
	for _, tc := range cases {
		got := geminiSupportsGenerateContent(tc.methods)
		require.Equal(t, tc.want, got, "methods=%v", tc.methods)
	}
}
