//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTkSeedanceVideoUnitPriceUSD_OfficialTiers(t *testing.T) {
	// doubao-seedance-2.0 @ 1080p: 48600 * 51 / 1e6 = 2.4786 CNY/s ÷ 6.7 × 1.06
	p1080, ok := tkSeedanceVideoUnitPriceUSD("doubao-seedance-2-0-260128", VideoBillingResolution1080P, true)
	require.True(t, ok)
	require.InDelta(t, 0.3920, p1080, 0.001)

	p480, ok := tkSeedanceVideoUnitPriceUSD("doubao-seedance-2-0-260128", VideoBillingResolution480P, true)
	require.True(t, ok)
	require.Less(t, p480, p1080)

	p4k, ok := tkSeedanceVideoUnitPriceUSD("doubao-seedance-2-0-260128", VideoBillingResolution4K, true)
	require.True(t, ok)
	require.Greater(t, p4k, p1080)
}

func TestTkSeedanceVideoUnitPriceUSD_15ProSilentHalfPrice(t *testing.T) {
	withAudio, ok := tkSeedanceVideoUnitPriceUSD("doubao-seedance-1-5-pro-251215", VideoBillingResolution1080P, true)
	require.True(t, ok)
	silent, ok := tkSeedanceVideoUnitPriceUSD("doubao-seedance-1-5-pro-251215", VideoBillingResolution1080P, false)
	require.True(t, ok)
	require.InDelta(t, withAudio/2, silent, 1e-6)
}

func TestTkVeoVideoUnitPriceUSD_OfficialAudioMatrix(t *testing.T) {
	withAudio, ok := tkVeoVideoUnitPriceUSD("veo-3.1-generate-001", VideoBillingResolution720P, nil)
	require.True(t, ok)
	require.InDelta(t, 0.40, withAudio, 1e-9)

	silent := false
	silentPrice, ok := tkVeoVideoUnitPriceUSD("veo-3.1-generate-001", VideoBillingResolution720P, &VideoBillingOptions{GenerateAudio: &silent})
	require.True(t, ok)
	require.InDelta(t, 0.20, silentPrice, 1e-9)
}

func TestTkGrokImagineVideoUnitPriceUSD_ImageInputSurcharge(t *testing.T) {
	text, ok := tkGrokImagineVideoUnitPriceUSD("grok-imagine-video", VideoBillingResolution720P, nil)
	require.True(t, ok)
	require.InDelta(t, 0.07, text, 1e-9)

	image, ok := tkGrokImagineVideoUnitPriceUSD("grok-imagine-video", VideoBillingResolution720P, &VideoBillingOptions{HasInputImage: true})
	require.True(t, ok)
	require.InDelta(t, 0.08, image, 1e-9)
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

func TestTkVideoModelUnpriced_Grok15HasTierPrice(t *testing.T) {
	svc := newTestBillingService()
	require.False(t, svc.TkVideoModelUnpriced("grok-imagine-video-1.5"))
}
