//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCeilOpenAIImagesQuantum(t *testing.T) {
	require.Equal(t, 1920, ceilOpenAIImagesQuantum(1920, 16))
	require.Equal(t, 1088, ceilOpenAIImagesQuantum(1080, 16))
	require.Equal(t, 1024, ceilOpenAIImagesQuantum(1024, 16))
}

func TestUpstreamOpenAIImagesSizeCeilsToQuantum(t *testing.T) {
	req := &OpenAIImagesRequest{ExplicitSize: true, Size: "1920x1080", Model: "gpt-image-2"}
	require.Equal(t, "1920x1088", upstreamOpenAIImagesSize(req))
	req.Size = "1024x640"
	require.Equal(t, "1024x640", upstreamOpenAIImagesSize(req))
}

func TestValidateOpenAIImagesExplicitSizeRejectsOverLimit(t *testing.T) {
	req := &OpenAIImagesRequest{ExplicitSize: true, Size: "3840x2176", Model: "gpt-image-2"}
	err := validateOpenAIImagesExplicitSize(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds max")

	ok := &OpenAIImagesRequest{ExplicitSize: true, Size: "3840x2160", Model: "gpt-image-2"}
	require.NoError(t, validateOpenAIImagesExplicitSize(ok))
}

func TestParseOpenAIImagesRequestRejectsOverLimitSize(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw","size":"3840x2176"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	_, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds max")
}

