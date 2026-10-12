//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIImagesInputPatchCount(t *testing.T) {
	require.Equal(t, 1024, openAIImagesInputPatchCount(1024, 1024))  // 32*32
	require.Equal(t, 2304, openAIImagesInputPatchCount(2048, 1152))  // 64*36 — common 2K canvas; under limit as input
	require.Equal(t, 35344, openAIImagesInputPatchCount(6000, 6000)) // production-class over-limit square
	require.Equal(t, 30001, openAIImagesInputPatchCount(960001, 1))
	require.Equal(t, 30000, openAIImagesInputPatchCount(960000, 1))
	// floor(sqrt(30000))*32 = 173*32 = 5536 → 173² = 29929 patches (under ceiling).
	require.Equal(t, 29929, openAIImagesInputPatchCount(5536, 5536))
	require.LessOrEqual(t, openAIImagesInputPatchCount(5536, 5536), openAIImagesInputPatchLimit)
	require.Greater(t, openAIImagesInputPatchCount(5536+32, 5536+32), openAIImagesInputPatchLimit)
}

func TestRejectOpenAIImagesInputDimensions_PatchLimit(t *testing.T) {
	require.NoError(t, rejectOpenAIImagesInputDimensions(2048, 1152, "image"))
	require.NoError(t, rejectOpenAIImagesInputDimensions(5536, 5536, "image"))

	err := rejectOpenAIImagesInputDimensions(6000, 6000, "image")
	require.Error(t, err)
	require.Contains(t, err.Error(), "35344 patches")
	require.Contains(t, err.Error(), "exceeding the limit of 30000")
	require.Contains(t, err.Error(), "Please resize the image")

	err = rejectOpenAIImagesInputDimensions(70000, 100, "image")
	require.Error(t, err)
	require.Contains(t, err.Error(), "maximum side length")
}

func TestParseOpenAIImagesRequest_EditAllowsSmallInputImage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pngBytes := mustPNGBytes(t, 512, 512)
	body, contentType := multipartEditBody(t, "gpt-image-2", "touch up", pngBytes)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.True(t, parsed.IsEdits())
	require.Len(t, parsed.Uploads, 1)
}

func TestParseOpenAIImagesRequest_EditRejectsOverPatchLimitInput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// IHDR-only PNG claims 6000×6000 (35344 patches) without allocating a full bitmap.
	pngBytes := pngIHDRDimensions(t, 6000, 6000)
	body, contentType := multipartEditBody(t, "gpt-image-2", "touch up", pngBytes)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
	require.Error(t, err)
	require.Nil(t, parsed)
	require.Contains(t, err.Error(), "35344 patches")
	require.Contains(t, err.Error(), "exceeding the limit of 30000")
	require.Contains(t, err.Error(), "Please resize the image")
}

func TestParseOpenAIImagesRequest_EditRejectsOverPatchLimitDataURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pngBytes := pngIHDRDimensions(t, 6000, 6000)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	body := []byte(fmt.Sprintf(`{"model":"gpt-image-2","prompt":"edit","images":[{"image_url":%q}]}`, dataURL))

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
	require.Error(t, err)
	require.Nil(t, parsed)
	require.Contains(t, err.Error(), "35344 patches")
	require.Contains(t, err.Error(), "exceeding the limit of 30000")
}

func TestForwardImages_EditOverPatchLimitNeverHitsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pngBytes := pngIHDRDimensions(t, 6000, 6000)
	body, contentType := multipartEditBody(t, "gpt-image-2", "touch up", pngBytes)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"created":1,"data":[{"b64_json":"ok"}]}`))),
		},
	}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}

	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.Error(t, err)
	require.Nil(t, parsed)
	require.Contains(t, err.Error(), "exceeding the limit of 30000")

	// Parse failure is the handler path that returns 400; ForwardImages must not run.
	require.Nil(t, upstream.lastReq)
	require.Empty(t, upstream.lastBody)
}

func TestForwardImages_EditSmallInputStillReachesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pngBytes := mustPNGBytes(t, 256, 256)
	body, contentType := multipartEditBody(t, "gpt-image-2", "touch up", pngBytes)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body: io.NopCloser(bytes.NewReader([]byte(`{"created":1,"data":[{"b64_json":"ZWRpdGVk"}]}`))),
		},
	}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}

	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	account := &Account{
		ID:       9,
		Name:     "openai-apikey-edit-patches",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "test-api-key",
			"base_url": "https://image-upstream.example/v1/",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://image-upstream.example/v1/images/edits", upstream.lastReq.URL.String())
	require.Equal(t, 1, result.ImageCount)
}

func multipartEditBody(t *testing.T, model, prompt string, imageBytes []byte) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", model))
	require.NoError(t, writer.WriteField("prompt", prompt))

	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="image"; filename="source.png"`)
	header.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write(imageBytes)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return body.Bytes(), writer.FormDataContentType()
}

func mustPNGBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// pngIHDRDimensions builds a minimal PNG whose IHDR reports width×height.
// image.DecodeConfig only needs IHDR, so tests can claim huge canvases without
// allocating a full bitmap (6000×6000 RGBA ≈ 144MiB).
func pngIHDRDimensions(t *testing.T, width, height int) []byte {
	t.Helper()
	require.Greater(t, width, 0)
	require.Greater(t, height, 0)

	var buf bytes.Buffer
	buf.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], uint32(width))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(height))
	ihdr[8] = 8  // bit depth
	ihdr[9] = 2  // color type RGB
	ihdr[10] = 0 // compression
	ihdr[11] = 0 // filter
	ihdr[12] = 0 // interlace
	writePNGChunk(&buf, "IHDR", ihdr)
	writePNGChunk(&buf, "IEND", nil)

	cfg, _, err := image.DecodeConfig(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	require.Equal(t, width, cfg.Width)
	require.Equal(t, height, cfg.Height)
	return buf.Bytes()
}

func writePNGChunk(buf *bytes.Buffer, chunkType string, data []byte) {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	buf.Write(lenBuf[:])
	buf.WriteString(chunkType)
	buf.Write(data)
	crc := crc32.NewIEEE()
	_, _ = crc.Write([]byte(chunkType))
	_, _ = crc.Write(data)
	var crcBuf [4]byte
	binary.BigEndian.PutUint32(crcBuf[:], crc.Sum32())
	buf.Write(crcBuf[:])
}
