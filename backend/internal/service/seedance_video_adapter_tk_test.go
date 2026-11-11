//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relay/channel/task/doubao"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
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
	for _, metadata := range []string{`[]`, `"text"`, `42`, `true`} {
		t.Run(metadata, func(t *testing.T) {
			_, err := PrepareSeedanceVideoRequest([]byte(`{"model":"m","content":[{"type":"text","text":"a"}],"metadata":` + metadata + `}`))
			require.ErrorContains(t, err, "metadata must be an object")
		})
	}
}

func TestSeedanceVideoRequestMetadataCannotReplaceValidatedContent(t *testing.T) {
	for _, image := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			metadata string
		}{
			{"conflicting", `{"content":[{"type":"image_url","role":"last_frame","image_url":{"url":"https://example.com/unchecked.png"}},{"type":"audio_url","audio_url":{"url":"https://example.com/unchecked.wav"}}]}`},
			{"null", `null`},
		} {
			t.Run(fmt.Sprintf("image=%t/metadata=%s", image, tc.name), func(t *testing.T) {
				content := `[{"type":"text","text":"red ball"}]`
				if image {
					content = `[{"type":"text","text":"red ball"},{"type":"image_url","role":"first_frame","image_url":{"url":"https://example.com/approved.png"}}]`
				}
				body, err := PrepareSeedanceVideoRequest([]byte(`{"model":"m","content":` + content + `,"metadata":` + tc.metadata + `}`))
				require.NoError(t, err)
				var request relaycommon.TaskSubmitReq
				require.NoError(t, json.Unmarshal(body, &request))
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Set("task_request", request)
				adaptor := &doubao.TaskAdaptor{}
				reader, err := adaptor.BuildRequestBody(c, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}})
				require.NoError(t, err)
				wire, err := io.ReadAll(reader)
				require.NoError(t, err)
				parts := gjson.GetBytes(wire, "content").Array()
				if image {
					require.Len(t, parts, 2)
					require.Equal(t, "image_url", parts[0].Get("type").String())
					require.Equal(t, "first_frame", parts[0].Get("role").String())
					require.Equal(t, "https://example.com/approved.png", parts[0].Get("image_url.url").String())
				} else {
					require.Len(t, parts, 1)
				}
				require.Equal(t, "text", parts[len(parts)-1].Get("type").String())
				require.Equal(t, "red ball", parts[len(parts)-1].Get("text").String())
			})
		}
	}
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