func TestApplyOpenAIImagesStrictCanvasExactPixels(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for y := 0; y < 30; y++ {
		for x := 0; x < 40; x++ {
			src.Set(x, y, color.RGBA{R: 200, G: 80, B: 40, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	require.NoError(t, png.Encode(&pngBuf, src))
	results := []openAIResponsesImageResult{{
		Result:       base64.StdEncoding.EncodeToString(pngBuf.Bytes()),
		OutputFormat: "png",
		Size:         "40x30",
	}}
	parsed := &OpenAIImagesRequest{ExplicitSize: true, Size: "64x48", Background: "opaque", OutputFormat: "png"}
	require.NoError(t, applyOpenAIImagesStrictCanvas(results, parsed))
	require.Equal(t, "64x48", results[0].Size)
	raw, err := decodeOpenAIImageB64(results[0].Result)
	require.NoError(t, err)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	require.NoError(t, err)
	require.Equal(t, 64, cfg.Width)
	require.Equal(t, 48, cfg.Height)
}

// Upstream OAuth softens tools[].size to auto and returns drifted pixels (edge-us3:
// 1024x1024 → 1254x1254). Local pad must still land on the client canvas.
func TestApplyOpenAIImagesStrictCanvasPadsUpstreamSoftSize(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 50, 50))
	for y := 0; y < 50; y++ {
		for x := 0; x < 50; x++ {
			src.Set(x, y, color.RGBA{R: 10, G: 120, B: 200, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	require.NoError(t, png.Encode(&pngBuf, src))
	results := []openAIResponsesImageResult{{
		Result:       base64.StdEncoding.EncodeToString(pngBuf.Bytes()),
		OutputFormat: "png",
		Size:         "50x50", // drifted; not the requested canvas
	}}
	parsed := &OpenAIImagesRequest{
		ExplicitSize:         true,
		Size:                 "64x64",
		ExplicitOutputFormat: true,
		OutputFormat:         "jpeg",
		Background:           "opaque",
	}
	require.NoError(t, applyOpenAIImagesClientFidelityPostprocess(results, parsed))
	require.Equal(t, "64x64", results[0].Size)
	require.Equal(t, "jpeg", results[0].OutputFormat)
	raw, err := decodeOpenAIImageB64(results[0].Result)
	require.NoError(t, err)
	require.Equal(t, "jpeg", sniffOpenAIImageFormat(raw))
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	require.NoError(t, err)
	require.Equal(t, 64, cfg.Width)
	require.Equal(t, 64, cfg.Height)
}

func TestApplyOpenAIImagesStrictCanvasSkipsWhenSizeNotExplicit(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var pngBuf bytes.Buffer
	require.NoError(t, png.Encode(&pngBuf, src))
	b64 := base64.StdEncoding.EncodeToString(pngBuf.Bytes())
	results := []openAIResponsesImageResult{{Result: b64, Size: "8x8", OutputFormat: "png"}}
	parsed := &OpenAIImagesRequest{Size: "64x64"} // ExplicitSize=false
	require.NoError(t, applyOpenAIImagesStrictCanvas(results, parsed))
	require.Equal(t, "8x8", results[0].Size)
	require.Equal(t, b64, results[0].Result)
}

func TestForwardImagesOAuthPadsUpstreamSoftSizeThroughResponses(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 800, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 800; x++ {
			src.Set(x, y, color.RGBA{R: 80, G: 40, B: 200, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	require.NoError(t, png.Encode(&pngBuf, src))
	upstreamB64 := base64.StdEncoding.EncodeToString(pngBuf.Bytes())

	sse := "" +
		"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"usage\":{\"input_tokens\":5,\"output_tokens\":20,\"output_tokens_details\":{\"image_tokens\":20}},\"tool_usage\":{\"image_gen\":{\"images\":1,\"output_tokens\":20,\"output_tokens_details\":{\"image_tokens\":20}}},\"tools\":[{\"type\":\"image_generation\",\"size\":\"auto\",\"output_format\":\"png\",\"model\":\"gpt-image-2-codex\"}],\"output\":[{\"type\":\"image_generation_call\",\"result\":\"" + upstreamB64 + "\",\"output_format\":\"png\",\"size\":\"800x600\"}]}}\n\n" +
		"data: [DONE]\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}}

	// 1024x1024 is above GPT Image 2 min pixels and already 16-aligned.
	body := []byte(`{"model":"gpt-image-2","prompt":"a small ginger cat","size":"1024x1024","output_format":"jpeg","background":"opaque","quality":"medium"}`)
	c, rec := newOpenAIImagesTestContext(t, body)
	svc := newOpenAIImagesTestService(upstream)
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.True(t, parsed.ExplicitSize)

	result, err := svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
	require.NoError(t, err)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, "/backend-api/codex/responses", result.UpstreamEndpoint)
	require.Equal(t, "1024x1024", gjson.GetBytes(upstream.lastBody, "tools.0.size").String(), "upstream tool size is client canvas (already 16-aligned)")
	// OpenAI Images JSON puts size on the response object, not each data[] item.
	require.Equal(t, "1024x1024", gjson.GetBytes(rec.Body.Bytes(), "size").String())
	require.Equal(t, "jpeg", gjson.GetBytes(rec.Body.Bytes(), "output_format").String())

	outB64 := gjson.GetBytes(rec.Body.Bytes(), "data.0.b64_json").String()
	if outB64 == "" {
		url := gjson.GetBytes(rec.Body.Bytes(), "data.0.url").String()
		require.True(t, strings.HasPrefix(url, "data:image/jpeg;base64,"))
		outB64 = strings.TrimPrefix(url, "data:image/jpeg;base64,")
	}
	raw, err := decodeOpenAIImageB64(outB64)
	require.NoError(t, err)
	require.Equal(t, "jpeg", sniffOpenAIImageFormat(raw))
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	require.NoError(t, err)
	require.Equal(t, 1024, cfg.Width)
	require.Equal(t, 1024, cfg.Height)
}

func TestApplyOpenAIImagesClientFidelityWebPAndSize(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 32, 32))
	src.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	var pngBuf bytes.Buffer
	require.NoError(t, png.Encode(&pngBuf, src))
	results := []openAIResponsesImageResult{{
		Result:       base64.StdEncoding.EncodeToString(pngBuf.Bytes()),
		OutputFormat: "png",
		Size:         "32x32",
	}}
	comp := 40
	parsed := &OpenAIImagesRequest{
		ExplicitSize:         true,
		Size:                 "48x48",
		ExplicitOutputFormat: true,
		OutputFormat:         "webp",
		OutputCompression:    &comp,
		Background:           "opaque",
	}
	require.NoError(t, applyOpenAIImagesClientFidelityPostprocess(results, parsed))
	require.Equal(t, "48x48", results[0].Size)
	require.Equal(t, "webp", results[0].OutputFormat)
	raw, err := decodeOpenAIImageB64(results[0].Result)
	require.NoError(t, err)
	require.Equal(t, "webp", sniffOpenAIImageFormat(raw))
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	require.NoError(t, err)
	require.Equal(t, 48, cfg.Width)
	require.Equal(t, 48, cfg.Height)
}

func TestOpenAIImagesJPEGQualityHintsDiffer(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			src.Set(x, y, color.RGBA{R: uint8(x * 3), G: uint8(y * 2), B: 90, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	require.NoError(t, png.Encode(&pngBuf, src))
	b64 := base64.StdEncoding.EncodeToString(pngBuf.Bytes())

	lowComp := 30
	highComp := 95
	lowB64, _, err := coerceOpenAIImageB64ToOutputFormatWithQuality(b64, "jpeg", &openAIImagesEncodeHints{OutputCompression: &lowComp})
	require.NoError(t, err)
	highB64, _, err := coerceOpenAIImageB64ToOutputFormatWithQuality(b64, "jpeg", &openAIImagesEncodeHints{OutputCompression: &highComp})
	require.NoError(t, err)
	lowRaw, _ := base64.StdEncoding.DecodeString(lowB64)
	highRaw, _ := base64.StdEncoding.DecodeString(highB64)
	require.NotEqual(t, len(lowRaw), len(highRaw))
	delta := float64(len(highRaw)-len(lowRaw)) / float64(len(highRaw))
	require.Greater(t, delta, 0.10, "compression hint should change jpeg size by >10%% (got %.2f, low=%d high=%d)", delta, len(lowRaw), len(highRaw))
}

func TestParseAllowsWebPWithoutLegacyContract(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw","output_format":"webp","tk_image_contract":"exact"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(string(body)))
	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.Equal(t, "webp", parsed.OutputFormat)
}
