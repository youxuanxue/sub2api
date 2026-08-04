//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func overlayVideoUSD(t *testing.T, model, resolution string, opts *VideoBillingOptions) float64 {
	t.Helper()
	p, ok := tkOverlayVideoUnitPriceUSD(model, resolution, opts)
	require.True(t, ok, "overlay must define %s @ %s", model, resolution)
	return p
}

func TestTkOverlayVideoUnitPriceUSD_SeedanceTierOrdering(t *testing.T) {
	model := "doubao-seedance-2-0-260128"
	p480 := overlayVideoUSD(t, model, VideoBillingResolution480P, nil)
	p1080 := overlayVideoUSD(t, model, VideoBillingResolution1080P, nil)
	p4k := overlayVideoUSD(t, model, VideoBillingResolution4K, nil)
	require.Less(t, p480, p1080)
	require.Less(t, p1080, p4k)
}

func TestTkOverlayVideoUnitPriceUSD_Seedance15ProSilentHalfPrice(t *testing.T) {
	model := "doubao-seedance-1-5-pro-251215"
	withAudio := overlayVideoUSD(t, model, VideoBillingResolution1080P, nil)
	silent := false
	silentPrice := overlayVideoUSD(t, model, VideoBillingResolution1080P, &VideoBillingOptions{GenerateAudio: &silent})
	require.InDelta(t, withAudio/2, silentPrice, 1e-6)
}

func TestTkOverlayVideoUnitPriceUSD_VeoAudioMatrix(t *testing.T) {
	model := "veo-3.1-generate-001"
	withAudio := overlayVideoUSD(t, model, VideoBillingResolution720P, nil)
	silent := false
	silentPrice := overlayVideoUSD(t, model, VideoBillingResolution720P, &VideoBillingOptions{GenerateAudio: &silent})
	require.Greater(t, withAudio, silentPrice)
	require.InDelta(t, withAudio/2, silentPrice, 1e-9)
}

func TestTkOverlayVideoUnitPriceUSD_GrokImageInputSurcharge(t *testing.T) {
	model := "grok-imagine-video"
	text := overlayVideoUSD(t, model, VideoBillingResolution720P, nil)
	image := overlayVideoUSD(t, model, VideoBillingResolution720P, &VideoBillingOptions{HasInputImage: true})
	require.Greater(t, image, text)
	raw := tkOverlayRawVideoEntry(model)
	require.NotNil(t, raw)
	tier := tkOverlayVideoTierForResolution(
		tkPresentLiteLLMModelPricingFromSnapshot(raw, loadTKPricingOverlaySnapshot()),
		VideoBillingResolution720P,
	)
	require.NotNil(t, tier)
	require.InDelta(t, tier.InputImageSurchargePerSecond, image-text, 1e-9)
}

func TestCalculateVideoCost_Seedance480pCheaperThan1080p(t *testing.T) {
	svc := newTestBillingService()
	p480 := svc.CalculateVideoCost("doubao-seedance-2-0-260128", VideoBillingResolution480P, 1, 5, nil, 1.0, nil)
	p1080 := svc.CalculateVideoCost("doubao-seedance-2-0-260128", VideoBillingResolution1080P, 1, 5, nil, 1.0, nil)
	require.Less(t, p480.TotalCost, p1080.TotalCost)
}

func TestVideoSubmitBillingParamsFromBody(t *testing.T) {
	body := []byte(`{"model":"veo-3.1-generate-001","prompt":"test","resolution":"720p","metadata":{"generate_audio":false},"image":"data:image/png;base64,abc"}`)
	p := VideoSubmitBillingParamsFromBody(body)
	require.Equal(t, "720p", p.Resolution)
	require.NotNil(t, p.GenerateAudio)
	require.False(t, *p.GenerateAudio)
	require.True(t, p.HasInputImage)
}

