//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func tkMediaGuardBillingService() *BillingService {
	return &BillingService{
		pricingService: &PricingService{
			pricingData: map[string]*LiteLLMModelPricing{
				"veo-3.1-generate-001":           {OutputCostPerSecond: 0.40, Mode: "video_generation"},
				"imagen-4.0-generate-001":        {OutputCostPerImage: 0.04, Mode: "image_generation"},
				"gpt-image-token-billed":         {InputCostPerToken: 1e-6, OutputCostPerToken: 4e-5, Mode: "chat"},
				"zero-placeholder-media":         {Mode: "image_generation"}, // litellm all-zero placeholder
				"doubao-seedance-1-0-pro-250528": {OutputCostPerSecond: 0.10880597014925374, Mode: "video_generation"},
			},
		},
	}
}

func TestTkVideoModelUnpriced(t *testing.T) {
	svc := tkMediaGuardBillingService()

	require.False(t, svc.TkVideoModelUnpriced("veo-3.1-generate-001"))
	require.False(t, svc.TkVideoModelUnpriced("doubao-seedance-1-0-pro-250528"))

	// Unknown model: would bill $0/s → must be rejected pre-dispatch.
	require.True(t, svc.TkVideoModelUnpriced("brand-new-video-model"))
	// Token-priced model pointed at the VIDEO endpoint: video billing only
	// reads OutputCostPerSecond, so this would also bill $0 → unpriced here.
	require.True(t, svc.TkVideoModelUnpriced("gpt-image-token-billed"))
	// Zero placeholder row → unpriced.
	require.True(t, svc.TkVideoModelUnpriced("zero-placeholder-media"))

	// Fail OPEN on missing wiring: a nil pricing service must not block.
	require.False(t, (&BillingService{}).TkVideoModelUnpriced("anything"))
	var nilSvc *BillingService
	require.False(t, nilSvc.TkVideoModelUnpriced("anything"))
}

func TestTkImageModelUnpriced(t *testing.T) {
	svc := tkMediaGuardBillingService()

	require.False(t, svc.TkImageModelUnpriced("imagen-4.0-generate-001", nil))
	// gpt-image-style models bill by tokens — token prices count as priced.
	require.False(t, svc.TkImageModelUnpriced("gpt-image-token-billed", nil))

	// Truly priceless / zero placeholder → rejected (this replaces the blind
	// $0.134 hardcoded fallback for models nobody priced).
	require.True(t, svc.TkImageModelUnpriced("never-priced-image-model", nil))
	require.True(t, svc.TkImageModelUnpriced("zero-placeholder-media", nil))

	// Group-level size prices are a legitimate sole price source.
	price := 0.05
	group := &Group{ImagePrice2K: &price}
	require.False(t, svc.TkImageModelUnpriced("never-priced-image-model", group))

	// Model-less requests (OAuth path defaults the model downstream) fail OPEN.
	require.False(t, svc.TkImageModelUnpriced("", nil))
	require.False(t, svc.TkImageModelUnpriced("   ", nil))

	// Fail OPEN on missing wiring.
	require.False(t, (&BillingService{}).TkImageModelUnpriced("anything", nil))
}

func TestTkUnpricedMediaModelMessage(t *testing.T) {
	msg := TkUnpricedMediaModelMessage("some-model", "video")
	require.True(t, strings.Contains(msg, `"some-model"`))
	require.True(t, strings.Contains(msg, "video generation price"))
	require.True(t, strings.Contains(msg, "operator"))
}
