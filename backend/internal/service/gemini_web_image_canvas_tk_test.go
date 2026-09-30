//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func canvasTestImage(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: 220, A: 255})
		}
	}
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, img))
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestGeminiWebCanvasUsesSharedPostprocessor(t *testing.T) {
	for _, size := range []string{"1K", "2K", "4K"} {
		for _, stream := range []bool{false, true} {
			canvas := &geminiWebImageCanvas{size: size, ratio: "1:1"}
			body := geminiImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + canvasTestImage(t) + `"}}`)
			contentType := "application/json"
			if stream {
				body = "data: " + body + "\n\n"
				contentType = "text/event-stream"
			}
			response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
			require.NoError(t, canvas.apply(context.Background(), response))
			raw, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			if stream {
				raw = bytes.TrimSpace(bytes.TrimPrefix(raw, []byte("data: ")))
			}
			part := gjson.GetBytes(raw, "candidates.0.content.parts.0.inlineData")
			decoded, err := base64.StdEncoding.DecodeString(part.Get("data").String())
			require.NoError(t, err)
			img, format, err := image.Decode(bytes.NewReader(decoded))
			require.NoError(t, err)
			width, height, _ := apicompat.GeminiImageCanvas("1:1", size)
			require.Equal(t, image.Rect(0, 0, width, height), img.Bounds())
			require.Equal(t, "png", format)
			require.Equal(t, "image/png", part.Get("mimeType").String())
			require.Equal(t, color.RGBA{255, 255, 255, 255}, color.RGBAModel.Convert(img.At(0, 0)))
			require.Equal(t, color.RGBA{220, 0, 0, 255}, color.RGBAModel.Convert(img.At(width/2, height/2)))
		}
	}
}

func TestGeminiWebCanvasKeepsRelayIntentAndOtherProviders(t *testing.T) {
	body := []byte(`{"contents":[{"parts":[{"text":"cup"}]}],"generationConfig":{"imageConfig":{"aspectRatio":"21:9","imageSize":"4K"}}}`)
	for _, kind := range []string{"local", "relay", "plain", "flash"} {
		account := webCandidateAccount(kind == "relay")
		model := "gemini-web-nano-banana-pro"
		if kind == "plain" {
			delete(account.Credentials, "gemini_web")
		}
		if kind == "flash" {
			model = "gemini-web-pro-image"
		}
		upstream, canvas, err := prepareGeminiWebImageCanvas(&account, model, body)
		require.NoError(t, err)
		if kind != "local" {
			require.Nil(t, canvas)
			require.Equal(t, body, upstream)
			continue
		}
		require.Equal(t, "4K", canvas.size)
		require.Equal(t, "21:9", canvas.ratio)
		require.False(t, gjson.GetBytes(upstream, "generationConfig.imageConfig.imageSize").Exists())
		require.Equal(t, "21:9", gjson.GetBytes(upstream, "generationConfig.imageConfig.aspectRatio").String())
	}
}

func TestGeminiWebCanvasForwardAllProtocolsAndFailureDoesNotRetry(t *testing.T) {
	for _, protocol := range []string{"native", "messages", "chat", "responses"} {
		for _, bad := range []bool{false, true} {
			t.Run(protocol+map[bool]string{false: "/success", true: "/failure"}[bad], func(t *testing.T) {
				payload := geminiImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + canvasTestImage(t) + `"}}`)
				if bad {
					payload = geminiImageResponse(`{"inlineData":{"mimeType":"image/png","data":"invalid!"}}`)
				}
				stub := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}}
				svc := &GeminiMessagesCompatService{httpUpstream: stub, cfg: &config.Config{}}
				account := webCandidateAccount(false)
				account.Credentials["model_mapping"] = map[string]any{"gemini-3-pro-image": "gemini-web-nano-banana-pro"}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				body := map[string]any{"model": "gemini-3-pro-image", "generationConfig": map[string]any{"responseModalities": []string{"IMAGE"}, "imageConfig": map[string]any{"imageSize": "1K", "aspectRatio": "1:1"}}}
				switch protocol {
				case "native":
					delete(body, "model")
					body["contents"] = []any{map[string]any{"parts": []any{map[string]any{"text": "cup"}}}}
				case "responses":
					body["input"] = "cup"
				default:
					body["messages"] = []any{map[string]any{"role": "user", "content": "cup"}}
					body["max_tokens"] = 1024
				}
				reference := canvasTestImage(t)
				switch protocol {
				case "native":
					body["contents"] = []any{map[string]any{"parts": []any{map[string]any{"text": "cup"}, map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": reference}}}}}
				case "messages":
					body["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "cup"}, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": reference}}}}}
				case "chat":
					body["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "cup"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + reference}}}}}
				case "responses":
					body["input"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "cup"}, map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + reference}}}}
				}
				raw, _ := json.Marshal(body)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, bytes.NewReader(raw))
				var result *ForwardResult
				var err error
				switch protocol {
				case "native":
					result, err = svc.ForwardNative(context.Background(), c, &account, "gemini-3-pro-image", "generateContent", false, raw)
				case "messages":
					result, err = svc.Forward(context.Background(), c, &account, raw)
				case "chat":
					result, err = svc.ForwardAsChatCompletions(context.Background(), c, &account, raw)
				case "responses":
					result, err = svc.ForwardAsResponses(context.Background(), c, &account, raw)
				}
				require.Equal(t, 1, stub.calls)
				sent, _ := io.ReadAll(stub.lastReq.Body)
				require.Contains(t, string(sent), reference)
				require.False(t, gjson.GetBytes(sent, "generationConfig.imageConfig.imageSize").Exists())
				if bad {
					require.Error(t, err)
					require.Nil(t, result)
					require.Equal(t, 502, rec.Code)
					return
				}
				require.NoError(t, err)
				require.Equal(t, 200, rec.Code)
				require.Equal(t, "1K", result.ImageInputSize)
				require.Equal(t, 1, result.ImageCount)
			})
		}
	}
}
