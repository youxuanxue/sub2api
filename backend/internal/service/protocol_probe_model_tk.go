package service

import (
	"net/http"
	"sort"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const protocolProbeMaxModels = 6

// protocolProbeModelCandidates returns a deterministic, bounded set of text
// models. A protocol probe may advance only after model-specific evidence;
// transient/auth failures do not fan out across the set.
func protocolProbeModelCandidates(account *Account) []string {
	if account == nil || protocolRoutingAccountHasNoTextModels(account) {
		return []string{openai.DefaultTestModel}
	}
	mapping := account.GetModelMapping()
	candidates := make([]string, 0, len(mapping))
	seen := make(map[string]struct{}, len(mapping)+2)
	for _, upstream := range mapping {
		upstream = strings.TrimSpace(upstream)
		if upstream == "" || strings.Contains(upstream, "*") || protocolProbeModelIsNonText(upstream) {
			continue
		}
		if _, ok := seen[upstream]; ok {
			continue
		}
		seen[upstream] = struct{}{}
		candidates = append(candidates, upstream)
	}
	sort.Strings(candidates)
	if len(candidates) == 0 {
		candidates = append(candidates, openai.DefaultTestModel)
		seen[openai.DefaultTestModel] = struct{}{}
	}
	if account.Platform == PlatformNewAPI && account.ChannelType == newapiconstant.ChannelTypeAli {
		for index, model := range candidates {
			if strings.HasPrefix(strings.ToLower(model), "qwen") {
				candidates = append([]string{model}, append(candidates[:index], candidates[index+1:]...)...)
				break
			}
		}
	}
	if isCloudwiseRelayAccount(account) {
		probeModel := openAICloudwiseRelayProtocolProbeModel()
		if _, ok := seen[probeModel]; !ok {
			if len(candidates) >= protocolProbeMaxModels {
				candidates = candidates[:protocolProbeMaxModels-1]
			}
			candidates = append(candidates, probeModel)
		}
	}
	if len(candidates) > protocolProbeMaxModels {
		candidates = candidates[:protocolProbeMaxModels]
	}
	return candidates
}

// selectProtocolProbeModel preserves the historical single-model helper for
// callers/tests that only need the deterministic first representative.
func selectProtocolProbeModel(account *Account) string {
	return protocolProbeModelCandidates(account)[0]
}

func protocolProbeModelSpecificHTTPFailure(status int, body []byte) bool {
	if status < http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError {
		return false
	}
	normalized := normalizeModelNotFoundBody(body)
	if normalized == "" || !strings.Contains(normalized, "model") {
		return false
	}
	for _, marker := range []string{
		"model not found",
		"unknown model",
		"invalid model",
		"unsupported model",
		"model is not supported",
		"model was not found",
		"model is unavailable",
		"model is not available",
		"model does not exist",
		"does not support responses",
		"does not support messages",
		"does not support chat",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func protocolProbeShouldTryNextModel(
	account *Account,
	protocol protocolrouter.Protocol,
	probeModel string,
	status int,
	body []byte,
	verdict ProtocolProbeVerdict,
	hasNext bool,
) bool {
	if !hasNext {
		return false
	}
	if verdict == ProtocolProbeModelSpecific {
		return true
	}
	return protocol == protocolrouter.ProtocolMessages &&
		status == http.StatusUnauthorized &&
		isCloudwiseRelayAccount(account) &&
		protocolProbeCloudwiseModelRoutingUnauthorized(body) &&
		!strings.EqualFold(strings.TrimSpace(probeModel), openAICloudwiseRelayProtocolProbeModel())
}

func protocolProbeCloudwiseModelRoutingUnauthorized(body []byte) bool {
	normalized := normalizeModelNotFoundBody(body)
	if normalized == "" || !strings.Contains(normalized, "model") {
		return false
	}
	for _, marker := range []string{"route", "provider", "unavailable", "not available", "not supported"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func protocolRoutingAccountHasNoTextModels(account *Account) bool {
	if account == nil {
		return false
	}
	if isNewAPIXRTokenAccount(account) {
		return true
	}
	mapping := account.GetModelMapping()
	if len(mapping) == 0 {
		return false
	}
	declared := 0
	for _, model := range mapping {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		declared++
		if !protocolProbeModelIsNonText(model) {
			return false
		}
	}
	return declared > 0
}

// protocolProbeModelIsNonText excludes models whose canonical runtime catalog
// mode belongs to another endpoint family. The active pricing registry is the
// model-modality SSOT; conservative name checks cover private upstream IDs that
// are not present in that registry yet.
func protocolProbeModelIsNonText(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return false
	}
	if snapshot := loadTKPricingOverlaySnapshot(); snapshot != nil {
		if pricing := snapshot.Models[normalized]; pricing != nil {
			switch strings.ToLower(strings.TrimSpace(pricing.Mode)) {
			case "embedding", "image", "video", "audio", "rerank":
				return true
			}
		}
	}
	return protocolProbeModelIsMedia(normalized) ||
		strings.Contains(normalized, "embedding") ||
		strings.Contains(normalized, "rerank") ||
		strings.HasPrefix(normalized, "bge-") ||
		strings.HasPrefix(normalized, "tts-") ||
		strings.HasPrefix(normalized, "whisper-")
}

func protocolProbeModelIsMedia(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return isOpenAIImageGenerationModel(model) ||
		isGrokVideoGenerationModel(model) ||
		antigravity.IsImageModel(model) ||
		strings.Contains(model, "seedance") ||
		strings.Contains(model, "seedream") ||
		strings.HasPrefix(model, "dall-e") ||
		strings.HasPrefix(model, "sora") ||
		strings.HasPrefix(model, "veo-") ||
		strings.HasPrefix(model, "imagen-")
}
