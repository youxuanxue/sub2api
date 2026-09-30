package service

import "strings"

const openAIImagesStyleGuidancePrefix = "Style guidance:"

// applyOpenAIImagesStyleGuidance appends codex2api-style "Style guidance: ..." when
// the client sent style. Idempotent if a Style guidance block is already present.
// Style is intentionally NOT written to tools[].style — ChatGPT OAuth often ignores
// that tool field while honouring prompt text.
func applyOpenAIImagesStyleGuidance(prompt, style string) string {
	style = strings.TrimSpace(style)
	if style == "" {
		return prompt
	}
	if strings.Contains(strings.ToLower(prompt), strings.ToLower(openAIImagesStyleGuidancePrefix)) {
		return prompt
	}
	prompt = strings.TrimSpace(prompt)
	block := openAIImagesStyleGuidancePrefix + " " + style
	if prompt == "" {
		return block
	}
	return prompt + "\n\n" + block
}
