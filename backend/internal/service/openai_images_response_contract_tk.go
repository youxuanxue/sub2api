package service

import (
	"fmt"
	"strings"
)

// openAIImagesWantCount is the client-visible image count for the Images API
// contract (default 1, capped at 4). Multi-fetch paths already use this bound
// when planning upstream calls ("前截断"); single-shot Responses replies can
// still over-produce image_generation_call items, so callers also truncate
// after collection ("后截断").
func openAIImagesWantCount(parsed *OpenAIImagesRequest) int {
	if parsed == nil || parsed.N < 1 {
		return 1
	}
	if parsed.N > 4 {
		return 4
	}
	return parsed.N
}

// truncateOpenAIImagesResults keeps at most want images after upstream
// collection. Prefer this over dropping mid-stream: one Codex Responses reply
// may emit multiple image_generation_call items even when tools[].n is unset
// and TokenKey only issued a single upstream request (N=1).
func truncateOpenAIImagesResults(results []openAIResponsesImageResult, want int) []openAIResponsesImageResult {
	if want < 1 {
		want = 1
	}
	if len(results) > want {
		return results[:want]
	}
	return results
}

// applyOpenAIImagesClientResponseEcho overlays client-requested Images envelope
// fields onto upstream tool metadata so the public response echoes the request
// (quality/model/…) instead of Codex soft-serve values like quality=auto or
// model=gpt-image-2-codex.
func applyOpenAIImagesClientResponseEcho(meta *openAIResponsesImageResult, parsed *OpenAIImagesRequest, requestModel string) {
	if meta == nil {
		return
	}
	if parsed != nil {
		if q := strings.TrimSpace(parsed.Quality); q != "" {
			meta.Quality = q
		}
		if bg := strings.TrimSpace(parsed.Background); bg != "" {
			meta.Background = bg
		}
		if of := strings.TrimSpace(parsed.OutputFormat); of != "" {
			meta.OutputFormat = of
		}
		if sz := strings.TrimSpace(parsed.Size); sz != "" {
			meta.Size = sz
		}
	}
	if m := strings.TrimSpace(requestModel); m != "" {
		meta.Model = m
		return
	}
	if parsed != nil {
		if m := strings.TrimSpace(parsed.Model); m != "" {
			meta.Model = m
		}
	}
}

// stripOpenAIImagesRevisedPrompts removes revised_prompt from GPT Image
// responses. Official gpt-image-* Images API responses do not return it;
// Codex Responses often does, which SandBase flags as a model-mismatch hint.
func stripOpenAIImagesRevisedPrompts(results []openAIResponsesImageResult) {
	for i := range results {
		results[i].RevisedPrompt = ""
	}
}

// prepareOpenAIImagesAPIResponseResults applies post-collection contract fixes:
// truncate to n, strip revised_prompt, echo client envelope onto firstMeta.
func prepareOpenAIImagesAPIResponseResults(
	results []openAIResponsesImageResult,
	firstMeta *openAIResponsesImageResult,
	parsed *OpenAIImagesRequest,
	requestModel string,
) []openAIResponsesImageResult {
	results = truncateOpenAIImagesResults(results, openAIImagesWantCount(parsed))
	stripOpenAIImagesRevisedPrompts(results)
	applyOpenAIImagesClientResponseEcho(firstMeta, parsed, requestModel)
	return results
}

// OpenAIImagesInputFidelityError is returned when the client sets
// input_fidelity on a model the official Images API rejects (gpt-image-2).
type OpenAIImagesInputFidelityError struct {
	Model string
	Value string
}

func (e OpenAIImagesInputFidelityError) Error() string {
	return fmt.Sprintf("invalid value: '%s' for 'input_fidelity'; model %s does not support input_fidelity", e.Value, e.Model)
}

// ClientMessage is the OpenAI-shaped body text for Images HTTP 400 responses.
func (e OpenAIImagesInputFidelityError) ClientMessage() string {
	return fmt.Sprintf("Invalid value: '%s' for 'input_fidelity'. The model %s does not support input_fidelity", e.Value, e.Model)
}

// validateOpenAIImagesInputFidelity rejects input_fidelity on models the
// official Images API / Codex soft-serve refuse (gpt-image-2 family), so the
// gateway returns 400 instead of silently omitting the field and succeeding.
func validateOpenAIImagesInputFidelity(req *OpenAIImagesRequest) error {
	if req == nil || !req.ExplicitInputFidelity {
		return nil
	}
	model := strings.TrimSpace(req.Model)
	if !openAIImagesCodexSoftModelRejectsInputFidelity(model) {
		return nil
	}
	value := strings.TrimSpace(req.InputFidelity)
	if value == "" {
		value = "null"
	}
	return OpenAIImagesInputFidelityError{Model: model, Value: value}
}
