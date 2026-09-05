package admin

import (
	"context"
	"sort"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Admin available-models always crosses the HTTP boundary as one minimal DTO;
// platform package model types remain internal metadata sources.
func tkAdminModelOptions[T any](models []T, fields func(T) (string, string)) []dto.AccountModelOption {
	out := make([]dto.AccountModelOption, 0, len(models))
	for _, model := range models {
		id, displayName := fields(model)
		if displayName == "" {
			displayName = id
		}
		out = append(out, dto.AccountModelOption{ID: id, DisplayName: displayName})
	}
	return out
}

func tkAdminModelOptionsForIDs(ids []string) []dto.AccountModelOption {
	out := make([]dto.AccountModelOption, 0, len(ids))
	for _, id := range ids {
		out = append(out, dto.AccountModelOption{ID: id, DisplayName: id})
	}
	return out
}

func sortedModelMappingKeys(mapping map[string]string) []string {
	ids := make([]string, 0, len(mapping))
	for id := range mapping {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func tkOpenAIAdminModelsForIDs(ids []string) []dto.AccountModelOption {
	return tkAdminModelOptions(openai.ModelsForIDs(ids), func(model openai.Model) (string, string) {
		return model.ID, model.DisplayName
	})
}

func tkOpenAIAdminDefaultModels(ctx context.Context) []dto.AccountModelOption {
	ids := service.ServableClientFacingIDs(ctx, service.PlatformOpenAI, nil, nil)
	defaultModelID := openai.DefaultModels[0].ID
	sort.SliceStable(ids, func(i, j int) bool {
		if ids[i] == defaultModelID || ids[j] == defaultModelID {
			return ids[i] == defaultModelID && ids[j] != defaultModelID
		}
		return ids[i] < ids[j]
	})
	return tkOpenAIAdminModelsForIDs(ids)
}

func tkGrokAdminModelsForIDs(ids []string) []dto.AccountModelOption {
	return tkAdminModelOptions(xai.ModelsForIDs(ids), func(model xai.Model) (string, string) {
		return model.ID, model.DisplayName
	})
}

func tkGrokAdminDefaultModels(ctx context.Context) []dto.AccountModelOption {
	ids := service.ServableClientFacingIDs(ctx, service.PlatformGrok, nil, nil)
	sort.SliceStable(ids, func(i, j int) bool {
		if ids[i] == service.GrokDefaultTestModelID {
			return true
		}
		if ids[j] == service.GrokDefaultTestModelID {
			return false
		}
		return ids[i] < ids[j]
	})
	return tkGrokAdminModelsForIDs(ids)
}

func accountHasExplicitModelMapping(account *service.Account) bool {
	if account == nil {
		return false
	}
	switch rawMapping := account.Credentials["model_mapping"].(type) {
	case map[string]any:
		return len(rawMapping) > 0
	case map[string]string:
		return len(rawMapping) > 0
	default:
		return false
	}
}

func tkGeminiAdminModelsForIDs(ids []string) []dto.AccountModelOption {
	return tkAdminModelOptions(geminicli.ModelsForIDs(ids), func(model geminicli.Model) (string, string) {
		return model.ID, model.DisplayName
	})
}

func tkGeminiAdminDefaultModels(ctx context.Context) []dto.AccountModelOption {
	return tkGeminiAdminModelsForIDs(
		service.ServableClientFacingIDs(ctx, service.PlatformGemini, nil, nil),
	)
}

func tkGeminiAdminAvailableModels(ctx context.Context, account *service.Account) []dto.AccountModelOption {
	mapping := account.GetModelMapping()
	if len(mapping) == 0 {
		return tkGeminiAdminDefaultModels(ctx)
	}
	// Google One mapping is already a conservative whitelist; do not intersect
	// with the global servable catalog (gemini-2.0-flash may be menu-hidden).
	if account.IsGeminiGoogleOne() {
		ids := make([]string, 0, len(mapping))
		for requestedModel := range mapping {
			ids = append(ids, requestedModel)
		}
		sort.Strings(ids)
		return tkGeminiAdminModelsForIDs(ids)
	}
	return tkGeminiAdminModelsForMapping(ctx, mapping)
}

func tkGeminiAdminModelsForMapping(ctx context.Context, mapping map[string]string) []dto.AccountModelOption {
	if len(mapping) == 0 {
		return tkGeminiAdminDefaultModels(ctx)
	}

	servable := service.ServableClientFacingIDs(ctx, service.PlatformGemini, nil, nil)
	servableSet := make(map[string]struct{}, len(servable))
	for _, id := range servable {
		servableSet[id] = struct{}{}
	}

	ids := make([]string, 0, len(mapping))
	for requestedModel := range mapping {
		if len(servableSet) > 0 {
			if _, ok := servableSet[requestedModel]; !ok {
				continue
			}
		}
		ids = append(ids, requestedModel)
	}
	sort.Strings(ids)
	return tkGeminiAdminModelsForIDs(ids)
}

// tkAntigravityAdminDefaultModels returns admin account-test models from the unified
// antigravity servable set, intersected with the account whitelist (mapAntigravityModel).
// Matches gateway tkAntigravityDefaultModels; DefaultModels only supplies display metadata.
func tkAntigravityAdminDefaultModels(ctx context.Context, account *service.Account) []dto.AccountModelOption {
	defaults := antigravity.DefaultModels()
	byID := make(map[string]antigravity.ClaudeModel, len(defaults))
	for _, m := range defaults {
		byID[m.ID] = m
	}
	ids := service.ServableClientFacingIDs(ctx, service.PlatformAntigravity, nil, nil)
	sort.SliceStable(ids, func(i, j int) bool {
		if ids[i] == service.AntigravityDefaultTestModelID {
			return true
		}
		if ids[j] == service.AntigravityDefaultTestModelID {
			return false
		}
		return ids[i] < ids[j]
	})
	out := make([]dto.AccountModelOption, 0, len(ids))
	for _, id := range ids {
		if account != nil && service.MapAntigravityModel(account, id) == "" {
			continue
		}
		if m, ok := byID[id]; ok {
			out = append(out, dto.AccountModelOption{ID: id, DisplayName: m.DisplayName})
			continue
		}
		out = append(out, dto.AccountModelOption{ID: id, DisplayName: id})
	}
	return out
}

func tkClaudeModelsToAdminOptions(models []claude.Model, ids []string) []dto.AccountModelOption {
	out := tkAdminModelOptions(models, func(model claude.Model) (string, string) {
		return model.ID, model.DisplayName
	})
	if len(ids) == len(out) {
		for i := range out {
			out[i].ID = ids[i]
		}
	}
	return out
}

func tkClaudeAdminModelsForIDs(ids []string) []dto.AccountModelOption {
	return tkClaudeModelsToAdminOptions(claude.ModelsForIDs(ids), ids)
}

func tkClaudeAdminDefaultModels(ctx context.Context) []dto.AccountModelOption {
	ids := service.ServableClientFacingIDs(ctx, service.PlatformAnthropic, nil, nil)
	return tkClaudeModelsToAdminOptions(claude.ModelsForIDs(ids), nil)
}
