package service

import (
	"fmt"
	"os"
	"regexp"
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
	openAIImagesVerbatimReasoningEffort = openAIImagesDefaultReasoningEffort

	openAIImagesPerceivedQualityPrimary = "Primary request:"
)

func openAIImagesReasoningEffortForPrompt(prompt string) string {
	if effort, ok := openAIImagesExplicitReasoningEffort(); ok {
		return effort
	}
	// Quoted labels use the same medium effort as other prompts. Production
	// traffic showed higher latency without a stable quality gain from high;
	// exact text remains a prompt-scaffold instruction, not an effort upgrade.
	if openAIImagesPromptHasShortASCIIQuotedLabel(prompt) {
		return openAIImagesVerbatimReasoningEffort
	}
	return openAIImagesDefaultReasoningEffort
}

// openAIImagesExplicitReasoningEffort reports a valid env override.
// Unset or unrecognized values are not an override. Aliases match the
// gateway effort normalizer; Velmo's mode name max maps to xhigh because
// the Codex image allowlist has no reasoning effort "max".
func openAIImagesExplicitReasoningEffort() (string, bool) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(openAIImagesReasoningEffortEnv)))
	if raw == "" {
		return "", false
	}
	raw = strings.NewReplacer("-", "", "_", "", " ", "").Replace(raw)
	switch raw {
	case "none", "minimal", "low", "medium", "high":
		return raw, true
	case "xhigh", "extrahigh":
		return "xhigh", true
	case "max":
		return "xhigh", true
	default:
		return "", false
	}
}

func openAIImagesPromptScaffoldEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(openAIImagesPromptScaffoldEnv))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// Short matched pairs only. {1,48} is a rune count in Go's regexp.
// Unbalanced marks (a single “ or a 12" measurement) do not match.
var (
	openAIImagesASCIIQuotedLabelRE = regexp.MustCompile(`"([^"\r\n]{1,48})"`)
	openAIImagesCJKQuotedSpanREs   = []*regexp.Regexp{
		regexp.MustCompile(`“([^”\r\n]{1,48})”`),
		regexp.MustCompile(`「([^」\r\n]{1,48})」`),
		regexp.MustCompile(`『([^』\r\n]{1,48})』`),
	}
)

func openAIImagesPromptHasShortASCIIQuotedLabel(prompt string) bool {
	for _, loc := range openAIImagesASCIIQuotedLabelRE.FindAllStringSubmatchIndex(prompt, -1) {
		if len(loc) < 4 || loc[2] < 0 || loc[3] < loc[2] {
			continue
		}
		// 12" and 12" to 24" are measurements, not labels to render.
		if loc[0] > 0 && prompt[loc[0]-1] >= '0' && prompt[loc[0]-1] <= '9' {
			continue
		}
		if strings.TrimSpace(prompt[loc[2]:loc[3]]) == "" {
			continue
		}
		return true
	}
	return false
}

func openAIImagesPromptHasShortQuotedSpan(prompt string) bool {
	if openAIImagesPromptHasShortASCIIQuotedLabel(prompt) {
		return true
	}
	for _, re := range openAIImagesCJKQuotedSpanREs {
		for _, match := range re.FindAllStringSubmatch(prompt, -1) {
			if len(match) > 1 && strings.TrimSpace(match[1]) != "" {
				return true
			}
		}
	}
	return false
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

	var composition, lighting, materials, constraints, avoid string
	if edit {
		// Do not ask an edit to invent a new frame or light. That restyles
		// the source image instead of changing only what the request names.
		composition = "Composition/framing: keep the input image's framing unless the request changes it"
		lighting = "Lighting/mood: keep the input image's light unless the request changes it"
		materials = "Materials/textures: keep existing surfaces unless the request changes them"
		constraints = "Constraints: preserve parts of the input image the request does not ask to change; keep the requested subject and any quoted text; do not add unrelated objects, captions, or watermarks"
		avoid = "Avoid: changing regions, light, or surfaces the request did not name; accidental extra text; and visual clutter the request did not ask for"
	} else {
		composition = "Composition/framing: if the request does not already specify framing, use a clear focal subject and uncluttered negative space; do not override an explicit framing instruction"
		lighting = "Lighting/mood: if the request does not already specify light, use a coherent direction and atmosphere; do not add light the request forbids"
		materials = "Materials/textures: if the request does not already specify surfaces, keep subject texture specific; do not restyle an explicit medium"
		constraints = "Constraints: keep the requested subject and any quoted text; do not add unrelated objects, captions, or watermarks"
		avoid = "Avoid: muddy or directionless light, accidental extra text, and visual clutter the request did not ask for"
	}

	lines := []string{
		openAIImagesPerceivedQualityPrimary + " " + prompt,
		composition,
		lighting,
		materials,
	}
	if openAIImagesPromptHasShortQuotedSpan(prompt) {
		lines = append(lines, "Text (verbatim): render the short quoted span from the primary request exactly, with the same letters, case, and punctuation, and no extra glyphs")
	}
	lines = append(lines, constraints, avoid)
	return strings.Join(lines, "\n")
}

// applyOpenAIImagesNativeQualityGuidance adds the quality intent that the
// Codex Images backend can observe in the prompt. Its JSON quality field is
// soft and the response metadata remains authoritative for the actual tier.
func applyOpenAIImagesNativeQualityGuidance(prompt, quality string) string {
	quality = strings.ToLower(strings.TrimSpace(quality))
	var guidance string
	switch quality {
	case "high":
		guidance = "Rendering fidelity: use the highest supported image-generation quality for the final image; preserve fine local detail, natural material texture, clean edges, and smooth tonal transitions. Do not produce a draft or simplified rendering."
	case "medium":
		guidance = "Rendering fidelity: use a balanced production-quality rendering with clear subject detail, natural material texture, clean edges, and coherent lighting."
	case "low":
		guidance = "Rendering fidelity: use a fast draft rendering while preserving the requested subject, composition, colors, and text exactly."
	default:
		return prompt
	}
	if strings.Contains(prompt, guidance) {
		return prompt
	}
	if strings.TrimSpace(prompt) == "" {
		return guidance
	}
	return strings.TrimSpace(prompt) + "\n" + guidance
}

// applyOpenAIImagesNativeSizeGuidance gives the native backend a deterministic
// aspect-ratio and canvas hint. Strict local canvas post-processing still owns
// the final customer dimensions.
func applyOpenAIImagesNativeSizeGuidance(prompt string, parsed *OpenAIImagesRequest) string {
	if parsed == nil || !parsed.ExplicitSize {
		return prompt
	}
	size := strings.TrimSpace(upstreamOpenAIImagesSize(parsed))
	parts := strings.SplitN(size, "x", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return prompt
	}
	width, height := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	guidance := fmt.Sprintf("Generate the image with a width of %s pixels and a height of %s pixels. Use this exact canvas aspect ratio and a fully framed composition.", width, height)
	if strings.Contains(prompt, guidance) {
		return prompt
	}
	if strings.TrimSpace(prompt) == "" {
		return guidance
	}
	return strings.TrimSpace(prompt) + "\n" + guidance
}
