package service

import (
	"sort"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// selectProtocolProbeModel selects one deterministic representative text model
// shared by Chat Completions, Responses, and Messages capability probes.
func selectProtocolProbeModel(account *Account) string {
	if account == nil || protocolRoutingAccountHasNoTextModels(account) {
		return openai.DefaultTestModel
	}
	mapping := account.GetModelMapping()
	candidates := make([]string, 0, len(mapping))
	for _, upstream := range mapping {
		upstream = strings.TrimSpace(upstream)
		if upstream == "" || strings.Contains(upstream, "*") || protocolProbeModelIsNonText(upstream) {
			continue
		}
		candidates = append(candidates, upstream)
	}
	if len(candidates) == 0 {
		return openai.DefaultTestModel
	}
	sort.Strings(candidates)
	if account.Platform == PlatformNewAPI && account.ChannelType == newapiconstant.ChannelTypeAli {
		for _, model := range candidates {
			if strings.HasPrefix(strings.ToLower(model), "qwen") {
				return model
			}
		}
	}
	return candidates[0]
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
