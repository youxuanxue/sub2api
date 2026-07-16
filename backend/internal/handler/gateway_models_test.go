package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayModelsAccountRepoStub struct {
	service.AccountRepository

	byGroup map[int64][]service.Account
	all     []service.Account
}

type gatewayModelsResponseForTest struct {
	Object string                    `json:"object"`
	Data   []gatewayModelItemForTest `json:"data"`
}

type gatewayModelItemForTest struct {
	ID                      string                                `json:"id"`
	Object                  string                                `json:"object"`
	Created                 int64                                 `json:"created"`
	OwnedBy                 string                                `json:"owned_by"`
	CreatedAt               string                                `json:"created_at"`
	SupportsReasoningEffort bool                                  `json:"supportsReasoningEffort"`
	ReasoningEffort         string                                `json:"reasoningEffort"`
	ReasoningEfforts        []gatewayReasoningEffortOptionForTest `json:"reasoningEfforts"`
}

type gatewayReasoningEffortOptionForTest struct {
	Value   string `json:"value"`
	Label   string `json:"label"`
	Default bool   `json:"default"`
}

func (s *gatewayModelsAccountRepoStub) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]service.Account, error) {
	accounts, ok := s.byGroup[groupID]
	if !ok {
		return nil, nil
	}
	out := make([]service.Account, len(accounts))
	copy(out, accounts)
	return out, nil
}

func (s *gatewayModelsAccountRepoStub) ListSchedulable(ctx context.Context) ([]service.Account, error) {
	out := make([]service.Account, len(s.all))
	copy(out, s.all)
	return out, nil
}

func newGatewayModelsHandlerForTest(repo service.AccountRepository) *GatewayHandler {
	return &GatewayHandler{
		gatewayService: service.NewGatewayService(
			repo,
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			nil,
		),
	}
}

type gatewayModelsUserRepoStub struct {
	service.UserRepository
	user *service.User
}

func (s gatewayModelsUserRepoStub) GetByID(context.Context, int64) (*service.User, error) {
	return s.user, nil
}

type gatewayModelsGroupRepoStub struct {
	service.GroupRepository
	groups []service.Group
}

func (s gatewayModelsGroupRepoStub) ListActive(context.Context) ([]service.Group, error) {
	out := make([]service.Group, len(s.groups))
	copy(out, s.groups)
	return out, nil
}

type gatewayModelsSubscriptionRepoStub struct {
	service.UserSubscriptionRepository
}

func (gatewayModelsSubscriptionRepoStub) ListActiveByUserID(context.Context, int64) ([]service.UserSubscription, error) {
	return nil, nil
}

func TestGatewayModels_UniversalKeyListsEntitledGroupUnion(t *testing.T) {
	gin.SetMode(gin.TestMode)

	openAIGroupID := int64(31)
	geminiGroupID := int64(32)
	unentitledGroupID := int64(99)
	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				openAIGroupID: {
					{ID: 1, Platform: service.PlatformOpenAI},
				},
				geminiGroupID: {
					{ID: 2, Platform: service.PlatformGemini},
				},
				unentitledGroupID: {
					{
						ID:       3,
						Platform: service.PlatformOpenAI,
						Credentials: map[string]any{
							"model_mapping": map[string]any{
								"leaked-global-model": "leaked-global-model",
							},
						},
					},
				},
			},
			all: []service.Account{
				{
					ID:       3,
					Platform: service.PlatformOpenAI,
					Credentials: map[string]any{
						"model_mapping": map[string]any{
							"leaked-global-model": "leaked-global-model",
						},
					},
				},
			},
		},
	)
	h.apiKeyService = service.NewAPIKeyService(
		nil,
		gatewayModelsUserRepoStub{user: &service.User{ID: 16, Status: service.StatusActive, AllowedGroups: []int64{openAIGroupID, geminiGroupID}}},
		gatewayModelsGroupRepoStub{groups: []service.Group{
			{ID: openAIGroupID, Name: "gpt", Platform: service.PlatformOpenAI, IsExclusive: true, Status: service.StatusActive},
			{ID: geminiGroupID, Name: "google", Platform: service.PlatformGemini, IsExclusive: true, Status: service.StatusActive},
			{ID: unentitledGroupID, Name: "other", Platform: service.PlatformOpenAI, IsExclusive: true, Status: service.StatusActive},
		}},
		gatewayModelsSubscriptionRepoStub{},
		nil,
		nil,
		nil,
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		UserID:      16,
		RoutingMode: service.RoutingModeUniversal,
		User:        &service.User{ID: 16, Status: service.StatusActive},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	ids := modelIDsForTest(got.Data)
	require.ElementsMatch(t, gatewayModelsSSOTUnionForTest(
		service.ServableClientFacingIDs(context.Background(), service.PlatformOpenAI, nil, nil),
		service.ServableClientFacingIDs(context.Background(), service.PlatformGemini, nil, nil),
	), ids, "universal list must be exactly the entitled OpenAI+Gemini SSOT union and must not scan the global schedulable pool")
}

