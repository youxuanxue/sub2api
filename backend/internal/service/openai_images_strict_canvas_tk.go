package service

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	"strconv"
	"strings"
)

// GPT Image 2 OAuth size bounds (official Images + 号池指南 / OpenAI cookbook).
const openAIImagesMaxGPTImage2Pixels int64 = 8294400
const openAIImagesMinGPTImage2Pixels int64 = 655360

const (
	openAIImagesMaxGPTImage2Aspect = 3
	openAIImagesSizeQuantum        = 16
	// Client-facing max edge for gpt-image-* (official / cookbook). Pad safety
	// uses the same cap so delivered canvases cannot exceed admission.
	openAIImagesMaxCanvasSide = 3840
)

type openAIImagesParsedSize struct {
	Width  int
	Height int
	Raw    string
}

// normalizeOpenAIImagesSizeSeparator accepts WIDTHxHEIGHT with x/X/*/× separators.
func normalizeOpenAIImagesSizeSeparator(size string) string {
	size = strings.TrimSpace(size)
	if size == "" {
		return size
	}
	size = strings.ReplaceAll(size, "×", "x")
	size = strings.ReplaceAll(size, "*", "x")
	return size
}

// canonicalizeOpenAIImagesSizeField normalizes separators and returns lowercase
// WIDTHxHEIGHT when parseable; "auto"/empty pass through; unparseable values are
// left for validateOpenAIImagesExplicitSize to reject.
func canonicalizeOpenAIImagesSizeField(size string) string {
	size = strings.TrimSpace(size)
	if size == "" {
		return ""
	}
	if strings.EqualFold(size, "auto") {
		return "auto"
	}
	size = normalizeOpenAIImagesSizeSeparator(size)
	if parsed, ok := parseOpenAIImagesWxH(size); ok {
		return parsed.Raw
	}
	return size
}

func parseOpenAIImagesWxH(size string) (openAIImagesParsedSize, bool) {
	raw := normalizeOpenAIImagesSizeSeparator(size)
	if raw == "" || strings.EqualFold(raw, "auto") {
		return openAIImagesParsedSize{}, false
	}
	parts := strings.Split(strings.ToLower(raw), "x")
	if len(parts) != 2 {
		return openAIImagesParsedSize{}, false
	}
	w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return openAIImagesParsedSize{}, false
	}
	return openAIImagesParsedSize{Width: w, Height: h, Raw: fmt.Sprintf("%dx%d", w, h)}, true
}

// validateOpenAIImagesExplicitSize enforces GPT Image 2 pixel/aspect limits before
// scheduling so over-limit canvases fail closed with 400 (customer matrix case 06).
func validateOpenAIImagesExplicitSize(req *OpenAIImagesRequest) error {
	if req == nil || !req.ExplicitSize {
		return nil
	}
	if !IsGPTImageGenerationModel(req.Model) {
		return nil
	}
	if canonical := canonicalizeOpenAIImagesSizeField(req.Size); canonical != "" {
		req.Size = canonical
	}
	parsed, ok := parseOpenAIImagesWxH(req.Size)
	if !ok {
		if strings.EqualFold(strings.TrimSpace(req.Size), "auto") {
			return nil
		}
		return fmt.Errorf("image size %q must use WIDTHxHEIGHT format or auto", strings.TrimSpace(req.Size))
	}
	if parsed.Width > openAIImagesMaxCanvasSide || parsed.Height > openAIImagesMaxCanvasSide {
		return fmt.Errorf("image size %q is invalid: each side must be <= %d", parsed.Raw, openAIImagesMaxCanvasSide)
	}
	pixels := int64(parsed.Width) * int64(parsed.Height)
	if pixels < openAIImagesMinGPTImage2Pixels {
		return fmt.Errorf("image size %q is invalid: total pixels %d below min %d", parsed.Raw, pixels, openAIImagesMinGPTImage2Pixels)
	}
	if pixels > openAIImagesMaxGPTImage2Pixels {
		return fmt.Errorf("image size %q is invalid: total pixels %d exceeds max %d", parsed.Raw, pixels, openAIImagesMaxGPTImage2Pixels)
	}
	longSide, shortSide := parsed.Width, parsed.Height
	if parsed.Height > parsed.Width {
		longSide, shortSide = parsed.Height, parsed.Width
	}
	if int64(longSide) > int64(shortSide)*openAIImagesMaxGPTImage2Aspect {
		return fmt.Errorf("image size %q is invalid: aspect ratio must not exceed %d:1", parsed.Raw, openAIImagesMaxGPTImage2Aspect)
	}
	return nil
}

