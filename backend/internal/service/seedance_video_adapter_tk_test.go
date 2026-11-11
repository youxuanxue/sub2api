//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSeedanceVideoRequestPreservesBilledInputs(t *testing.T) {
	body, err := PrepareSeedanceVideoRequest([]byte(`{"model":"doubao-seedance-2-5-260628","content":[{"type":"text","text":"red ball"},{"type":"text","text":"rolling"},{"type":"image_url","role":"first_frame","image_url":{"url":"https://example.com/ball.png"}}],"duration":5,"resolution":"480p","generate_audio":false,"ratio":"16:9"}`))
	require.NoError(t, err)
	require.Equal(t, "red ball\nrolling", gjson.GetBytes(body, "prompt").String())
	require.Equal(t, "https://example.com/ball.png", gjson.GetBytes(body, "images.0").String())
	require.Equal(t, "https://example.com/ball.png", gjson.GetBytes(body, "metadata.content.2.image_url.url").String())
	require.Equal(t, "5", gjson.GetBytes(body, "seconds").String())
	require.Equal(t, int64(5), gjson.GetBytes(body, "metadata.duration").Int())
	require.Equal(t, "16:9", gjson.GetBytes(body, "metadata.ratio").String())
	require.Equal(t, "16:9", gjson.GetBytes(body, "ratio").String())
	require.False(t, gjson.GetBytes(body, "image").Exists())
	billing := VideoSubmitBillingParamsFromBody(body)
	require.True(t, billing.HasInputImage)
	require.Equal(t, "480p", billing.Resolution)
	require.NotNil(t, billing.GenerateAudio)
	require.False(t, *billing.GenerateAudio)
	require.Equal(t, int64(5), gjson.GetBytes(body, "duration").Int())
}

func TestSeedanceVideoRequestRejectsUnsupportedOrConflictingInputs(t *testing.T) {
	for _, content := range []string{
		`[{"type":"video_url","video_url":{"url":"https://example.com/a.mp4"}}]`,
		`[{"type":"audio_url","audio_url":{"url":"https://example.com/a.wav"}}]`,
		`[{"type":"image_url","role":"last_frame","image_url":{"url":"https://example.com/a.png"}}]`,
		`[{"type":"image_url","image_url":{"url":""}}]`,
	} {
		_, err := PrepareSeedanceVideoRequest([]byte(`{"model":"m","content":` + content + `}`))
		require.Error(t, err)
	}
	_, err := PrepareSeedanceVideoRequest([]byte(`{"model":"m","content":[{"type":"text","text":"a"}],"image":"https://example.com/hidden.png"}`))
	require.ErrorContains(t, err, "use content.image_url")
}

func TestSeedanceBillingOriginUsesActualAccountTariff(t *testing.T) {
	price := 1.0
	groups := []Group{{ID: 1, RateMultiplier: 1}, {ID: 2, RateMultiplier: .5, VideoPrice720P: &price}}
	bridge := &Account{ID: 118, Platform: PlatformNewAPI}
	selected, err := selectCandidateBillingOrigin(context.Background(), 7, bridge, groups, "video", ShapeSeedance, nil, nil, nil)
	require.Nil(t, selected)
	require.ErrorIs(t, err, ErrCandidatePolicyConflict)
	native := &Account{ID: 1, Platform: PlatformOpenAI}
	selected, err = selectCandidateBillingOrigin(context.Background(), 7, native, groups, "video", ShapeSeedance, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), selected.ID, "raw Ark still uses token policy and its multiplier")
}
