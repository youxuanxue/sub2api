package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OpenRouterProviderModel is the subset of OpenRouter provider /v1/models schema
// TokenKey emits for onboarding and monitoring.
type OpenRouterProviderModel struct {
	ID                string                        `json:"id"`
	Name              string                        `json:"name"`
	Created           int64                         `json:"created"`
	InputModalities   []string                      `json:"input_modalities"`
	OutputModalities  []string                      `json:"output_modalities"`
	ContextLength     int                           `json:"context_length"`
	MaxOutputLength   int                           `json:"max_output_length,omitempty"`
	Pricing           OpenRouterProviderModelPricing `json:"pricing"`
	SupportedFeatures []string                      `json:"supported_features,omitempty"`
	IsReady           bool                          `json:"is_ready"`
	CapacityTPM       *int64                        `json:"capacity_tpm,omitempty"`
	OpenRouter        map[string]string             `json:"openrouter,omitempty"`
	Datacenters       []OpenRouterProviderDatacenter `json:"datacenters,omitempty"`
}

type OpenRouterProviderModelPricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
}

type OpenRouterProviderDatacenter struct {
	CountryCode string `json:"country_code"`
}

type OpenRouterProviderModelsResponse struct {
	Data []OpenRouterProviderModel `json:"data"`
}

func formatOpenRouterUSDPerToken(value float64) string {
	if value <= 0 {
		return "0"
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func openRouterProviderEffectiveMultiplier(group *Group, userMultiplier float64) float64 {
	if group == nil {
		return userMultiplier
	}
	mult := group.RateMultiplier
	if mult <= 0 {
		mult = 1
	}
	if userMultiplier > 0 {
		mult *= userMultiplier
	}
	if group.PeakRateEnabled {
		if peak := group.PeakMultiplierAt(time.Now()); peak > 0 {
			mult *= peak
		}
	}
	return mult
}

func (s *GatewayService) BuildOpenRouterProviderCatalog(
	ctx context.Context,
	cfg OpenRouterProviderConfig,
) (*OpenRouterProviderModelsResponse, error) {
	if !cfg.Enabled {
		return nil, fmt.Errorf("openrouter provider config disabled")
	}
	if len(cfg.GroupIDs) == 0 {
		return nil, fmt.Errorf("openrouter provider config: group_ids required")
	}
	if s == nil || s.groupRepo == nil {
		return nil, fmt.Errorf("gateway service not configured")
	}

	type modelEntry struct {
		publicID string
		sourceID string
		groupID  int64
	}
	seen := make(map[string]modelEntry)
	order := make([]string, 0)

	for _, groupID := range cfg.GroupIDs {
		if groupID <= 0 {
			continue
		}
		group, err := s.groupRepo.GetByID(ctx, groupID)
		if err != nil {
			return nil, fmt.Errorf("get group %d: %w", groupID, err)
		}
		if group == nil {
			return nil, fmt.Errorf("group %d not found", groupID)
		}
		platform := strings.TrimSpace(group.Platform)
		if platform == "" {
			continue
		}
		models := s.GetAvailableModels(ctx, &groupID, platform)
		for _, modelID := range models {
			modelID = strings.TrimSpace(modelID)
			if modelID == "" {
				continue
			}
			publicID := cfg.PublicModelID(modelID)
			if _, ok := seen[publicID]; ok {
				continue
			}
			seen[publicID] = modelEntry{publicID: publicID, sourceID: modelID, groupID: groupID}
			order = append(order, publicID)
		}
	}

	sort.Strings(order)
	created := time.Now().Unix()
	out := make([]OpenRouterProviderModel, 0, len(order))

	for _, publicID := range order {
		entry := seen[publicID]
		group, err := s.groupRepo.GetByID(ctx, entry.groupID)
		if err != nil || group == nil {
			return nil, fmt.Errorf("reload group %d: %w", entry.groupID, err)
		}

		userMult := 1.0
		if cfg.BillingUserID > 0 {
			userMult = s.ResolveUserGroupRateMultiplier(ctx, cfg.BillingUserID, entry.groupID, group.RateMultiplier)
		}
		effectiveMult := openRouterProviderEffectiveMultiplier(group, userMult)
		if effectiveMult <= 0 {
			effectiveMult = 1
		}

		promptUSD := 0.0
		completionUSD := 0.0
		if s.billingService != nil {
			if pricing, err := s.billingService.GetModelPricing(entry.sourceID); err == nil && pricing != nil {
				promptUSD = pricing.InputPricePerToken * effectiveMult
				completionUSD = pricing.OutputPricePerToken * effectiveMult
			}
		}
		if promptUSD <= 0 && completionUSD <= 0 {
			continue
		}

		maxOut := int(math.Min(float64(cfg.DefaultContextLen/4), 128000))
		if maxOut <= 0 {
			maxOut = 8192
		}

		item := OpenRouterProviderModel{
			ID:               publicID,
			Name:             publicID,
			Created:          created,
			InputModalities:  []string{"text"},
			OutputModalities: []string{"text"},
			ContextLength:    cfg.DefaultContextLen,
			MaxOutputLength:  maxOut,
			Pricing: OpenRouterProviderModelPricing{
				Prompt:     formatOpenRouterUSDPerToken(promptUSD),
				Completion: formatOpenRouterUSDPerToken(completionUSD),
			},
			SupportedFeatures: []string{"tools", "json_mode", "reasoning"},
			IsReady:           true,
			CapacityTPM:       cfg.CapacityTPM,
			OpenRouter: map[string]string{
				"slug": publicID,
			},
			Datacenters: []OpenRouterProviderDatacenter{{CountryCode: "US"}},
		}
		out = append(out, item)
	}

	return &OpenRouterProviderModelsResponse{Data: out}, nil
}

func (s *SettingService) GetOpenRouterProviderConfig(ctx context.Context) (OpenRouterProviderConfig, error) {
	if s == nil || s.settingRepo == nil {
		return DefaultOpenRouterProviderConfig(), nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyTKOpenRouterProviderConfig)
	if err != nil {
		return OpenRouterProviderConfig{}, err
	}
	return ParseOpenRouterProviderConfig(raw)
}
