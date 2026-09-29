package service

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"strings"

	"github.com/HugoSmits86/nativewebp"
	_ "golang.org/x/image/webp"
)

type openAIImagesEncodeHints struct {
	Quality           string
	OutputCompression *int
}

// coerceOpenAIImageB64ToOutputFormatWithQuality re-encodes image bytes when the
// client explicitly requested jpeg/jpg/png/webp but Codex Direct / OAuth returned
// a different container. Optional quality/compression hints adjust encoder settings.
// Upstream ChatGPT Images frequently ignores tools.output_format and emits PNG while
// still acknowledging the request; without this step client format contracts lie.
//
// Fail-closed: unrecognized containers, decode failures, and re-encode failures return
// an error so callers never stamp output_format=jpeg on bytes that are still PNG/webp.
func coerceOpenAIImageB64ToOutputFormatWithQuality(encoded, wantFormat string, hints *openAIImagesEncodeHints) (string, string, error) {
	want := strings.ToLower(strings.TrimSpace(wantFormat))
	switch want {
	case "", "auto":
		return encoded, "", nil
	case "jpg":
		want = "jpeg"
	case "jpeg", "png", "webp":
	default:
		return encoded, "", nil
	}

	raw, err := decodeOpenAIImageB64(encoded)
	if err != nil {
		return "", "", err
	}
	actual := sniffOpenAIImageFormat(raw)
	if actual == "" {
		return "", "", fmt.Errorf("unrecognized image container for output_format=%s coerce", want)
	}

	// Same container and no quality/compression hint: keep upstream bytes.
	if actual == want && !openAIImagesEncodeHintsActive(hints) {
		return encoded, actual, nil
	}

	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", "", fmt.Errorf("decode %s image for output_format=%s coerce: %w", actual, want, err)
	}
	var out bytes.Buffer
	switch want {
	case "jpeg":
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: openAIImagesJPEGQuality(hints)}); err != nil {
			return "", "", fmt.Errorf("encode jpeg: %w", err)
		}
	case "png":
		level := png.DefaultCompression
		if hints != nil && hints.OutputCompression != nil {
			c := *hints.OutputCompression
			switch {
			case c <= 20:
				level = png.BestSpeed
			case c >= 80:
				level = png.BestCompression
			}
		}
		encoder := png.Encoder{CompressionLevel: level}
		if err := encoder.Encode(&out, img); err != nil {
			return "", "", fmt.Errorf("encode png: %w", err)
		}
	case "webp":
		opts := &nativewebp.Options{CompressionLevel: openAIImagesWebPCompression(hints)}
		if err := nativewebp.Encode(&out, img, opts); err != nil {
			return "", "", fmt.Errorf("encode webp: %w", err)
		}
	}
	return base64.StdEncoding.EncodeToString(out.Bytes()), want, nil
}

func openAIImagesEncodeHintsActive(hints *openAIImagesEncodeHints) bool {
	if hints == nil {
		return false
	}
	if hints.OutputCompression != nil {
		return true
	}
	q := strings.ToLower(strings.TrimSpace(hints.Quality))
	return q != "" && q != "auto"
}

func openAIImagesJPEGQuality(hints *openAIImagesEncodeHints) int {
	if hints != nil && hints.OutputCompression != nil {
		c := *hints.OutputCompression
		if c < 1 {
			c = 1
		}
		if c > 100 {
			c = 100
		}
		return c
	}
	switch strings.ToLower(strings.TrimSpace(hintQuality(hints))) {
	case "low":
		return 45
	case "medium":
		return 72
	case "high":
		return 88
	case "xhigh", "max":
		return 95
	default:
		return 92
	}
}

func openAIImagesWebPCompression(hints *openAIImagesEncodeHints) nativewebp.CompressionLevel {
	if hints != nil && hints.OutputCompression != nil {
		c := *hints.OutputCompression
		switch {
		case c <= 30:
			return nativewebp.BestSpeed
		case c >= 80:
			return nativewebp.BestCompression
		default:
			return nativewebp.DefaultCompression
		}
	}
	switch strings.ToLower(strings.TrimSpace(hintQuality(hints))) {
	case "low":
		return nativewebp.BestSpeed
	case "high", "xhigh", "max":
		return nativewebp.BestCompression
	default:
		return nativewebp.DefaultCompression
	}
}

func hintQuality(hints *openAIImagesEncodeHints) string {
	if hints == nil {
		return ""
	}
	return hints.Quality
}

func decodeOpenAIImageB64(encoded string) ([]byte, error) {
	payload := strings.TrimSpace(encoded)
	if strings.HasPrefix(strings.ToLower(payload), "data:") {
		comma := strings.IndexByte(payload, ',')
		if comma < 0 || comma+1 >= len(payload) {
			return nil, fmt.Errorf("invalid data URL")
		}
		payload = strings.TrimSpace(payload[comma+1:])
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(payload)
	}
	if err != nil {
		return nil, fmt.Errorf("decode b64 image: %w", err)
	}
	return raw, nil
}

func sniffOpenAIImageFormat(raw []byte) string {
	switch {
	case len(raw) >= 3 && raw[0] == 0xff && raw[1] == 0xd8 && raw[2] == 0xff:
		return "jpeg"
	case len(raw) >= 8 && string(raw[:8]) == "\x89PNG\r\n\x1a\n":
		return "png"
	case len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "webp"
	default:
		return ""
	}
}

func applyOpenAIImagesOutputFormatCoercionWithHints(results []openAIResponsesImageResult, wantFormat string, hints *openAIImagesEncodeHints) error {
	want := strings.ToLower(strings.TrimSpace(wantFormat))
	explicitFormat := want != "" && want != "auto"
	if !explicitFormat && !openAIImagesEncodeHintsActive(hints) {
		return nil
	}
	for i := range results {
		format := want
		if !explicitFormat {
			raw, err := decodeOpenAIImageB64(results[i].Result)
			if err != nil {
				// Quality-only re-encode cannot proceed without decodable bytes;
				// leave upstream payload untouched rather than fail the request.
				continue
			}
			format = sniffOpenAIImageFormat(raw)
			// Do not trust call_meta.output_format when bytes are not a known
			// image container — upstream often stamps png on opaque payloads.
			if format == "" || format == "auto" {
				continue
			}
		}
		coerced, actual, err := coerceOpenAIImageB64ToOutputFormatWithQuality(results[i].Result, format, hints)
		if err != nil {
			return err
		}
		if coerced == "" {
			return fmt.Errorf("output_format coerce produced empty image")
		}
		results[i].Result = coerced
		if actual != "" {
			results[i].OutputFormat = actual
		}
	}
	return nil
}

// applyOpenAIImagesClientFidelityPostprocess applies codex2api-style local fidelity:
// exact requested canvas first, then output_format / compression / quality re-encode.
func applyOpenAIImagesClientFidelityPostprocess(results []openAIResponsesImageResult, parsed *OpenAIImagesRequest) error {
	if err := applyOpenAIImagesStrictCanvas(results, parsed); err != nil {
		return err
	}
	wantFormat := ""
	hints := &openAIImagesEncodeHints{}
	if parsed != nil {
		if parsed.ExplicitOutputFormat {
			wantFormat = parsed.OutputFormat
		}
		if parsed.ExplicitQuality {
			hints.Quality = parsed.Quality
		}
		hints.OutputCompression = parsed.OutputCompression
	}
	if wantFormat == "" && !openAIImagesEncodeHintsActive(hints) {
		return nil
	}
	return applyOpenAIImagesOutputFormatCoercionWithHints(results, wantFormat, hints)
}
