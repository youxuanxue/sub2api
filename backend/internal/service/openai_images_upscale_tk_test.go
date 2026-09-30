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

func TestScaleOpenAIImageProgressiveEnlargesInSteps(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for y := 0; y < 30; y++ {
		for x := 0; x < 40; x++ {
			src.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 6), B: 90, A: 255})
		}
	}
	dst := scaleOpenAIImageProgressive(src, src.Bounds(), 160, 120)
	require.Equal(t, 160, dst.Bounds().Dx())
	require.Equal(t, 120, dst.Bounds().Dy())
	r, g, b, a := dst.At(0, 0).RGBA()
	require.Equal(t, uint32(255), a>>8)
	require.True(t, r+g+b > 0)
}

func TestResizeOpenAIImageExactUsesProgressiveUpscale(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 50, 50))
	for y := 0; y < 50; y++ {
		for x := 0; x < 50; x++ {
			src.Set(x, y, color.RGBA{R: 20, G: 180, B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, src))
	out, err := resizeOpenAIImageExact(buf.Bytes(), 200, 200, true)
	require.NoError(t, err)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	require.NoError(t, err)
	require.Equal(t, 200, cfg.Width)
	require.Equal(t, 200, cfg.Height)
}

func TestApplyOpenAIImagesStrictCanvasLargeUpscaleExactPixels(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 80, 45))
	for y := 0; y < 45; y++ {
		for x := 0; x < 80; x++ {
			src.Set(x, y, color.RGBA{R: 240, G: 10, B: 10, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	require.NoError(t, png.Encode(&pngBuf, src))
	results := []openAIResponsesImageResult{{
		Result:       base64.StdEncoding.EncodeToString(pngBuf.Bytes()),
		OutputFormat: "png",
		Size:         "80x45",
	}}
	parsed := &OpenAIImagesRequest{ExplicitSize: true, Size: "320x180", Background: "opaque", OutputFormat: "png"}
	require.NoError(t, applyOpenAIImagesStrictCanvas(results, parsed))
	require.Equal(t, "320x180", results[0].Size)
	raw, err := decodeOpenAIImageB64(results[0].Result)
	require.NoError(t, err)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	require.NoError(t, err)
	require.Equal(t, 320, cfg.Width)
	require.Equal(t, 180, cfg.Height)
}
