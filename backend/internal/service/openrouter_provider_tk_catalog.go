package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const openRouterProviderSchemaVersion = "2.4"

// OpenRouterProviderModel is the OpenRouter provider /v1/models document (schema 2.4).
// Only the OR seller surface emits this shape; TokenKey customer gateway paths are unchanged.
type OpenRouterProviderModel struct {
	SchemaVersion    string                             `json:"schema_version"`
	ID               string                             `json:"id"`
	Name             string                             `json:"name"`
	Description      string                             `json:"description,omitempty"`
	Created          int64                              `json:"created"`
	Quantization     string                             `json:"quantization"`
	InputModalities  []OpenRouterProviderInputModality  `json:"input_modalities"`
	OutputModalities []OpenRouterProviderOutputModality `json:"output_modalities"`
	IsReady          bool                               `json:"is_ready"`
	OpenRouter       map[string]string                  `json:"openrouter,omitempty"`
	Datacenters      []OpenRouterProviderDatacenter     `json:"datacenters,omitempty"`
}

type OpenRouterProviderInputModality struct {
	Type            string                            `json:"type"`
	SupportedInputs map[string]any                    `json:"supported_inputs,omitempty"`
	Pricing         []OpenRouterProviderPriceEntry    `json:"pricing,omitempty"`
	Capacity        []OpenRouterProviderCapacityEntry `json:"capacity,omitempty"`
}

type OpenRouterProviderOutputModality struct {
	Type                string                                       `json:"type"`
	MaxLength           *OpenRouterProviderQuantity                  `json:"max_length,omitempty"`
	Streaming           *bool                                        `json:"streaming,omitempty"`
	SupportedParameters map[string]OpenRouterProviderParamDescriptor `json:"supported_parameters,omitempty"`
	Pricing             []OpenRouterProviderPriceEntry               `json:"pricing,omitempty"`
	Capacity            []OpenRouterProviderCapacityEntry            `json:"capacity,omitempty"`
}

type OpenRouterProviderQuantity struct {
	Value int    `json:"value"`
	Unit  string `json:"unit,omitempty"`
}

type OpenRouterProviderParamDescriptor struct {
	Type     string   `json:"type"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	Unit     string   `json:"unit,omitempty"`
	MaxItems *int     `json:"max_items,omitempty"`
	Values   []any    `json:"values,omitempty"`
	Default  any      `json:"default,omitempty"`
}

type OpenRouterProviderPriceEntry struct {
	Type      string                            `json:"type"`
	Unit      string                            `json:"unit"`
	CostUSD   string                            `json:"cost_usd"`
	UTCStart  *int                              `json:"utc_start,omitempty"`
	UTCEnd    *int                              `json:"utc_end,omitempty"`
	Overrides []OpenRouterProviderPriceOverride `json:"overrides,omitempty"`
}

type OpenRouterProviderPriceOverride struct {
	When    map[string]any `json:"when"`
	CostUSD string         `json:"cost_usd"`
}

type OpenRouterProviderCapacityEntry struct {
	Type  string `json:"type"`
	Unit  string `json:"unit"`
	Per   string `json:"per,omitempty"`
	Value int64  `json:"value"`
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
			if cfg.CatalogExcluded(modelID) {
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

		item := openRouterProviderBuildModelDocument(
			cfg,
			group,
			metaPtr,
			publicID,
			entry.sourceID,
			slug,
			created,
			promptUSD,
			completionUSD,
			cacheReadUSD,
			baseMult,
		)
		openRouterProviderEnrichCatalogItem(&item, cfg, entry.sourceID)
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
