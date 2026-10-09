package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

// ErrUnsupportedInputModality reports that every authorized supply for the
// requested model is evidence-known not to accept the request's input modality
// (e.g. kimi-k3 + video on NVIDIA Build only). Caller fault → HTTP 400, not a
// capacity 429. Message deliberately omits "no available accounts" so ops
// capacity classifiers do not relabel it.
// Owner: docs/approved/multimodal-supply-capability-ssot.md
var ErrUnsupportedInputModality = errors.New("unsupported input modality")

// TkUnsupportedInputModalityErrType is the OpenAI-compat / Responses envelope
// type/code for modality-empty pools (parity with unsupported-model).
const TkUnsupportedInputModalityErrType = "invalid_request_error"

const (
	openAICompatIneligibleInputModality = "input_modality_unsupported"
	multimodalInputModalityVideo        = "video"
	multimodalInputModalityImage        = "image"
)

// TkUnsupportedInputModalityMessage is the client-facing OpenRouter-aligned copy.
func TkUnsupportedInputModalityMessage(model, modality string) string {
	modality = strings.TrimSpace(modality)
	if modality == "" {
		modality = multimodalInputModalityVideo
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return fmt.Sprintf("No available endpoints support input %s", modality)
	}
	return fmt.Sprintf("No available endpoints support input %s for model: %s", modality, model)
}

// accountInputModalityRejection returns the blocked modality name ("video" /
// "image") when this account is a known-negative for the request's input
// modalities, else "". Video is checked before image when both are present so
// the empty-pool message names a concrete modality. Empty string means admit
// (unknown supplies stay eligible).
func accountInputModalityRejection(ctx context.Context, account *Account, model string) string {
	if account == nil {
		return ""
	}
	req, ok := ProtocolRoutingRequest(ctx)
	if !ok {
		return ""
	}
	kinds := req.Profile().ContentKinds
	if kinds&protocolrouter.ContentVideo != 0 && supplyKnownNegativeForInputVideo(account, model) {
		return multimodalInputModalityVideo
	}
	if kinds&protocolrouter.ContentImage != 0 && supplyKnownNegativeForInputImage(account, model) {
		return multimodalInputModalityImage
	}
	return ""
}

// supplyKnownNegativeForInputVideo reports evidence-backed "this supply cannot
// consume input video for this client model". Absence from the table is not a
// negative — callers must keep unknown supplies eligible.
func supplyKnownNegativeForInputVideo(account *Account, model string) bool {
	// Probe 2026-10-09 OpenAI edge mirrors (Responses): Invalid value
	// 'input_video' for every sampled model (gpt-5.4…gpt-6.1, image-2.5,
	// codex-auto-review). Image remains eligible except model-specific rows.
	if isOpenAIEdgeMirrorAccount(account) {
		return true
	}
	normalized := normalizeMultimodalSupplyModelID(model)
	if normalized == "" {
		return false
	}
	switch normalized {
	case "kimi-k3":
		// Probe 2026-10-08: NVIDIA Build rejects video_url ("At most 0 video(s)");
		// Volc Agent Plan / china-edge relay servable with video.
		return isNewAPINVIDIABuildAccount(account)
	case "glm-5.3-flash":
		// Probe 2026-10-08 direct upstream: NVIDIA Build "At most 0 video(s)";
		// Volc / china-edge servable with video.
		return isNewAPINVIDIABuildAccount(account)
	case "glm-4.5-air", "glm-5.2", "glm-5.3":
		// Probe 2026-10-08: Volc Agent Plan + china-edge relay
		// "Model only support text input" for video (and image).
		if isNewAPIVolcEngineAgentPlanAccount(account) || isNewAPIChinaEdgeRelayAccount(account) {
			return true
		}
		// Qianfan: glm-4.5-air only among this set (glm-5.2/5.3 not hard-proven there).
		return normalized == "glm-4.5-air" && isNewAPIQianfanTokenPlanAccount(account)
	case "gpt-5", "gpt-5-mini", "gpt-5.1", "gpt-5.2":
		// Probe 2026-10-08 tokensea: Invalid value 'video_url' (image_url OK).
		return isNewAPITokenseaRelayAccount(account)
	case "deepseek-flash", "deepseek-v4.1-flash":
		// Probe 2026-10-08 Ali Token Plan: "does not support video input"
		// (same models accept video on Volc / china-edge).
		return isNewAPIAliTokenPlanAccount(account)
	case "qwen3.7-max":
		// Probe 2026-10-08 Ali Token Plan: Unexpected item type in content
		// for both video_url and image_url.
		return isNewAPIAliTokenPlanAccount(account)
	case "deepseek-v4-flash-0731", "deepseek-v4-pro", "deepseek-v4-pro-0813", "glm-4.7":
		// Probe 2026-10-08 Qianfan Token Plan:
		// "Invalid content type. video_url is only supported by certain models".
		return isNewAPIQianfanTokenPlanAccount(account)
	default:
		return false
	}
}

