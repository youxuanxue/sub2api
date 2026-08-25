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
	if account == nil || protocolRoutingAccountIsMediaOnly(account) {
		return openai.DefaultTestModel
	}
	mapping := account.GetModelMapping()
	candidates := make([]string, 0, len(mapping))
	for _, upstream := range mapping {
		upstream = strings.TrimSpace(upstream)
		if upstream == "" || strings.Contains(upstream, "*") || protocolProbeModelIsMedia(upstream) {
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

func protocolRoutingAccountIsMediaOnly(account *Account) bool {
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
		if !protocolProbeModelIsMedia(model) {
			return false
		}
	}
	return declared > 0
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
