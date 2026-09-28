package service

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Official GPT Image docs do not expose an aspect_ratio request field; they document
// size=WIDTHxHEIGHT. The allowlist below is the reduced aspect-ratio set implied by
// OpenAI's commonly listed sizes for gpt-image-* (image prompting guide + Images API):
//
//	1024x1024 / 2048x2048 → 1:1
//	1536x1024             → 3:2
//	1024x1536             → 2:3
//	2048x1152 / 3840x2160 → 16:9
//	1152x2048 / 2160x3840 → 9:16
//
// ChatGPT OAuth / Codex Images soft-controls canvas ratio via prompt text
// ("marker AR=..."); TokenKey admits aspect_ratio for gpt-image-* and injects
// that marker on outbound prompts. This is a TokenKey OAuth soft-control, not an
// official OpenAI Images parameter.
var openAIImagesAllowedAspectRatios = map[string]struct{}{
	"1:1":  {},
	"3:2":  {},
	"2:3":  {},
	"16:9": {},
	"9:16": {},
}

// Canonical listing for error messages (stable order).
var openAIImagesAllowedAspectRatioList = []string{"1:1", "3:2", "2:3", "16:9", "9:16"}

var openAIImagesAspectRatioMarkerRE = regexp.MustCompile(`(?i)\bmarker\s+AR\s*=\s*[^\s,;]+`)

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

// openAIImagesAspectRatioFromSize maps official common sizes onto the admitted
// aspect-ratio set. Unknown / auto sizes return empty (no marker injection).
func openAIImagesAspectRatioFromSize(size string) string {
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "1024x1024", "2048x2048":
		return "1:1"
	case "1536x1024":
		return "3:2"
	case "1024x1536":
		return "2:3"
	case "2048x1152", "3840x2160":
		return "16:9"
	case "1152x2048", "2160x3840":
		return "9:16"
	default:
		return ""
	}
}

// resolveOpenAIImagesAspectRatioForMarker prefers an explicit admitted
// aspect_ratio; otherwise derives from a known official size.
func resolveOpenAIImagesAspectRatioForMarker(parsed *OpenAIImagesRequest) string {
	if parsed == nil || !IsGPTImageGenerationModel(parsed.Model) {
		return ""
	}
	if parsed.ExplicitAspectRatio {
		return strings.TrimSpace(parsed.AspectRatio)
	}
	if parsed.ExplicitSize {
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
