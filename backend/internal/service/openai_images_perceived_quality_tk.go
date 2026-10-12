package service

import (
	"os"
	"strings"
)

// OAuth gpt-image perceived-quality knobs.
//
// Research (read before this file existed):
//   - VelmoAI/codex-imagen builds two strings. For string prompts the user
//     message stays the caller's text. medium/high/max put a Codex labeled-spec
//     preamble in instructions so the mainline model rewrites that text before
//     image_generation. raw uses anti-refinement instructions plus
//     reasoning.effort=none. Mode map: raw→none, medium→medium, high→high,
//     max→xhigh. auto with quoted "Text (verbatim)" bumps to high; a plain
//     string with no skills becomes raw.
//   - The JSON body is built by smturtle2/codex-image-gen _build_payload:
//     reasoning is set only when effort/summary are passed; instructions
//     default to a one-line tool reminder, not "". quality and action are
//     not sent. Allowed efforts: none, minimal, low, medium, high, xhigh.
//
// TokenKey keeps instructions "" (codex2api: do not ask the driver to rewrite).
// The labeled-spec cues therefore live on input_text, which is what a
// passthrough driver forwards to the image tool. This does not change pixel
// count; upstream may still soft-size near 1K. No upscaler.
const (
	openAIImagesReasoningEffortEnv      = "SUB2API_IMAGES_REASONING_EFFORT"
	openAIImagesPromptScaffoldEnv       = "SUB2API_IMAGES_PROMPT_SCAFFOLD"
	openAIImagesDefaultReasoningEffort  = "medium"
	openAIImagesVerbatimReasoningEffort = "high"

	openAIImagesPerceivedQualityPrimary = "Primary request:"
)

var openAIImagesAllowedReasoningEfforts = map[string]struct{}{
	"none":    {},
	"minimal": {},
	"low":     {},
	"medium":  {},
	"high":    {},
	"xhigh":   {},
}

func openAIImagesReasoningEffortForPrompt(prompt string) string {
	if effort, ok := openAIImagesExplicitReasoningEffort(); ok {
		return effort
	}
	if openAIImagesPromptHasQuotedText(prompt) {
		return openAIImagesVerbatimReasoningEffort
	}
	return openAIImagesDefaultReasoningEffort
}

// openAIImagesExplicitReasoningEffort reports a valid env override.
// Unset or unrecognized values are not an override; the caller uses the
// Velmo-shaped default (medium, or high when the prompt quotes text).
func openAIImagesExplicitReasoningEffort() (string, bool) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(openAIImagesReasoningEffortEnv)))
	if raw == "" {
		return "", false
	}
	if _, ok := openAIImagesAllowedReasoningEfforts[raw]; !ok {
		return "", false
	}
	return raw, true
}

func openAIImagesPromptScaffoldEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(openAIImagesPromptScaffoldEnv))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

func openAIImagesPromptHasQuotedText(prompt string) bool {
	return strings.ContainsAny(prompt, "\"“”「」『』")
}

// applyOpenAIImagesPerceivedQualityPrompt wraps a client prompt with a short
// Codex labeled-spec scaffold. It does not invent a subject, palette, size,
// pixel count, or quality tier. Style guidance and marker AR stay outside
// this helper so they can still be appended afterwards.
func applyOpenAIImagesPerceivedQualityPrompt(prompt string, edit bool) string {
	if !openAIImagesPromptScaffoldEnabled() {
		return prompt
	}
	if strings.HasPrefix(strings.TrimLeft(prompt, " \t"), openAIImagesPerceivedQualityPrimary) {
		return prompt
	}

	constraints := "Constraints: keep the requested subject and any quoted text; do not add unrelated objects, captions, or watermarks"
	if edit {
		constraints = "Constraints: preserve parts of the input image the request does not ask to change; keep the requested subject and any quoted text; do not add unrelated objects, captions, or watermarks"
	}

	lines := []string{
		openAIImagesPerceivedQualityPrimary + " " + prompt,
		"Composition/framing: clear focal subject, intentional framing, and uncluttered negative space that serve the request",
		"Lighting/mood: coherent light direction and atmosphere consistent with the request",
		"Materials/textures: specific surface detail where the subject calls for it",
	}
	if openAIImagesPromptHasQuotedText(prompt) {
		lines = append(lines, "Text (verbatim): render quoted text from the primary request exactly, with the same letters, case, and punctuation, and no extra glyphs")
	}
	lines = append(lines,
		constraints,
		"Avoid: muddy or directionless light, accidental extra text, and visual clutter the request did not ask for",
	)
	return strings.Join(lines, "\n")
}
