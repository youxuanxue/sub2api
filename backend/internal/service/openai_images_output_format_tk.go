package service

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
)

// coerceOpenAIImageB64ToOutputFormat re-encodes image bytes when the client explicitly
// requested jpeg/jpg (or png) but Codex Direct / OAuth returned a different container.
// Upstream ChatGPT Images frequently ignores tools.output_format and emits PNG while
// still acknowledging the request; without this step tk_image_contract=exact and plain
// output_format=jpeg both lie to the client.
func coerceOpenAIImageB64ToOutputFormat(encoded, wantFormat string) (string, string, error) {
	want := strings.ToLower(strings.TrimSpace(wantFormat))
	switch want {
	case "", "auto":
		return encoded, "", nil
	case "jpg":
		want = "jpeg"
	case "jpeg", "png":
	default:
		return encoded, "", nil
	}

	raw, err := decodeOpenAIImageB64(encoded)
	if err != nil {
		return "", "", err
	}
	actual := sniffOpenAIImageFormat(raw)
	if actual == want || actual == "" {
		if actual == "" {
			actual = want
		}
		return encoded, actual, nil
	}

	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", "", fmt.Errorf("decode image for output_format coerce: %w", err)
	}
	var out bytes.Buffer
	switch want {
	case "jpeg":
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 92}); err != nil {
			return "", "", fmt.Errorf("encode jpeg: %w", err)
		}
	case "png":
		if err := png.Encode(&out, img); err != nil {
			return "", "", fmt.Errorf("encode png: %w", err)
		}
	}
	return base64.StdEncoding.EncodeToString(out.Bytes()), want, nil
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

func applyOpenAIImagesOutputFormatCoercion(results []openAIResponsesImageResult, wantFormat string) {
	want := strings.ToLower(strings.TrimSpace(wantFormat))
	if want == "" || want == "auto" {
		return
	}
	for i := range results {
		coerced, actual, err := coerceOpenAIImageB64ToOutputFormat(results[i].Result, want)
		if err != nil || coerced == "" {
			continue
		}
		results[i].Result = coerced
		if actual != "" {
			results[i].OutputFormat = actual
		}
	}
}
