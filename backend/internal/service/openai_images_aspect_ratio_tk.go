package service

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// openAIImagesKnownSize is one official WxH ↔ aspect_ratio binding for gpt-image-*.
// openAIImagesKnownSizeTable is the SINGLE source of truth for:
//   - size → aspect_ratio marker derivation (openAIImagesAspectRatioFromSize)
//   - admitted aspect_ratio allowlist
//   - Studio/Quickstart size chips (StudioChip=true), generated into
//     frontend/src/constants/gptImageSizes.generated.tk.ts via
//     go run ./cmd/gpt-image-sizes-ssot
//
// Official GPT Image docs do not expose aspect_ratio; they document size=WxH.
// ChatGPT OAuth soft-controls canvas via prompt "marker AR=...".
type openAIImagesKnownSize struct {
	Size       string
	Ratio      string
	StudioChip bool // Studio/Quickstart default chip (prefer 1K/2K, not 4K)
}

// Stable order: studio chips first per ratio, then non-studio aliases.
var openAIImagesKnownSizeTable = []openAIImagesKnownSize{
	{Size: "1024x1024", Ratio: "1:1", StudioChip: true},
	{Size: "2048x2048", Ratio: "1:1", StudioChip: false},
	{Size: "1536x1024", Ratio: "3:2", StudioChip: true},
	{Size: "1024x1536", Ratio: "2:3", StudioChip: true},
	{Size: "2048x1152", Ratio: "16:9", StudioChip: true},
	{Size: "3840x2160", Ratio: "16:9", StudioChip: false},
	{Size: "1152x2048", Ratio: "9:16", StudioChip: true},
	{Size: "2160x3840", Ratio: "9:16", StudioChip: false},
}

// Canonical listing for error messages (stable order, one entry per ratio).
var openAIImagesAllowedAspectRatioList = openAIImagesAllowedAspectRatiosFromTable()

var openAIImagesAllowedAspectRatios = func() map[string]struct{} {
	out := make(map[string]struct{}, len(openAIImagesAllowedAspectRatioList))
	for _, ratio := range openAIImagesAllowedAspectRatioList {
		out[ratio] = struct{}{}
	}
	return out
}()

func openAIImagesAllowedAspectRatiosFromTable() []string {
	seen := make(map[string]struct{}, 8)
	out := make([]string, 0, 8)
	for _, row := range openAIImagesKnownSizeTable {
		if _, ok := seen[row.Ratio]; ok {
			continue
		}
		seen[row.Ratio] = struct{}{}
		out = append(out, row.Ratio)
	}
	return out
}

var openAIImagesAspectRatioMarkerRE = regexp.MustCompile(`(?i)\bmarker\s+AR\s*=\s*[^\s,;]+`)

// GPTImageStudioSizeChip is the FE/Studio projection of StudioChip rows.
type GPTImageStudioSizeChip struct {
	Ratio string `json:"ratio"`
	Value string `json:"value"`
}

// ExportGPTImageStudioSizeChips returns Studio/Quickstart chips derived from
// openAIImagesKnownSizeTable. Used by cmd/gpt-image-sizes-ssot.
func ExportGPTImageStudioSizeChips() []GPTImageStudioSizeChip {
	out := make([]GPTImageStudioSizeChip, 0, 8)
	for _, row := range openAIImagesKnownSizeTable {
		if !row.StudioChip {
			continue
		}
		out = append(out, GPTImageStudioSizeChip{Ratio: row.Ratio, Value: row.Size})
	}
	return out
}

func normalizeOpenAIImagesAspectRatio(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return "", unsupportedOpenAIImagesAspectRatio(raw)
	}
	w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return "", unsupportedOpenAIImagesAspectRatio(raw)
	}
	canonical := fmt.Sprintf("%d:%d", w, h)
	if _, ok := openAIImagesAllowedAspectRatios[canonical]; !ok {
		return "", unsupportedOpenAIImagesAspectRatio(raw)
	}
	return canonical, nil
}

func unsupportedOpenAIImagesAspectRatio(raw string) error {
	return fmt.Errorf(
		"unsupported aspect_ratio %q; supported values: %s",
		raw,
		strings.Join(openAIImagesAllowedAspectRatioList, ", "),
	)
}

// openAIImagesAspectRatioFromSize maps known official sizes onto the admitted
// aspect-ratio set. Unknown / auto sizes return empty (no marker injection).
func openAIImagesAspectRatioFromSize(size string) string {
	want := strings.ToLower(strings.TrimSpace(size))
	if want == "" || want == "auto" {
		return ""
	}
	for _, row := range openAIImagesKnownSizeTable {
		if row.Size == want {
			return row.Ratio
		}
	}
	return ""
}

// resolveOpenAIImagesAspectRatioForMarker prefers an explicit admitted
// aspect_ratio. When deriveFromSize is true (OAuth / Responses soft-control
// paths), a known official size may also derive the marker. API-key Images
// already honors size as a hard control, so that path must pass false to avoid
// size-derived markers — and must also skip explicit AR markers when size is
// already set, so soft AR text cannot fight the official WxH size.
func resolveOpenAIImagesAspectRatioForMarker(parsed *OpenAIImagesRequest, deriveFromSize bool) string {
	if parsed == nil || !IsGPTImageGenerationModel(parsed.Model) {
		return ""
	}
	if parsed.ExplicitAspectRatio {
		if !deriveFromSize && parsed.ExplicitSize {
			return ""
		}
		return strings.TrimSpace(parsed.AspectRatio)
	}
	if deriveFromSize && parsed.ExplicitSize {
		return openAIImagesAspectRatioFromSize(parsed.Size)
	}
	return ""
}

// applyOpenAIImagesAspectRatioMarker appends "marker AR=<ratio>" when absent.
// Existing client markers are left untouched (idempotent).
func applyOpenAIImagesAspectRatioMarker(prompt, aspectRatio string) string {
	aspectRatio = strings.TrimSpace(aspectRatio)
	if aspectRatio == "" {
		return prompt
	}
	if openAIImagesAspectRatioMarkerRE.MatchString(prompt) {
		return prompt
	}
	marker := "marker AR=" + aspectRatio
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return marker
	}
	return prompt + ", " + marker
}

func validateOpenAIImagesAspectRatio(req *OpenAIImagesRequest) error {
	if req == nil || !req.ExplicitAspectRatio {
		return nil
	}
	if !IsGPTImageGenerationModel(req.Model) {
		return fmt.Errorf("aspect_ratio is only supported for gpt-image-* models")
	}
	normalized, err := normalizeOpenAIImagesAspectRatio(req.AspectRatio)
	if err != nil {
		return err
	}
	req.AspectRatio = normalized
	return nil
}