func TestGatewayModels_GeminiGroupFallsBackToGeminiModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(20)
	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				groupID: {
					{ID: 1, Platform: service.PlatformGemini},
				},
			},
		},
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{ID: groupID, Platform: service.PlatformGemini},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "list", got.Object)
	require.ElementsMatch(t,
		service.ServableClientFacingIDs(context.Background(), service.PlatformGemini, nil, nil),
		modelIDsForTest(got.Data),
		"Gemini group fallback must mirror the Gemini servable SSOT")
}

func TestGatewayModels_Grok45AdvertisesReasoningEffortForGrokBuild(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(4409)
	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				groupID: {
					{
						ID:       1,
						Platform: service.PlatformGrok,
						Credentials: map[string]any{
							"model_mapping": map[string]any{"grok-4.5": "grok-4.5"},
						},
					},
				},
			},
		},
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{ID: groupID, Platform: service.PlatformGrok},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)
	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Data, 1)
	model := got.Data[0]
	require.Equal(t, "grok-4.5", model.ID)
	require.True(t, model.SupportsReasoningEffort)
	require.Equal(t, "high", model.ReasoningEffort)
	require.Equal(t, []gatewayReasoningEffortOptionForTest{
		{Value: "low", Label: "Low"},
		{Value: "medium", Label: "Medium"},
		{Value: "high", Label: "High", Default: true},
	}, model.ReasoningEfforts)
}

func TestGatewayModels_GeminiGroupFiltersMappedModelsByPlatform(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(21)
	anthropicModel := firstNSSOTIDsForGatewayModelsTest(t, service.PlatformAnthropic, 1)[0]
	geminiModel := firstNSSOTIDsForGatewayModelsTest(t, service.PlatformGemini, 1)[0]
	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				groupID: {
					{
						ID:       1,
						Platform: service.PlatformAnthropic,
						Credentials: map[string]any{
							"model_mapping": map[string]any{
								anthropicModel: anthropicModel,
							},
						},
					},
					{
						ID:       2,
						Platform: service.PlatformGemini,
						Credentials: map[string]any{
							"model_mapping": map[string]any{
								geminiModel: geminiModel,
							},
						},
					},
				},
			},
		},
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{ID: groupID, Platform: service.PlatformGemini},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, []string{geminiModel}, modelIDsForTest(got.Data))
}

func TestGatewayModels_CustomModelsListDisabledKeepsOriginalModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(22)
	openAIModels := firstNSSOTIDsForGatewayModelsTest(t, service.PlatformOpenAI, 2)
	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				groupID: {
					{
						ID:       1,
						Platform: service.PlatformOpenAI,
						Credentials: map[string]any{
							"model_mapping": anyMappingFromGatewayModelIDs(openAIModels),
						},
					},
				},
			},
		},
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformOpenAI,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: false,
				Models:  []string{openAIModels[0]},
			},
		},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.ElementsMatch(t, openAIModels, modelIDsForTest(got.Data))
}

func TestGatewayModels_CustomModelsListFiltersAndOrdersMappedModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(23)
	openAIModels := firstNSSOTIDsForGatewayModelsTest(t, service.PlatformOpenAI, 2)
	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				groupID: {
					{
						ID:       1,
						Platform: service.PlatformOpenAI,
						Credentials: map[string]any{
							"model_mapping": anyMappingFromGatewayModelIDs(append(append([]string{}, openAIModels...), "legacy-gpt-2024")),
						},
					},
				},
			},
		},
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformOpenAI,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: true,
				Models:  []string{openAIModels[1], "missing-model", openAIModels[0]},
			},
		},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, []string{openAIModels[1], openAIModels[0]}, modelIDsForTest(got.Data))
}

func TestGatewayModels_CustomModelsListKeepsConcreteModelAllowedByWildcardMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(26)
	anthropicModel := firstNSSOTIDsForGatewayModelsTest(t, service.PlatformAnthropic, 1)[0]
	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				groupID: {
					{
						ID:       1,
						Platform: service.PlatformAnthropic,
						Credentials: map[string]any{
							"model_mapping": map[string]any{
								"claude-*": anthropicModel,
							},
						},
					},
				},
			},
		},
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformAnthropic,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: true,
				Models:  []string{anthropicModel},
			},
		},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, []string{anthropicModel}, modelIDsForTest(got.Data))
}

func TestGatewayModels_AnthropicCustomModelsListIncludesOAuthClaudeAndMappedDeepSeek(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(28)
	anthropicModels := firstNSSOTIDsForGatewayModelsTest(t, service.PlatformAnthropic, 2)
	mappedNewAPIModel := firstNewAPIManifestIDForGatewayModelsTest(t)
	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				groupID: {
					{
						ID:       1,
						Platform: service.PlatformAnthropic,
						Type:     service.AccountTypeOAuth,
					},
					{
						ID:       2,
						Platform: service.PlatformAnthropic,
						Type:     service.AccountTypeAPIKey,
						Credentials: map[string]any{
							"model_mapping": map[string]any{
								mappedNewAPIModel: mappedNewAPIModel,
							},
						},
					},
				},
			},
		},
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformAnthropic,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: true,
				Models:  []string{anthropicModels[0], anthropicModels[1], mappedNewAPIModel},
			},
		},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, []string{anthropicModels[0], anthropicModels[1], mappedNewAPIModel}, modelIDsForTest(got.Data))
}

