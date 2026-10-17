//go:build unit

package service

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"
)

// The ancillary caBX payload is deliberately opaque, not a valid C2PA signature.
// Byte identity proves preservation without pretending to validate credentials.
func imagePNGWithOpaqueCredentials(t *testing.T) []byte {
	t.Helper()
	raw, err := decodeOpenAIImageB64(encodeOpenAIImageTestPNG(t, 32, 32))
	require.NoError(t, err)
	payload := []byte("opaque-upstream-content-credentials")
	chunk := make([]byte, len(payload)+12)
	binary.BigEndian.PutUint32(chunk[:4], uint32(len(payload)))
	copy(chunk[4:8], "caBX")
	copy(chunk[8:], payload)
	binary.BigEndian.PutUint32(chunk[len(chunk)-4:], crc32.ChecksumIEEE(chunk[4:len(chunk)-4]))
	out := append([]byte{}, raw[:len(raw)-12]...)
	out = append(out, chunk...)
	return append(out, raw[len(raw)-12:]...)
}

func TestImagesPostprocessPreservesUpstreamBytesForGenerationQuality(t *testing.T) {
	raw := imagePNGWithOpaqueCredentials(t)
	for _, quality := range []string{"low", "medium", "high", "auto"} {
		for _, format := range []string{"", "png"} {
			t.Run(quality+"/"+format, func(t *testing.T) {
				results := []openAIResponsesImageResult{{Result: base64.StdEncoding.EncodeToString(raw), OutputFormat: "png"}}
				parsed := &OpenAIImagesRequest{Quality: quality, ExplicitQuality: true, Size: "32x32", ExplicitSize: true, OutputFormat: format, ExplicitOutputFormat: format != ""}
				require.NoError(t, applyOpenAIImagesClientFidelityPostprocess(results, parsed))
				got, err := decodeOpenAIImageB64(results[0].Result)
				require.NoError(t, err)
				require.Equal(t, raw, got, "unchanged image must retain all original chunks")
			})
		}
	}
}

func TestImagesPostprocessStillTransformsRequestedFormatSizeAndCompression(t *testing.T) {
	raw := imagePNGWithOpaqueCredentials(t)
	compression := 80
	for _, tc := range []struct {
		name, format, size string
		compression        *int
	}{
		{name: "format", format: "jpeg", size: "32x32"},
		{name: "canvas", format: "png", size: "64x48"},
		{name: "compression", format: "png", size: "32x32", compression: &compression},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results := []openAIResponsesImageResult{{Result: base64.StdEncoding.EncodeToString(raw)}}
			req := &OpenAIImagesRequest{OutputFormat: tc.format, ExplicitOutputFormat: true, Size: tc.size, ExplicitSize: true, Quality: "low", ExplicitQuality: true, OutputCompression: tc.compression}
			require.NoError(t, applyOpenAIImagesClientFidelityPostprocess(results, req))
			got, err := decodeOpenAIImageB64(results[0].Result)
			require.NoError(t, err)
			require.Equal(t, tc.format, sniffOpenAIImageFormat(got))
			cfg, _, err := image.DecodeConfig(bytes.NewReader(got))
			require.NoError(t, err)
			want, _ := parseOpenAIImagesWxH(tc.size)
			require.Equal(t, want.Width, cfg.Width)
			require.Equal(t, want.Height, cfg.Height)
			require.NotContains(t, string(got), "opaque-upstream-content-credentials", "do not copy invalid credentials onto a derivative")
			if tc.name == "compression" {
				before, err := png.Decode(bytes.NewReader(raw))
				require.NoError(t, err)
				after, err := png.Decode(bytes.NewReader(got))
				require.NoError(t, err)
				require.Equal(t, before, after)
			}
		})
	}
}
