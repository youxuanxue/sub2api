package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestVolcEnginePlanTTSPayloadAndValidation(t *testing.T) {
	request := []byte(`{"model":"doubao-seed-tts-2.0","input":"hello","response_format":"opus","speed":1.5,"instructions":"Speak quietly"}`)
	payload, mime, err := buildVolcEnginePlanTTSRequest(request)
	require.NoError(t, err)
	require.Equal(t, "audio/ogg", mime)
	require.Equal(t, "ogg_opus", gjson.GetBytes(payload, "req_params.audio_params.format").String())
	require.Equal(t, int64(50), gjson.GetBytes(payload, "req_params.audio_params.speech_rate").Int())
	require.Equal(t, "hello", gjson.GetBytes(payload, "req_params.text").String())
	for _, invalid := range []string{
		`{}`, `{"model":"doubao-seed-tts-2.0","input":123}`,
		`{"model":"other","input":"hi"}`,
		`{"model":"doubao-seed-tts-2.0","input":"hi","response_format":"wav"}`,
		`{"model":"doubao-seed-tts-2.0","input":"hi","speed":4}`,
		`{"model":"doubao-seed-tts-2.0","input":"hi","sample_rate":123}`,
	} {
		require.Error(t, ValidateVolcEnginePlanTTSRequest([]byte(invalid)))
	}
}

func TestVolcEnginePlanTTSRejectsPartialAudio(t *testing.T) {
	for _, body := range []string{
		`{"code":0,"data":"YWJj"}`,
		`{"code":0,"data":"YWJj"}{"code":12345}`,
		`{"code":0,"data":"!"}{"code":20000000,"usage":{"text_words":3}}`,
		`{"code":20000000,"usage":{"text_words":3}}`,
		`{"code":0,"data":"YWJj"}{"code":20000000}`,
		`{"code":0,"data":"YWJj"}{"code":20000000,"usage":{"text_words":3}}{}`,
	} {
		audio, _, err := decodeVolcEnginePlanTTSAudio([]byte(body))
		require.Error(t, err)
		require.Empty(t, audio)
	}
}

func TestVolcEnginePlanTTSForwardAndMetering(t *testing.T) {
	body := []byte(`{"model":"doubao-seed-tts-2.0","input":"hello"}`)
	response := `{"code":0,"data":"YWJj"}` + "\n" + `{"code":0,"data":"ZGVm"}` + "\n" + `{"code":20000000,"usage":{"text_words":7}}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response))}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/speech", bytes.NewReader(body))
	result, err := svc.ForwardNativeAudioSpeech(context.Background(), c, volcEnginePlanTestAccount(), body)
	require.NoError(t, err)
	require.Equal(t, "abcdef", recorder.Body.String())
	require.Equal(t, "audio/mpeg", recorder.Header().Get("Content-Type"))
	require.Equal(t, volcEnginePlanTTSURL, upstream.lastReq.URL.String())
	require.Equal(t, "seed-tts-2.0", upstream.lastReq.Header.Get("X-Api-Resource-Id"))
	require.Equal(t, "test-plan-key", upstream.lastReq.Header.Get("X-Api-Key"))
	require.Empty(t, upstream.lastReq.Header.Get("Authorization"))
	require.InDelta(t, 7.0/1_000_000, result.AudioUsage.DurationOrUnits, 1e-12)
	billing := NewBillingService(nil, nil)
	price := billing.TkRegistryTTSPricePerMillionChars(volcEnginePlanTTSModel)
	require.Positive(t, price)
	cost := billing.CalculateAudioCostForModel(volcEnginePlanTTSModel, "tts", result.AudioUsage.DurationOrUnits, nil, 1)
	require.InDelta(t, price*7.0/1_000_000, cost.ActualCost, 1e-12)
}