func TestGatewayModels_AnthropicCustomModelsListDisabledKeepsMappedModelList(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(29)
	mappedNewAPIModel := firstNewAPIManifestIDForGatewayModelsTest(t)
	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				groupID: {
					{
						ID:       1,
						Platform: service.PlatformAnthropic,
						Type:     service.AccountTypeOAuth,
					},
					{
						ID:       2,
						Platform: service.PlatformAnthropic,
						Type:     service.AccountTypeAPIKey,
						Credentials: map[string]any{
							"model_mapping": map[string]any{
								mappedNewAPIModel: mappedNewAPIModel,
							},
						},
					},
				},
			},
		},
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformAnthropic,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: false,
				Models:  []string{"claude-not-used-while-disabled", mappedNewAPIModel},
			},
		},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, []string{mappedNewAPIModel}, modelIDsForTest(got.Data))
}

func TestGatewayModels_AnthropicCustomModelsListIncludesOAuthClaudeWithoutMappings(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(30)
	anthropicModels := firstNSSOTIDsForGatewayModelsTest(t, service.PlatformAnthropic, 2)
	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				groupID: {
					{
						ID:       1,
						Platform: service.PlatformAnthropic,
						Type:     service.AccountTypeOAuth,
					},
				},
			},
		},
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformAnthropic,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: true,
				Models:  anthropicModels,
			},
		},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, anthropicModels, modelIDsForTest(got.Data))
}

func TestGatewayModels_CustomModelsListCanReturnEmptyWhenSelectionsUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(24)
	openAIModels := firstNSSOTIDsForGatewayModelsTest(t, service.PlatformOpenAI, 2)
	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				groupID: {
					{
						ID:       1,
						Platform: service.PlatformOpenAI,
						Credentials: map[string]any{
							"model_mapping": map[string]any{
								openAIModels[0]: openAIModels[0],
							},
						},
					},
				},
			},
		},
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformOpenAI,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: true,
				Models:  []string{openAIModels[1]},
			},
		},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Empty(t, modelIDsForTest(got.Data))
}

func TestGatewayModels_CustomModelsListFiltersDefaultFallbackModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(25)
	openAIModels := firstNSSOTIDsForGatewayModelsTest(t, service.PlatformOpenAI, 3)
	requestedModels := append(append([]string{}, openAIModels...), "codex-auto-review", "gpt-image-2", "legacy-gpt-2024", "gpt-not-a-real-id-zzz")
	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				groupID: {
					{ID: 1, Platform: service.PlatformOpenAI},
				},
			},
		},
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformOpenAI,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: true,
				// codex-auto-review is an internal capability, not directly
				// selectable (2026-07 SSOT audit #5) — it must be filtered out
				// here just like the other non-servable ids, even though the
				// admin explicitly listed it.
				Models: requestedModels,
			},
		},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, openAIModels, modelIDsForTest(got.Data))
}

func TestGatewayModels_OpenAICustomModelsListKeepsOpenAIResponseShapeForDefaultFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(27)
	openAIModels := firstNSSOTIDsForGatewayModelsTest(t, service.PlatformOpenAI, 2)
	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				groupID: {
					{ID: 1, Platform: service.PlatformOpenAI},
				},
			},
		},
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformOpenAI,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: true,
				Models:  openAIModels,
			},
		},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, openAIModels, modelIDsForTest(got.Data))
	require.Equal(t, "model", got.Data[0].Object)
	require.NotZero(t, got.Data[0].Created)
	require.Equal(t, "openai", got.Data[0].OwnedBy)
	require.Empty(t, got.Data[0].CreatedAt)
}

func modelIDsForTest(models []gatewayModelItemForTest) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func gatewayModelsSSOTUnionForTest(groups ...[]string) []string {
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, id := range group {
			seen[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out
}

func firstNSSOTIDsForGatewayModelsTest(t *testing.T, platform string, n int) []string {
	t.Helper()
	ids := service.ServableClientFacingIDs(context.Background(), platform, nil, nil)
	require.GreaterOrEqual(t, len(ids), n, "platform %s SSOT must have enough ids for this test", platform)
	out := make([]string, n)
	copy(out, ids[:n])
	return out
}

func firstNewAPIManifestIDForGatewayModelsTest(t *testing.T) string {
	t.Helper()
	ids := service.AccountModelMappingPresetIDs(context.Background(), service.PlatformNewAPI, newapiconstant.ChannelTypeDeepSeek, nil)
	require.NotEmpty(t, ids, "newapi manifest SSOT must expose at least one mapped id for this test")
	return ids[0]
}

func anyMappingFromGatewayModelIDs(ids []string) map[string]any {
	out := make(map[string]any, len(ids))
	for _, id := range ids {
		out[id] = id
	}
	return out
}
