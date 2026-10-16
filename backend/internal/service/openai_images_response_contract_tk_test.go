//go:build unit

package service

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestTruncateOpenAIImagesResults_PostCollectionCap(t *testing.T) {
	results := []openAIResponsesImageResult{
		{Result: "a", RevisedPrompt: "first"},
		{Result: "b", RevisedPrompt: "redo"},
		{Result: "c"},
	}
	got := truncateOpenAIImagesResults(results, 1)
	require.Len(t, got, 1)
	require.Equal(t, "a", got[0].Result)

	require.Len(t, truncateOpenAIImagesResults(results, 2), 2)
	require.Len(t, truncateOpenAIImagesResults(results[:1], 4), 1)
	require.Len(t, truncateOpenAIImagesResults(nil, 1), 0)
}

func TestPrepareOpenAIImagesAPIResponseResults_EchoStripAndTruncate(t *testing.T) {
	parsed := &OpenAIImagesRequest{
		Model:        "gpt-image-2",
		Quality:      "low",
		Background:   "opaque",
		OutputFormat: "png",
		Size:         "1024x1024",
		N:            1,
	}
	results := []openAIResponsesImageResult{
		{Result: "one", Quality: "auto", Model: "gpt-image-2-codex", RevisedPrompt: "rewrite me", Size: "1254x1254"},
		{Result: "two", Quality: "auto", Model: "gpt-image-2-codex", RevisedPrompt: "redo"},
	}
	meta := openAIResponsesImageResult{Quality: "auto", Model: "gpt-image-2-codex", Size: "1254x1254"}
	got := prepareOpenAIImagesAPIResponseResults(results, &meta, parsed, "gpt-image-2")
	require.Len(t, got, 1)
	require.Equal(t, "one", got[0].Result)
	require.Empty(t, got[0].RevisedPrompt)
	require.Equal(t, "low", meta.Quality)
	require.Equal(t, "gpt-image-2", meta.Model)
	require.Equal(t, "opaque", meta.Background)
	require.Equal(t, "png", meta.OutputFormat)
	require.Equal(t, "1024x1024", meta.Size)

	body, err := buildOpenAIImagesAPIResponse(got, 1, nil, meta, "b64_json")
	require.NoError(t, err)
	require.Equal(t, "low", gjson.GetBytes(body, "quality").String())
	require.Equal(t, "gpt-image-2", gjson.GetBytes(body, "model").String())
	require.Equal(t, "1024x1024", gjson.GetBytes(body, "size").String())
	require.False(t, gjson.GetBytes(body, "data.0.revised_prompt").Exists())
	require.Equal(t, float64(1), gjson.GetBytes(body, "data.#").Float())
}

func TestValidateOpenAIImagesInputFidelity_RejectsGPTImage2(t *testing.T) {
	err := validateOpenAIImagesInputFidelity(&OpenAIImagesRequest{
		Model:                 "gpt-image-2",
		InputFidelity:         "high",
		ExplicitInputFidelity: true,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "input_fidelity")
	require.Contains(t, err.Error(), "gpt-image-2")

	require.NoError(t, validateOpenAIImagesInputFidelity(&OpenAIImagesRequest{
		Model:                 "gpt-image-1.5",
		InputFidelity:         "high",
		ExplicitInputFidelity: true,
	}))
	require.NoError(t, validateOpenAIImagesInputFidelity(&OpenAIImagesRequest{
		Model: "gpt-image-2",
	}))
}

func TestParseOpenAIImagesRequest_RejectsGPTImage2InputFidelity(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"keep the bottle","input_fidelity":"high"}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	_, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
	require.Error(t, err)
	require.Contains(t, err.Error(), "input_fidelity")
}

