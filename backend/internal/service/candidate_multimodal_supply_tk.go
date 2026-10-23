package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
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
	multimodalInputModalityAudio        = "audio"
)

// TkUnsupportedInputModalityMessage is the client-facing OpenRouter-aligned copy.
func TkUnsupportedInputModalityMessage(model, modality string) string {
	modality = normalizeUnsupportedInputModality(modality)
	model = strings.TrimSpace(model)
	if model == "" {
		return fmt.Sprintf("No available endpoints support input %s", modality)
	}
	return fmt.Sprintf("No available endpoints support input %s for model: %s", modality, model)
}

// UnsupportedInputModalityFromError extracts "video" / "image" / "audio" from a wrapped
// ErrUnsupportedInputModality. Unknown or missing tokens default to video so
// legacy callers keep the OpenRouter-aligned shape.
func UnsupportedInputModalityFromError(err error) string {
	if err == nil || !errors.Is(err, ErrUnsupportedInputModality) {
		return multimodalInputModalityVideo
	}
	msg := err.Error()
	const prefix = "unsupported input modality: "
	rest, ok := strings.CutPrefix(msg, prefix)
	if !ok {
		return multimodalInputModalityVideo
	}
	if fields := strings.Fields(rest); len(fields) > 0 {
		return normalizeUnsupportedInputModality(fields[0])
	}
	return multimodalInputModalityVideo
}

// requestInputModalityPreference names the request's blocked input modality for
// empty-pool upgrades. Video wins when both are present (same order as
// accountInputModalityRejection). Ungoverned / text-only contexts fall back to
// video so the client message stays concrete.
func requestInputModalityPreference(ctx context.Context) string {
	req, ok := ProtocolRoutingRequest(ctx)
	if !ok {
		return multimodalInputModalityVideo
	}
	kinds := req.Profile().ContentKinds
	if kinds&protocolrouter.ContentVideo != 0 {
		return multimodalInputModalityVideo
	}
	if kinds&protocolrouter.ContentImage != 0 {
		return multimodalInputModalityImage
	}
	if kinds&protocolrouter.ContentAudio != 0 {
		return multimodalInputModalityAudio
	}
	return multimodalInputModalityVideo
}

func normalizeUnsupportedInputModality(modality string) string {
	switch strings.ToLower(strings.TrimSpace(modality)) {
	case multimodalInputModalityImage:
		return multimodalInputModalityImage
	case multimodalInputModalityAudio:
		return multimodalInputModalityAudio
	default:
		return multimodalInputModalityVideo
	}
}

// accountInputModalityRejection returns the blocked modality name ("video" /
// "image" / "audio") when this account is a known-negative for the request's input
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
	if kinds&protocolrouter.ContentAudio != 0 && supplyKnownNegativeForInputAudio(account, model) {
		return multimodalInputModalityAudio
	}
	return ""
}

func supplyKnownNegativeForInputAudio(account *Account, model string) bool {
	return normalizeMultimodalSupplyModelID(model) == "kimi-k3" &&
		isNewAPIVolcEngineAgentPlanAccount(account)
}

// supplyKnownNegativeForInputVideo reports evidence-backed "this supply cannot
// consume input video for this client model". Absence from the table is not a
// negative — callers must keep unknown supplies eligible.
func supplyKnownNegativeForInputVideo(account *Account, model string) bool {
	normalized := normalizeMultimodalSupplyModelID(model)
	if normalized == "" {
		return false
	}
	switch normalized {
	case "kimi-k3":
		// NVIDIA Build: "At most 0 video(s)".
		// DashScope pay-as-you-go, probe 2026-10-23 cn-prod #135:
		// "Video inputs are not supported by this model". Image stayed servable.
		return isNewAPINVIDIABuildAccount(account) || isNewAPIDashScopeAccount(account)
	case "glm-5.3-flash":
		return isNewAPINVIDIABuildAccount(account)
	case "glm-4.5-air", "glm-5.2", "glm-5.3":
		// Probe 2026-10-08: Volc Agent Plan "Model only support text input".
		if isNewAPIVolcEngineAgentPlanAccount(account) {
			return true
		}
		// Qianfan: glm-4.5-air only among this set (glm-5.2/5.3 not hard-proven there).
		return normalized == "glm-4.5-air" && isNewAPIQianfanTokenPlanAccount(account)
	case "gpt-5", "gpt-5-mini", "gpt-5.1", "gpt-5.2":
		// Probe 2026-10-08 tokensea: Invalid value 'video_url' (image_url OK).
		return isNewAPITokenseaRelayAccount(account)
	case "deepseek-flash":
		return isNewAPIAliTokenPlanAccount(account)
	case "deepseek-v4.1-flash":
		// Ali Token Plan: "does not support video input".
		// DashScope pay-as-you-go, probe 2026-10-23 cn-prod #135:
		// "The current model does not support video input". Image stayed servable.
		// The deepseek-flash alias has no DashScope evidence and stays off this host.
		return isNewAPIAliTokenPlanAccount(account) || isNewAPIDashScopeAccount(account)
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
		return false
	case "glm-4.5-air", "glm-5.2", "glm-5.3":
		// Probe 2026-10-08: Volc Agent Plan
		// "Model only support text input" for image_url.
		// Probe 2026-10-15: Ali GLM-5.3 Responses explicitly rejects images;
		// Chat returns 200 but sees no image (Qwen positive control passed).
		// See the dated evidence in the multimodal supply SSOT.
		if normalized == "glm-5.3" && isNewAPIAliTokenPlanAccount(account) {
			return true
		}
		if isNewAPIVolcEngineAgentPlanAccount(account) {
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

// isNewAPIDashScopeAccount reports Bailian pay-as-you-go
// (dashscope.aliyuncs.com). Ali MaaS Token Plan shares ChannelTypeAli and is
// identified separately by isNewAPIAliTokenPlanAccount.
func isNewAPIDashScopeAccount(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformNewAPI &&
		account.ChannelType == newapiconstant.ChannelTypeAli &&
		isDashScopePayAsYouGoBaseURL(account.GetBaseURL())
}

func isDashScopePayAsYouGoBaseURL(base string) bool {
	base = strings.TrimSpace(base)
	if base == "" {
		return false
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "dashscope.aliyuncs.com")
}

func normalizeMultimodalSupplyModelID(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if i := strings.IndexByte(model, '['); i > 0 {
		model = model[:i]
	}
	return strings.TrimSpace(model)
}

// isNewAPITokenseaRelayAccount reports newapi accounts whose upstream host is
// agent.tokensea.ai (prod inventory uses PlatformNewAPI, not PlatformOpenAI).
func isNewAPITokenseaRelayAccount(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformNewAPI &&
		isTokenseaRelayBaseURL(account.GetBaseURL())
}

// wrapUnsupportedInputModality builds the stable sentinel + modality + model.
func wrapUnsupportedInputModality(model, modality string) error {
	modality = normalizeUnsupportedInputModality(modality)
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
