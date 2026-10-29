package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

var ErrUnsupportedModel = errors.New("unsupported model")

const TkUnsupportedModelErrType = "invalid_request_error"

const (
	TkUnsupportedRequestErrType = "invalid_request_error"
	TkUnsupportedRequestCode    = "unsupported_request"
)

// TkUnsupportedRequestMessage is the client copy when entitled accounts exist
// but none can serve this request. It must not say the service is busy.
func TkUnsupportedRequestMessage(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "Unsupported request"
	}
	return "Unsupported request for model: " + model
}

// TkUnsupportedModelMessage builds the client-facing message for an unserveable
// model name (single source so path A and path B never drift).
func TkUnsupportedModelMessage(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "Unsupported model"
	}
	return "Unsupported model: " + model
}

// tkSelectionFailedDueToUnsupportedModel reports whether a failed account
// selection was caused PURELY by "no account serves this model name", as opposed
// to a transient capacity gap.
//
// It is true only when at least one platform-matched candidate lacks the model
// and every account is either model-unsupported or filtered for another
// platform. Any current or future availability category leaves the total
// unmatched, so the caller conservatively falls back to ErrNoAvailableAccounts.
func tkSelectionFailedDueToUnsupportedModel(stats selectionFailureStats) bool {
	return stats.ModelUnsupported > 0 &&
		stats.ModelUnsupported+stats.PlatformFiltered == stats.Total
}

// tkWrapSelectionFailure classifies terminal account-selection failures. It returns:
//   - ErrNoAvailableAccounts as-is when no model was requested;
//   - an Anthropic namespace error only when the resolved scheduling platform is
//     Anthropic; Gemini/Antigravity model names must not be judged as claude-* names;
//   - ErrUnsupportedModel (with the model name + stats) when the failure is
//     purely an unsupported model name (caller fault → handler maps to HTTP 400);
//   - otherwise the original ErrNoAvailableAccounts wrapped with the model + stats
//     (transient capacity → 429), preserving prior behavior.
func tkWrapSelectionFailure(platform, requestedModel string, stats selectionFailureStats) error {
	if requestedModel == "" {
		return ErrNoAvailableAccounts
	}
	if err := tkDeprecatedAnthropicSelectionFailure(requestedModel); err != nil {
		return err
	}
	if platform == PlatformAnthropic {
		if err := tkAnthropicCrossVendorSelectionFailure(requestedModel); err != nil {
			return err
		}
	}
	if tkSelectionFailedDueToUnsupportedModel(stats) {
		return fmt.Errorf("%w: %s (%s)", ErrUnsupportedModel, requestedModel, summarizeSelectionFailureStats(stats))
	}
	if stats.ChannelRestricted > 0 {
		return fmt.Errorf("%w supporting model: %s (channel pricing restriction; %s)", ErrNoAvailableAccounts, requestedModel, summarizeSelectionFailureStats(stats))
	}
	return fmt.Errorf("%w supporting model: %s (%s)", ErrNoAvailableAccounts, requestedModel, summarizeSelectionFailureStats(stats))
}

// tkAnthropicCrossVendorSelectionFailure wraps ErrUnsupportedModel when the
// requested model name is outside the Anthropic claude-* namespace (e.g. bare
// "gpt", "opus", deepseek-*). Mirrors tkDeprecatedAnthropicSelectionFailure:
// beats mixed model_unsupported + unschedulable stats and load-batch bare
// ErrNoAvailableAccounts so wrong-model traffic never surfaces routing 429.
//
// Prod 2026-07-07 (user_id=16, api_key_id=121): claude-group direct key hammering
// model=gpt on /v1/messages via load-batch empty-pool returned routing/platform 429
// even though #1255/#1257 fixed retired ids only.
func tkAnthropicCrossVendorSelectionFailure(requestedModel string) error {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return nil
	}
	if tkIsForwardableAnthropicModelName(requestedModel) {
		return nil
	}
	// Dual-stack CloudWise anthropic accounts (#94) advertise glm/kimi/minimax/
	// deepseek via model_mapping prefixes. Ingress must not 400 those names
	// before selection; empty-mapping Anthropic OAuth still cannot claim them
	// because isModelSupportedByAccount keeps the claude-* namespace guard.
	if openAICloudwiseRelaySupportsRequestedModel(requestedModel) {
		return nil
	}
	// Dual-stack tokensea anthropic accounts (#93) advertise the mapped
	// subset of agent.tokensea.ai via model_mapping. Ingress
	// must not 400 those exact IDs before selection; official Anthropic
	// OAuth/APIKey still cannot claim them because isModelSupportedByAccount
	// keeps the claude-* namespace guard unless IsAnthropicTokenseaRelay.
	if tokenseaRelaySupportsRequestedModel(requestedModel) {
		return nil
	}
	if normalized := claude.NormalizeModelID(requestedModel); normalized != "" && normalized != requestedModel {
		if tkIsForwardableAnthropicModelName(normalized) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s (anthropic namespace)", ErrUnsupportedModel, requestedModel)
}

// TkIsAnthropicCrossVendorModelName reports whether reqModel is outside the
// Anthropic claude-* namespace. Exported for handler ingress pre-checks.
func TkIsAnthropicCrossVendorModelName(requestedModel string) bool {
	return tkAnthropicCrossVendorSelectionFailure(requestedModel) != nil
}

func tkIsForwardableAnthropicModelName(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return true // empty model name is out of this guard's scope (handled elsewhere)
	}
	return strings.HasPrefix(m, "claude-")
}

func tkIsForwardableOpenAIModelName(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "models/")
	m = strings.TrimPrefix(m, "openai/")
	m = strings.ReplaceAll(m, " ", "")
	if m == "" {
		return true
	}
	openAIPrefixes := []string{
		"gpt",
		"o1", "o2", "o3", "o4", "o5", "o6", "o7", "o8", "o9",
		"chatgpt",
		"codex",
		"text-embedding",
		"text-moderation",
		"dall-e",
		"tts",
		"whisper",
		"omni",
		"computer-use",
		"sora",
	}
	for _, prefix := range openAIPrefixes {
		if strings.HasPrefix(m, prefix) {
			return true
		}
	}
	return false
}