func TestParseOpenAIImagesRequest_MultipartInputFidelity(t *testing.T) {
	for _, model := range []string{"gpt-image-2", "gpt-image-2-codex", "gpt-image-1.5"} {
		t.Run(model, func(t *testing.T) {
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			require.NoError(t, form.WriteField("model", model))
			require.NoError(t, form.WriteField("prompt", "keep the bottle"))
			require.NoError(t, form.WriteField("input_fidelity", "high"))
			file, err := form.CreateFormFile("image", "input.png")
			require.NoError(t, err)
			_, err = file.Write(imagePNGWithOpaqueCredentials(t))
			require.NoError(t, err)
			require.NoError(t, form.Close())
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
			c.Request.Header.Set("Content-Type", form.FormDataContentType())
			parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body.Bytes())
			if model == "gpt-image-1.5" {
				require.NoError(t, err)
				require.Equal(t, "high", parsed.InputFidelity)
			} else {
				var fidelityErr OpenAIImagesInputFidelityError
				require.ErrorAs(t, err, &fidelityErr)
			}
		})
	}
}

func TestImagesNativeResponseSanitizerPreservesOtherModelsAndPayloads(t *testing.T) {
	body := []byte(`{"data":[{"b64_json":"original-bytes","revised_prompt":"rewrite","file_id":"file-1"}],"usage":{"output_tokens":196},"task_id":"img_1"}`)
	for _, model := range []string{"gpt-image-2", "gpt-image-1.5", "dall-e-3"} {
		got := sanitizeOpenAIImagesResponseJSON(body, &OpenAIImagesRequest{Model: model})
		if model == "dall-e-3" {
			require.Equal(t, body, got)
			continue
		}
		require.False(t, gjson.GetBytes(got, "data.0.revised_prompt").Exists())
		require.Equal(t, "original-bytes", gjson.GetBytes(got, "data.0.b64_json").String())
		require.Equal(t, "file-1", gjson.GetBytes(got, "data.0.file_id").String())
		require.Equal(t, int64(196), gjson.GetBytes(got, "usage.output_tokens").Int())
		require.Equal(t, "img_1", gjson.GetBytes(got, "task_id").String())
	}
}

func TestImagesNativeStreamingResponseStripsMultilineRevisedPrompt(t *testing.T) {
	for _, model := range []string{"gpt-image-2", "dall-e-3"} {
		t.Run(model, func(t *testing.T) {
			stream := ": keepalive\r\nid: 7\r\nevent: image_generation.completed\r\ndata: {\"type\":\"image_generation.completed\",\n" +
				"data: \"b64_json\":\"b3JpZ2luYWw=\",\"revised_prompt\":\"rewrite\",\"usage\":{\"output_tokens\":196}}\r\n\r\ndata: [DONE]\n\n"
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			_, count, _, _, err := (&OpenAIGatewayService{}).handleOpenAIImagesStreamingResponse(resp, c, time.Now(), nil, &OpenAIImagesRequest{Model: model})
			require.NoError(t, err)
			require.Equal(t, 1, count)
			if model == "dall-e-3" {
				require.Equal(t, stream, recorder.Body.String())
				return
			}
			require.NotContains(t, recorder.Body.String(), "revised_prompt")
			require.Contains(t, recorder.Body.String(), ": keepalive\r\nid: 7\r\nevent: image_generation.completed\r\n")
			require.Contains(t, recorder.Body.String(), "data: [DONE]\n\n")
			forEachOpenAISSEDataPayload(recorder.Body.String(), func(payload []byte) {
				require.True(t, gjson.ValidBytes(payload))
				require.Equal(t, "b3JpZ2luYWw=", gjson.GetBytes(payload, "b64_json").String())
				require.Equal(t, int64(196), gjson.GetBytes(payload, "usage.output_tokens").Int())
			})
		})
	}
}

func TestImagesNativeStreamFrameLimitRejectsBeforeForwarding(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	cfg := &config.Config{}
	cfg.Gateway.UpstreamResponseReadMaxBytes = 32
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"b64_json\":\"" + strings.Repeat("x", 80) + "\"}\n\n"))}
	_, count, _, _, err := (&OpenAIGatewayService{cfg: cfg}).handleOpenAIImagesStreamingResponse(resp, c, time.Now(), nil, &OpenAIImagesRequest{Model: "gpt-image-2"})
	require.ErrorContains(t, err, "frame exceeds response read limit")
	require.Equal(t, 0, count)
	require.Equal(t, "", recorder.Body.String())
}
