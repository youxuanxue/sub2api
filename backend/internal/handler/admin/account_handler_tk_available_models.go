package admin

import (
	"context"
	"net/http"
	"sort"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
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

// tkRespondAvailableModels dispatches admin available-models by account platform.
func (h *AccountHandler) tkRespondAvailableModels(c *gin.Context, account *service.Account) {
	// Handle OpenAI accounts
	if account.IsOpenAI() {
		// OpenAI 自动透传会绕过常规模型改写，测试/模型列表也应回落到默认模型集。
		if account.IsOpenAIPassthroughEnabled() {
			response.Success(c, tkOpenAIAdminDefaultModels(c.Request.Context()))
			return
		}

		mapping := account.GetModelMapping()
		if len(mapping) == 0 {
			response.Success(c, tkOpenAIAdminDefaultModels(c.Request.Context()))
			return
		}

		response.Success(c, tkOpenAIAdminModelsForIDs(sortedModelMappingKeys(mapping)))
		return
	}

	// Handle Gemini accounts via runtime model_mapping SSOT (includes Google One defaults).
	if account.IsGemini() {
		response.Success(c, tkGeminiAdminAvailableModels(c.Request.Context(), account))
		return
	}

	// Handle Antigravity accounts: live servable set (same SSOT as /antigravity/models).
	if account.Platform == service.PlatformAntigravity {
		response.Success(c, tkAntigravityAdminDefaultModels(c.Request.Context(), account))
		return
	}

	// Handle fifth platform `newapi` accounts.
	// newapi accounts route OpenAI-compatible payloads through the new-api adaptor
	// pool, so the model space is whatever the upstream channel exposes. The admin
	// UI has a dedicated probe (POST /api/v1/admin/channel-types/fetch-upstream-models)
	// for live model discovery; this endpoint must NOT fall through to the Claude
	// catalog. We mirror openai's behavior: prefer model_mapping keys when set,
	// otherwise return an empty list (the UI shows "configure model_mapping" hint).
	if account.Platform == service.PlatformNewAPI {
		mapping := account.GetModelMapping()
		if len(mapping) == 0 {
			if tkRespondNewAPIAgentPlanAvailableModelsWhenMappingEmpty(c, account) {
				return
			}
			ids, err := h.adminService.GetAccountModelMappingPresetIDs(
				c.Request.Context(),
				service.PlatformNewAPI,
				account.ChannelType,
			)
			if err != nil {
				response.Error(c, http.StatusInternalServerError, "failed to resolve model mapping preset")
				return
			}
			if len(ids) > 0 {
				sort.Strings(ids)
				response.Success(c, tkAdminModelOptionsForIDs(ids))
				return
			}
			response.Success(c, []dto.AccountModelOption{})
			return
		}
		response.Success(c, tkAdminModelOptionsForIDs(sortedModelMappingKeys(mapping)))
		return
	}

	if account.IsGrok() {
		if !accountHasExplicitModelMapping(account) {
			response.Success(c, tkGrokAdminDefaultModels(c.Request.Context()))
			return
		}
		mapping := account.GetModelMapping()
		if len(mapping) == 0 {
			response.Success(c, tkGrokAdminDefaultModels(c.Request.Context()))
			return
		}
		// Deterministic order: pin the grok chat-probe default first, then
		// alphabetical. Iterating the mapping map directly is non-deterministic
		// and makes the admin selector (and its test) flaky.
		ids := make([]string, 0, len(mapping))
		for requestedModel := range mapping {
			ids = append(ids, requestedModel)
		}
		sort.SliceStable(ids, func(i, j int) bool {
			if ids[i] == service.GrokDefaultTestModelID {
				return true
			}
			if ids[j] == service.GrokDefaultTestModelID {
				return false
			}
			return ids[i] < ids[j]
		})
		response.Success(c, tkGrokAdminModelsForIDs(ids))
		return
	}

	if account.IsKiro() || account.IsKiroMirrorStub() {
		response.Success(c, tkClaudeModelsToAdminOptions(service.KiroAdminTestModels(), nil))
		return
	}

	// Handle Claude/Anthropic accounts
	// For OAuth and Setup-Token accounts: return default models
	if account.IsOAuth() {
		response.Success(c, tkClaudeAdminDefaultModels(c.Request.Context()))
		return
	}

	// For API Key accounts: return models based on model_mapping
	mapping := account.GetModelMapping()
	if len(mapping) == 0 {
		// No mapping configured, return default models
		response.Success(c, tkClaudeAdminDefaultModels(c.Request.Context()))
		return
	}

	response.Success(c, tkClaudeAdminModelsForIDs(sortedModelMappingKeys(mapping)))

}