// supplyKnownNegativeForInputImage reports evidence-backed "this supply cannot
// consume input image for this client model". Absence is not a negative.
func supplyKnownNegativeForInputImage(account *Account, model string) bool {
	normalized := normalizeMultimodalSupplyModelID(model)
	if normalized == "" {
		return false
	}
	switch normalized {
	case "gpt-5.3-codex-spark":
		// Probe 2026-10-09 OpenAI edge: model does not support image input.
		return isOpenAIEdgeMirrorAccount(account)
	case "glm-4.5-air", "glm-5.2", "glm-5.3":
		// Probe 2026-10-08: Volc Agent Plan + china-edge relay
		// "Model only support text input" for image_url.
		// Note: same models are image-servable on Ali Token Plan.
		if isNewAPIVolcEngineAgentPlanAccount(account) || isNewAPIChinaEdgeRelayAccount(account) {
			return true
		}
		return normalized == "glm-4.5-air" && isNewAPIQianfanTokenPlanAccount(account)
	case "qwen3.7-max":
		// Probe 2026-10-08 Ali Token Plan: Unexpected item type in content.
		return isNewAPIAliTokenPlanAccount(account)
	case "deepseek-v4-flash-0731", "deepseek-v4-pro", "deepseek-v4-pro-0813", "glm-4.7":
		// Probe 2026-10-08 Qianfan Token Plan:
		// "Invalid content type. image_url is only supported by certain models".
		return isNewAPIQianfanTokenPlanAccount(account)
	default:
		return false
	}
}

func normalizeMultimodalSupplyModelID(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if i := strings.IndexByte(model, '['); i > 0 {
		model = model[:i]
	}
	return strings.TrimSpace(model)
}

// isNewAPIChinaEdgeRelayAccount reports prod→edge newapi mirror stubs
// (china-us*) whose credentials.base_url is api-<edge>.tokenkey.dev.
// Same GLM text-only known-negatives as Volc Agent Plan on those models.
func isNewAPIChinaEdgeRelayAccount(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformNewAPI &&
		MirrorStubEdgeID(account) != ""
}

// isNewAPITokenseaRelayAccount reports newapi accounts whose upstream host is
// agent.tokensea.ai (prod inventory uses PlatformNewAPI, not PlatformOpenAI).
func isNewAPITokenseaRelayAccount(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformNewAPI &&
		isTokenseaRelayBaseURL(account.GetBaseURL())
}

// isOpenAIEdgeMirrorAccount reports prod→edge OpenAI/Codex mirror stubs
// (openai-us*) whose credentials.base_url is api-<edge>.tokenkey.dev.
func isOpenAIEdgeMirrorAccount(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformOpenAI &&
		MirrorStubEdgeID(account) != ""
}

// wrapUnsupportedInputModality builds the stable sentinel + modality + model.
func wrapUnsupportedInputModality(model, modality string) error {
	modality = strings.TrimSpace(modality)
	if modality == "" {
		modality = multimodalInputModalityVideo
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return fmt.Errorf("%w: %s", ErrUnsupportedInputModality, modality)
	}
	return fmt.Errorf("%w: %s model=%s", ErrUnsupportedInputModality, modality, model)
}

// openAIFilterOnlyReason reports whether every filtered candidate was dropped
// for exactly one named reason (used for modality-empty pools).
func openAIFilterOnlyReason(stats openAISelectionFilterStats, reason string) bool {
	if stats.pool <= 0 || reason == "" || stats.reasons == nil {
		return false
	}
	n := stats.reasons[reason]
	if n <= 0 {
		return false
	}
	total := 0
	for _, count := range stats.reasons {
		total += count
	}
	return n == total && total == stats.pool
}
