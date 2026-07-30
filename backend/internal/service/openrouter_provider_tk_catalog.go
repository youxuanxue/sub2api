package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OpenRouterProviderModel is the subset of OpenRouter provider /v1/models schema
// TokenKey emits for onboarding and monitoring.
type OpenRouterProviderModel struct {
	ID                          string                         `json:"id"`
	Name                        string                         `json:"name"`
	Description                 string                         `json:"description,omitempty"`
	Created                     int64                          `json:"created"`
	InputModalities             []string                       `json:"input_modalities"`
	OutputModalities            []string                       `json:"output_modalities"`
	Quantization                string                         `json:"quantization"`
	ContextLength               int                            `json:"context_length"`
	MaxOutputLength             int                            `json:"max_output_length"`
	Pricing                     OpenRouterProviderModelPricing `json:"pricing"`
	SupportedSamplingParameters []string                       `json:"supported_sampling_parameters"`
	SupportedFeatures           []string                       `json:"supported_features,omitempty"`
	IsReady                     bool                           `json:"is_ready"`
	CapacityTPM                 *int64                         `json:"capacity_tpm,omitempty"`
	OpenRouter                  map[string]string              `json:"openrouter,omitempty"`
	Datacenters                 []OpenRouterProviderDatacenter `json:"datacenters,omitempty"`
}

type OpenRouterProviderModelPricing struct {
	Prompt         string                              `json:"prompt"`
	Completion     string                              `json:"completion"`
	Image          string                              `json:"image"`
	Request        string                              `json:"request"`
	InputCacheRead string                              `json:"input_cache_read"`
	Overrides      []OpenRouterProviderPricingOverride `json:"overrides,omitempty"`
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

func (s *GatewayService) openRouterProviderCatalogCandidates(ctx context.Context, group *Group) []string {
	if s == nil || group == nil {
		return nil
	}
	platform := strings.TrimSpace(group.Platform)
	if platform == "" {
		return nil
	}

	seen := make(map[string]struct{})
	order := make([]string, 0)
	modelProvider := availableModelsProvider(func(ctx context.Context, groupID *int64, platform string) []string {
		return s.GetAvailableModels(ctx, groupID, platform)
	})

	add := func(modelID string) {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			return
		}
		if _, ok := seen[modelID]; ok {
			return
		}
		if !groupServesModel(ctx, modelProvider, *group, modelID) {
			return
		}
		seen[modelID] = struct{}{}
		order = append(order, modelID)
	}

	for _, modelID := range ServableClientFacingIDs(ctx, platform, nil, s.tkPricingCatalog) {
		add(modelID)
	}
	for _, modelID := range modelProvider(ctx, &group.ID, platform) {
		add(modelID)
	}

	sort.Strings(order)
	return order
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
		for _, modelID := range s.openRouterProviderCatalogCandidates(ctx, group) {
			if openRouterProviderCatalogExcluded(modelID) {
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

	var catalogIndex map[string]PublicCatalogModel
	if s.tkPricingCatalog != nil {
		catalogIndex = openRouterProviderCatalogIndex(s.tkPricingCatalog.BuildPublicCatalog(ctx))
	}

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
		baseMult := openRouterProviderBaseMultiplier(group, userMult)

		promptUSD := 0.0
		completionUSD := 0.0
		cacheReadUSD := 0.0
		var metaPtr *PublicCatalogModel
		if meta, ok := catalogIndex[entry.sourceID]; ok {
			metaCopy := meta
			metaPtr = &metaCopy
			promptUSD = meta.Pricing.InputPer1KTokens / 1000
			completionUSD = meta.Pricing.OutputPer1KTokens / 1000
			cacheReadUSD = openRouterProviderCacheReadUSDPerToken(&meta)
		}
		if s.billingService != nil && promptUSD <= 0 && completionUSD <= 0 {
			if pricing, err := s.billingService.GetModelPricing(entry.sourceID); err == nil && pricing != nil {
				promptUSD = pricing.InputPricePerToken
				completionUSD = pricing.OutputPricePerToken
			}
		}
		if !openRouterProviderModelHasListedPrice(metaPtr, promptUSD, completionUSD) {
			continue
		}

		slug := publicID
		if strings.TrimSpace(cfg.Slug) != "" && !strings.HasPrefix(publicID, cfg.Slug+"/") {
			slug = strings.TrimSpace(cfg.Slug) + "/" + strings.TrimPrefix(publicID, cfg.ModelIDPrefix)
		}

		item := OpenRouterProviderModel{
			ID:                          publicID,
			Name:                        openRouterProviderDisplayName(cfg, publicID, metaPtr),
			Description:                 openRouterProviderDescription(cfg, publicID, metaPtr),
			Created:                     created,
			InputModalities:             openRouterProviderInputModalities(metaPtr),
			OutputModalities:            openRouterProviderOutputModalities(metaPtr),
			Quantization:                openRouterProviderDefaultQuantization,
			ContextLength:               openRouterProviderContextLength(cfg, metaPtr),
			MaxOutputLength:             openRouterProviderMaxOutputLength(cfg, metaPtr),
			Pricing:                     openRouterProviderBuildPricing(group, metaPtr, promptUSD, completionUSD, cacheReadUSD, baseMult),
			SupportedSamplingParameters: append([]string(nil), openRouterProviderDefaultSamplingParameters...),
			SupportedFeatures:           openRouterProviderSupportedFeatures(metaPtr),
			IsReady:                     true,
			CapacityTPM:                 openRouterProviderCapacityTPM(cfg, entry.sourceID),
			OpenRouter: map[string]string{
				"slug": slug,
			},
			Datacenters: openRouterProviderDatacenters(cfg),
		}
		openRouterProviderEnrichCatalogItem(&item, entry.sourceID)
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