func TestVideoSubmitBillingParamsFromBody_DoesNotCoerceMalformedAudio(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"generateAudio":"false"}`),
		[]byte(`{"metadata":{"generate_audio":{"value":false}}}`),
		[]byte(`{"metadata":"{\"generateAudio\":\"false\"}"}`),
	} {
		p := VideoSubmitBillingParamsFromBody(body)
		require.Nil(t, p.GenerateAudio, string(body))
	}
}

func TestVideoSubmitBillingParamsFromBody_TopLevelAudioWinsOverMetadata(t *testing.T) {
	p := VideoSubmitBillingParamsFromBody([]byte(`{
		"generateAudio": false,
		"metadata": {"generateAudio": true}
	}`))
	require.NotNil(t, p.GenerateAudio)
	require.False(t, *p.GenerateAudio)
}

func TestVideoSubmitBillingParamsFromBody_NormalizesTaskSizeForBilling(t *testing.T) {
	p := VideoSubmitBillingParamsFromBody([]byte(`{
		"model": "veo-3.1-generate-001",
		"size": "1920x1080"
	}`))
	require.Equal(t, VideoBillingResolution1080P, NormalizeVideoBillingResolutionOrDefault(p.Resolution))
}

func TestTkVideoModelUnpriced_Grok15HasTierPrice(t *testing.T) {
	svc := newTestBillingService()
	require.False(t, svc.TkVideoModelUnpriced("grok-imagine-video-1.5"))
}

func TestTKPricingOverlay_VideoTierModelsPresent(t *testing.T) {
	for _, model := range []string{
		"doubao-seedance-2-0-260128",
		"veo-3.1-generate-001",
		"grok-imagine-video",
		"grok-imagine-video-1.5",
	} {
		raw := tkOverlayRawVideoEntry(model)
		require.NotNil(t, raw, model)
		require.NotEmpty(t, raw.VideoPriceTiers, model)
	}
}

func TestUS043_OverlayVideoPricingUsesOneSnapshotForTiersAndTax(t *testing.T) {
	videoRow := func() *LiteLLMModelPricing {
		return &LiteLLMModelPricing{
			LiteLLMProvider: "volcengine",
			Mode:            "video_generation",
			VideoPriceTiers: []PricingVideoTier{{
				Resolution:      VideoBillingResolution720P,
				PerSecond:       0.1,
				DefaultForModel: true,
			}},
		}
	}
	policy := func(multiplier float64) tkOfficialListBaseTaxPolicy {
		return tkOfficialListBaseTaxPolicy{
			Multiplier: multiplier,
			Rules: []tkOfficialListBaseTaxRule{{
				Provider:      "volcengine",
				ModelPrefixes: []string{"video"},
			}},
		}
	}
	oldSnapshot := &tkPricingOverlaySnapshot{
		Models:  map[string]*LiteLLMModelPricing{"video": videoRow()},
		BaseTax: policy(2),
	}
	newSnapshot := &tkPricingOverlaySnapshot{
		Models:  map[string]*LiteLLMModelPricing{"video": videoRow()},
		BaseTax: policy(1.5),
	}

	tkOverlayMu.Lock()
	previous := tkOverlayEffective
	tkOverlayEffective = newSnapshot
	tkOverlayMu.Unlock()
	t.Cleanup(func() {
		tkOverlayMu.Lock()
		tkOverlayEffective = previous
		tkOverlayMu.Unlock()
	})

	priced := tkOverlayVideoPricingFromSnapshot(oldSnapshot, "video")
	require.NotNil(t, priced)
	require.Len(t, priced.VideoPriceTiers, 1)
	require.InDelta(t, 0.2, priced.VideoPriceTiers[0].PerSecond, 1e-15)
}

func TestUS043_OverlayVideoUnitPriceUsesSnapshotDefaultResolution(t *testing.T) {
	oldSnapshot := &tkPricingOverlaySnapshot{
		Models: map[string]*LiteLLMModelPricing{
			"video": {
				LiteLLMProvider:        "openai",
				Mode:                   "video_generation",
				DefaultVideoResolution: VideoBillingResolution720P,
				VideoPriceTiers: []PricingVideoTier{
					{Resolution: VideoBillingResolution720P, PerSecond: 0.1, DefaultForModel: true},
					{Resolution: VideoBillingResolution1080P, PerSecond: 0.4},
				},
			},
		},
	}
	newSnapshot := &tkPricingOverlaySnapshot{
		Models: map[string]*LiteLLMModelPricing{
			"video": {
				LiteLLMProvider:        "openai",
				Mode:                   "video_generation",
				DefaultVideoResolution: VideoBillingResolution1080P,
				VideoPriceTiers: []PricingVideoTier{
					{Resolution: VideoBillingResolution720P, PerSecond: 0.2},
					{Resolution: VideoBillingResolution1080P, PerSecond: 0.8, DefaultForModel: true},
				},
			},
		},
	}

	tkOverlayMu.Lock()
	previous := tkOverlayEffective
	tkOverlayEffective = newSnapshot
	tkOverlayMu.Unlock()
	t.Cleanup(func() {
		tkOverlayMu.Lock()
		tkOverlayEffective = previous
		tkOverlayMu.Unlock()
	})

	price, ok := tkOverlayVideoUnitPriceUSDFromSnapshot(oldSnapshot, "video", "", nil)
	require.True(t, ok)
	require.InDelta(t, 0.1, price, 1e-15)
}
