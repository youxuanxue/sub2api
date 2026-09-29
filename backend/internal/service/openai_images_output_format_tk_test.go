//go:build unit

package service

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCoerceOpenAIImageB64ToJPEG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var pngBuf bytes.Buffer
	require.NoError(t, png.Encode(&pngBuf, img))
	pngB64 := base64.StdEncoding.EncodeToString(pngBuf.Bytes())

	got, format, err := coerceOpenAIImageB64ToOutputFormat(pngB64, "jpeg")
	require.NoError(t, err)
	require.Equal(t, "jpeg", format)
	raw, err := base64.StdEncoding.DecodeString(got)
	require.NoError(t, err)
	require.Equal(t, "jpeg", sniffOpenAIImageFormat(raw))
}

func TestRequireGeminiImageModelOutput(t *testing.T) {
	empty := map[string]any{
		"candidates": []any{
			map[string]any{"content": map[string]any{"parts": []any{}}, "finishReason": "STOP"},
		},
	}
	require.ErrorIs(t, requireGeminiImageModelOutput("gemini-3.1-flash-image", empty), errGeminiImageModelEmpty)
	require.NoError(t, requireGeminiImageModelOutput("gemini-2.5-flash", empty))

	withImage := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{
						map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "iVBORw0KGgo="}},
					},
				},
			},
		},
	}
	require.NoError(t, requireGeminiImageModelOutput("gemini-3.1-flash-image", withImage))
}

func TestApplyOpenAIImagesOutputFormatCoercion(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	var pngBuf bytes.Buffer
	require.NoError(t, png.Encode(&pngBuf, img))
	results := []openAIResponsesImageResult{{
		Result:       base64.StdEncoding.EncodeToString(pngBuf.Bytes()),
		OutputFormat: "png",
	}}
	applyOpenAIImagesOutputFormatCoercion(results, "jpeg")
	require.Equal(t, "jpeg", results[0].OutputFormat)
	raw, err := base64.StdEncoding.DecodeString(results[0].Result)
	require.NoError(t, err)
	require.Equal(t, "jpeg", sniffOpenAIImageFormat(raw))
}