// upstreamOpenAIImagesSize adapts a client canvas for GPT Image 2 (multiples of 16)
// while leaving req.Size as the authoritative final canvas for post-process.
func upstreamOpenAIImagesSize(req *OpenAIImagesRequest) string {
	if req == nil || !req.ExplicitSize {
		return strings.TrimSpace(reqSizeOrEmpty(req))
	}
	parsed, ok := parseOpenAIImagesWxH(req.Size)
	if !ok {
		return strings.TrimSpace(req.Size)
	}
	w := ceilOpenAIImagesQuantum(parsed.Width, openAIImagesSizeQuantum)
	h := ceilOpenAIImagesQuantum(parsed.Height, openAIImagesSizeQuantum)
	for int64(w)*int64(h) > openAIImagesMaxGPTImage2Pixels && (w > openAIImagesSizeQuantum || h > openAIImagesSizeQuantum) {
		if w >= h && w > openAIImagesSizeQuantum {
			w -= openAIImagesSizeQuantum
		} else if h > openAIImagesSizeQuantum {
			h -= openAIImagesSizeQuantum
		} else {
			break
		}
	}
	return fmt.Sprintf("%dx%d", w, h)
}

func reqSizeOrEmpty(req *OpenAIImagesRequest) string {
	if req == nil {
		return ""
	}
	return req.Size
}

func ceilOpenAIImagesQuantum(value, quantum int) int {
	if value <= 0 || quantum <= 1 {
		return value
	}
	return ((value + quantum - 1) / quantum) * quantum
}

// applyOpenAIImagesStrictCanvas resizes each result to the client-requested WxH
// via content-preserving pad (codex2api strict default; cover is intentionally out of scope).
// Large enlargements use progressive CatmullRom so big WxH canvases stay sharper
// than a single huge resample hop (no external Real-ESRGAN in this path).
func applyOpenAIImagesStrictCanvas(results []openAIResponsesImageResult, req *OpenAIImagesRequest) error {
	if req == nil || !req.ExplicitSize {
		return nil
	}
	target, ok := parseOpenAIImagesWxH(req.Size)
	if !ok {
		return nil
	}
	padOpaque := openAIImagesPadOpaque(req)
	for i := range results {
		raw, err := decodeOpenAIImageB64(results[i].Result)
		if err != nil {
			return err
		}
		out, err := resizeOpenAIImageExact(raw, target.Width, target.Height, padOpaque)
		if err != nil {
			return err
		}
		results[i].Result = base64.StdEncoding.EncodeToString(out)
		results[i].Size = target.Raw
		// Canvas is PNG until format coercion runs.
		if results[i].OutputFormat == "" {
			results[i].OutputFormat = "png"
		}
	}
	return nil
}

func openAIImagesPadOpaque(req *OpenAIImagesRequest) bool {
	if req == nil {
		return true
	}
	bg := strings.ToLower(strings.TrimSpace(req.Background))
	format := strings.ToLower(strings.TrimSpace(req.OutputFormat))
	if bg == "transparent" && (format == "png" || format == "webp" || format == "") {
		return false
	}
	if format == "jpeg" || format == "jpg" {
		return true
	}
	return bg != "transparent"
}

func resizeOpenAIImageExact(src []byte, targetWidth, targetHeight int, padOpaque bool) ([]byte, error) {
	return resizeImageExactWithinBounds(src, targetWidth, targetHeight, padOpaque, openAIImagesMaxCanvasSide, openAIImagesMaxGPTImage2Pixels)
}

// Shared canvas owner. Provider admission supplies its own bounds; scaling,
// aspect-preserving pad and encoding remain identical for GPT and Gemini Web.
func resizeImageExactWithinBounds(src []byte, targetWidth, targetHeight int, padOpaque bool, maxSide int, maxPixels int64) ([]byte, error) {
	if len(src) == 0 || targetWidth <= 0 || targetHeight <= 0 {
		return src, nil
	}
	if targetWidth > maxSide || targetHeight > maxSide || int64(targetWidth)*int64(targetHeight) > maxPixels {
		return nil, fmt.Errorf("strict canvas %dx%d exceeds provider bounds", targetWidth, targetHeight)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxPixels {
		return nil, fmt.Errorf("strict canvas: invalid or oversized source")
	}
	srcImg, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("strict canvas decode: %w", err)
	}
	bounds := srcImg.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()
	if sw <= 0 || sh <= 0 {
		return nil, fmt.Errorf("strict canvas: invalid source dimensions")
	}
	if sw == targetWidth && sh == targetHeight {
		return src, nil
	}

	dw, dh := fitOpenAIImageInside(sw, sh, targetWidth, targetHeight)
	content := scaleOpenAIImageProgressive(srcImg, bounds, dw, dh)

	dst := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	if padOpaque {
		stddraw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.RGBA{R: 255, G: 255, B: 255, A: 255}}, image.Point{}, stddraw.Src)
	}
	left := (targetWidth - dw) / 2
	top := (targetHeight - dh) / 2
	stddraw.Draw(dst, image.Rect(left, top, left+dw, top+dh), content, image.Point{}, stddraw.Src)

	out, err := encodeOpenAIImagePNG(dst)
	if err != nil {
		return nil, fmt.Errorf("strict canvas png encode: %w", err)
	}
	return out, nil
}

func fitOpenAIImageInside(sw, sh, boxW, boxH int) (int, int) {
	scale := float64(boxW) / float64(sw)
	if heightScale := float64(boxH) / float64(sh); heightScale < scale {
		scale = heightScale
	}
	dw := int(float64(sw)*scale + 0.5)
	dh := int(float64(sh)*scale + 0.5)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	return dw, dh
}
