package service

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

// GPT Image / Codex input-image patch budget (OpenAI Images & vision docs).
//
// Official formula for patch-based image inputs:
//
//	patches = ceil(width/32) * ceil(height/32)
//
// Hard rejection limit after processing: 30000 patches per image (not summed
// across images). Evidence: production gpt-image edits 400s for user 61
// (rt@tk.com), e.g. "requires 59996 patches after processing, exceeding the
// limit of 30000"; same class seen with 49152/52160/106032/135420. OpenAI
// docs: developers.openai.com/api/docs/guides/images-vision §Patch-based
// image tokenization — images over the limit are rejected, not auto-resized.
//
// Square-equivalent guidance (same 30000 budget): max square side ≈
// floor(sqrt(30000))*32 = 5536; full-patch pixel ceiling ≈ 30000*32*32 =
// 30_720_000. Thin/wide images can exceed the patch limit with far fewer
// total pixels, so the patches check is authoritative. The 65535 side cap
// matches OpenAI's documented max dimension before original-detail scaling.
const (
	openAIImagesInputPatchSizePx = 32
	openAIImagesInputPatchLimit  = 30000
	openAIImagesInputMaxSidePx   = 65535
)

// validateOpenAIImagesEditInputPatches rejects gpt-image edits whose local
// input image(s) would trip the upstream 30000-patch ceiling, before any
// account scheduling or upstream call. Undecodable bytes and remote http(s)
// URLs are skipped (fail-open) so existing fixtures and URL-only JSON edits
// still reach upstream; data: URLs and multipart uploads are checked when
// DecodeConfig succeeds.
func validateOpenAIImagesEditInputPatches(req *OpenAIImagesRequest) error {
	if req == nil || !req.IsEdits() {
		return nil
	}
	if !IsGPTImageGenerationModel(req.Model) {
		return nil
	}

	for i, upload := range req.Uploads {
		if err := rejectOpenAIImagesInputIfOverPatchLimit(upload.Data, uploadLabel(upload.FieldName, i)); err != nil {
			return err
		}
	}
	if req.MaskUpload != nil {
		if err := rejectOpenAIImagesInputIfOverPatchLimit(req.MaskUpload.Data, "mask"); err != nil {
			return err
		}
	}
	for i, imageURL := range req.InputImageURLs {
		if raw, ok := decodeOpenAIImagesDataURLPayload(imageURL); ok {
			if err := rejectOpenAIImagesInputIfOverPatchLimit(raw, fmt.Sprintf("images[%d]", i)); err != nil {
				return err
			}
		}
	}
	if raw, ok := decodeOpenAIImagesDataURLPayload(req.MaskImageURL); ok {
		if err := rejectOpenAIImagesInputIfOverPatchLimit(raw, "mask"); err != nil {
			return err
		}
	}
	return nil
}

func uploadLabel(fieldName string, index int) string {
	if trimmed := strings.TrimSpace(fieldName); trimmed != "" {
		return trimmed
	}
	return fmt.Sprintf("image[%d]", index)
}

func rejectOpenAIImagesInputIfOverPatchLimit(data []byte, label string) error {
	w, h, ok := probeOpenAIImagesInputDimensions(data)
	if !ok {
		return nil
	}
	return rejectOpenAIImagesInputDimensions(w, h, label)
}

func rejectOpenAIImagesInputDimensions(width, height int, label string) error {
	if width <= 0 || height <= 0 {
		return nil
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = "input"
	}
	patches := openAIImagesInputPatchCount(width, height)

	// Patches first: matches the upstream 400 customers already see.
	if patches > openAIImagesInputPatchLimit {
		return fmt.Errorf(
			"the image you provided (%s, %dx%d) requires %d patches after processing, exceeding the limit of %d. Please resize the image and try again",
			label, width, height, patches, openAIImagesInputPatchLimit,
		)
	}
	if width > openAIImagesInputMaxSidePx || height > openAIImagesInputMaxSidePx {
		return fmt.Errorf(
			"the image you provided (%s, %dx%d) exceeds the maximum side length of %d pixels. Please resize the image and try again",
			label, width, height, openAIImagesInputMaxSidePx,
		)
	}
	return nil
}

// openAIImagesInputPatchCount mirrors OpenAI's documented patch coverage:
// ceil(width/32) * ceil(height/32).
func openAIImagesInputPatchCount(width, height int) int {
	if width <= 0 || height <= 0 {
		return 0
	}
	return ceilDivInt(width, openAIImagesInputPatchSizePx) * ceilDivInt(height, openAIImagesInputPatchSizePx)
}

func ceilDivInt(value, divisor int) int {
	if value <= 0 || divisor <= 0 {
		return 0
	}
	return (value + divisor - 1) / divisor
}

func probeOpenAIImagesInputDimensions(data []byte) (int, int, bool) {
	if len(data) == 0 {
		return 0, 0, false
	}
	if w, h, ok := detectOpenAIWebPDimensions(data); ok {
		return w, h, true
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

func decodeOpenAIImagesDataURLPayload(raw string) ([]byte, bool) {
	payload := strings.TrimSpace(raw)
	lower := strings.ToLower(payload)
	if !strings.HasPrefix(lower, "data:image/") {
		return nil, false
	}
	comma := strings.IndexByte(payload, ',')
	if comma < 0 || comma+1 >= len(payload) {
		return nil, false
	}
	meta := strings.ToLower(payload[:comma])
	encoded := strings.TrimSpace(payload[comma+1:])
	if encoded == "" {
		return nil, false
	}
	if !strings.Contains(meta, ";base64") {
		return nil, false
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		decoded, err := encoding.DecodeString(encoded)
		if err == nil && len(decoded) > 0 {
			return decoded, true
		}
	}
	return nil, false
}
