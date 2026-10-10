package bridge

import (
	"io"
	"net/http/httptest"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestTaskAdaptorForChannel_SelectsAliCompanion(t *testing.T) {
	t.Parallel()
	a := taskAdaptorForChannel(newapiconstant.ChannelTypeAli, "https://token-plan.cn-beijing.maas.aliyuncs.com")
	_, ok := a.(*aliVideoTaskAdaptor)
	require.True(t, ok, "ch17 must use TK Ali video companion for HappyHorse dialect")
}

func TestBuildHappyHorseAliRequest_T2V(t *testing.T) {
	t.Parallel()
	req := relaycommon.TaskSubmitReq{
		Model:  "happyhorse-1.1-t2v",
		Prompt: "a cardboard city at night",
		Size:   "720p",
	}
	got, err := buildHappyHorseAliRequest("happyhorse-1.1-t2v", req)
	require.NoError(t, err)
	require.Equal(t, "happyhorse-1.1-t2v", got.Model)
	require.Equal(t, "a cardboard city at night", got.Input.Prompt)
	require.Empty(t, got.Input.Media)
	require.Equal(t, "720P", got.Parameters.Resolution)
	require.Equal(t, "16:9", got.Parameters.Ratio)
	require.Equal(t, 5, got.Parameters.Duration)
	require.NotNil(t, got.Parameters.Watermark)
	require.False(t, *got.Parameters.Watermark)
}

func TestBuildHappyHorseAliRequest_I2VRequiresOneImage(t *testing.T) {
	t.Parallel()
	_, err := buildHappyHorseAliRequest("happyhorse-1.1-i2v", relaycommon.TaskSubmitReq{
		Model:  "happyhorse-1.1-i2v",
		Prompt: "cat runs",
	})
	require.Error(t, err)

	got, err := buildHappyHorseAliRequest("happyhorse-1.1-i2v", relaycommon.TaskSubmitReq{
		Model:    "happyhorse-1.1-i2v",
		Prompt:   "cat runs",
		Images:   []string{"https://example.com/first.png"},
		Duration: 3,
	})
	require.NoError(t, err)
	require.Len(t, got.Input.Media, 1)
	require.Equal(t, "first_frame", got.Input.Media[0].Type)
	require.Equal(t, "https://example.com/first.png", got.Input.Media[0].URL)
	require.Equal(t, 3, got.Parameters.Duration)
	require.Equal(t, "1080P", got.Parameters.Resolution)
}

func TestBuildHappyHorseAliRequest_R2VReferenceImages(t *testing.T) {
	t.Parallel()
	got, err := buildHappyHorseAliRequest("happyhorse-1.1-r2v", relaycommon.TaskSubmitReq{
		Model:  "happyhorse-1.1-r2v",
		Prompt: "[Image 1] walks with [Image 2]",
		Images: []string{
			"https://example.com/a.png",
			"https://example.com/b.png",
		},
		Metadata: map[string]any{"ratio": "9:16", "resolution": "480p"},
	})
	require.NoError(t, err)
	require.Len(t, got.Input.Media, 2)
	require.Equal(t, "reference_image", got.Input.Media[0].Type)
	require.Equal(t, "reference_image", got.Input.Media[1].Type)
	require.Equal(t, "9:16", got.Parameters.Ratio)
	require.Equal(t, "480P", got.Parameters.Resolution)
}

func TestBuildHappyHorseAliRequest_RejectsBadDuration(t *testing.T) {
	t.Parallel()
	_, err := buildHappyHorseAliRequest("happyhorse-1.1-t2v", relaycommon.TaskSubmitReq{
		Model:    "happyhorse-1.1-t2v",
		Prompt:   "x",
		Duration: 2,
	})
	require.Error(t, err)
	_, err = buildHappyHorseAliRequest("happyhorse-1.1-t2v", relaycommon.TaskSubmitReq{
		Model:    "happyhorse-1.1-t2v",
		Prompt:   "x",
		Duration: 16,
	})
	require.Error(t, err)
}

func TestAliVideoTaskAdaptor_BuildRequestBody_HappyHorseT2V(t *testing.T) {
	t.Parallel()
	ginCtx := newVideoTaskGinContext(t, relaycommon.TaskSubmitReq{
		Model:    "happyhorse-1.1-t2v",
		Prompt:   "waves on a shore",
		Size:     "720p",
		Duration: 5,
	})
	a := newAliVideoTaskAdaptor()
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "happyhorse-1.1-t2v"},
	}
	reader, err := a.BuildRequestBody(ginCtx, info)
	require.NoError(t, err)
	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "happyhorse-1.1-t2v", gjson.GetBytes(body, "model").String())
	require.Equal(t, "waves on a shore", gjson.GetBytes(body, "input.prompt").String())
	require.False(t, gjson.GetBytes(body, "input.media").Exists())
	require.Equal(t, "720P", gjson.GetBytes(body, "parameters.resolution").String())
	require.Equal(t, "16:9", gjson.GetBytes(body, "parameters.ratio").String())
	require.Equal(t, int64(5), gjson.GetBytes(body, "parameters.duration").Int())
	require.False(t, gjson.GetBytes(body, "input.img_url").Exists(), "must not use Wan img_url dialect")
}

func TestAliVideoTaskAdaptor_BuildRequestBody_DelegatesWan(t *testing.T) {
	t.Parallel()
	// Wan path still goes through embedded adaptor; only assert we do not
	// force HappyHorse media[] onto a wan model id.
	ginCtx := newVideoTaskGinContext(t, relaycommon.TaskSubmitReq{
		Model:          "wan2.5-i2v-preview",
		Prompt:         "run",
		InputReference: "https://example.com/frame.png",
		Size:           "720P",
		Duration:       5,
	})
	a := newAliVideoTaskAdaptor()
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "wan2.5-i2v-preview"},
	}
	reader, err := a.BuildRequestBody(ginCtx, info)
	require.NoError(t, err)
	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "wan2.5-i2v-preview", gjson.GetBytes(body, "model").String())
	require.False(t, gjson.GetBytes(body, "input.media").Exists())
	require.Equal(t, "https://example.com/frame.png", gjson.GetBytes(body, "input.img_url").String())
}

func newVideoTaskGinContext(t *testing.T, req relaycommon.TaskSubmitReq) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("task_request", req)
	return c
}
