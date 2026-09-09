package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
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

type codexModelsResponseForTest struct {
	Models []struct {
		Slug                     string                       `json:"slug"`
		SupportedReasoningLevels []codexReasoningLevelForTest `json:"supported_reasoning_levels"`
		InputModalities          []string                     `json:"input_modalities"`
		ModelMessages            map[string]json.RawMessage   `json:"model_messages"`
		TruncationPolicy         map[string]json.RawMessage   `json:"truncation_policy"`
		AvailabilityNUX          json.RawMessage              `json:"availability_nux"`
		Upgrade                  json.RawMessage              `json:"upgrade"`
	} `json:"models"`
}

type codexReasoningLevelForTest struct {
	Effort string `json:"effort"`
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
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		),
	}
}

func TestGatewayModels_UniversalKeyListsCallableOpenAIProjection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{tkCapabilities: &us046CapabilitySource{byProtocol: map[service.UniversalProtocol][]service.UniversalCapability{
		service.UniversalProtocolOpenAI: {
			{ID: "gpt-callable", Modalities: []service.UniversalModality{service.UniversalModalityChat}},
		},
	}}}

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
	require.Equal(t, []string{"gpt-callable"}, modelIDsForTest(got.Data))
	require.Equal(t, "openai", got.Data[0].OwnedBy)
}

func TestDefaultModelIDsForCompositeIncludesAntigravityDefaults(t *testing.T) {
	antigravityIDs := defaultModelIDsForPlatform(service.PlatformAntigravity)
	require.NotEmpty(t, antigravityIDs)

	compositeIDs := defaultModelIDsForPlatform(service.PlatformComposite)
	require.Contains(t, compositeIDs, antigravityIDs[0])
}

// Scenario: Anthropic defaults contain only Claude while Antigravity keeps its own Gemini models.
func TestDefaultModelIDsForAnthropicExcludeAntigravityGemini(t *testing.T) {
	anthropicIDs := defaultModelIDsForPlatform(service.PlatformAnthropic)
	require.Contains(t, anthropicIDs, "claude-opus-4-6")
	require.NotContains(t, anthropicIDs, "gemini-2.5-flash")

	antigravityIDs := defaultModelIDsForPlatform(service.PlatformAntigravity)
	require.Contains(t, antigravityIDs, "gemini-2.5-flash")
}

// Scenario: non-OpenAI groups return a Codex manifest instead of a standard model list.
func TestGatewayCodexModels_NonOpenAIGroupsUseMappedModels(t *testing.T) {
	tests := []struct {
		name       string
		platform   string
		model      string
		efforts    []string
		modalities []string
	}{
		{
			name:       "Grok",
			platform:   service.PlatformGrok,
			model:      "grok-4.6",
			efforts:    []string{"low", "medium", "high", "xhigh"},
			modalities: []string{"text", "image"},
		},
		{
			name:       "DeepSeek",
			platform:   service.PlatformDeepseek,
			model:      "deepseek-v4-pro",
			efforts:    []string{"low", "high", "max"},
			modalities: []string{"text"},
		},
		{
			name:       "provider-qualified Claude",
			platform:   service.PlatformAnthropic,
			model:      "anthropic/claude-sonnet-4-6",
			efforts:    []string{"low", "medium", "high", "max"},
			modalities: []string{"text"},
		},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			groupID := int64(100 + index)
			h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{
				byGroup: map[int64][]service.Account{
					groupID: {
						{
							ID:       1,
							Platform: tt.platform,
							Credentials: map[string]any{
								"model_mapping": map[string]any{tt.model: tt.model},
							},
						},
					},
				},
			})

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/models?client_version=0.147.0", nil)
			c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
				Group: &service.Group{ID: groupID, Platform: tt.platform},
			})

			h.CodexModels(c)

			require.Equal(t, http.StatusOK, rec.Code)
			var got codexModelsResponseForTest
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			if tt.platform == service.PlatformGrok {
				want := service.FilterCodexModelIDsForGroup(service.ServableClientFacingIDs(context.Background(), service.PlatformGrok, nil, nil), nil)
				require.ElementsMatch(t, want, codexModelSlugsForTest(got.Models))
				for i := range got.Models {
					if got.Models[i].Slug == tt.model {
						got.Models[0], got.Models[i] = got.Models[i], got.Models[0]
						break
					}
				}
			} else {
				require.Len(t, got.Models, 1)
			}
			require.Equal(t, tt.model, got.Models[0].Slug)
			require.NotEmpty(t, got.Models[0].ModelMessages)
			require.NotEmpty(t, got.Models[0].TruncationPolicy)
			require.NotNil(t, got.Models[0].AvailabilityNUX)
			require.NotNil(t, got.Models[0].Upgrade)
			require.Equal(t, tt.efforts, codexReasoningEffortsForTest(got.Models[0].SupportedReasoningLevels))
			require.Equal(t, tt.modalities, got.Models[0].InputModalities)
		})
	}
}

// Composite manifests include defaults from unmapped accounts and explicit mappings.
func TestGatewayCodexModels_CompositeUsesCompleteEffectiveModelList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const groupID int64 = 120
	h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{
		byGroup: map[int64][]service.Account{
			groupID: {
				{
					ID:          3,
					Platform:    service.PlatformOpenAI,
					Status:      service.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{},
				},
				{
					ID:       1,
					Platform: service.PlatformOpenAI,
					Credentials: map[string]any{
						"model_mapping": map[string]any{"gpt-5.5": "gpt-5.5"},
					},
				},
				{
					ID:       2,
					Platform: service.PlatformGrok,
					Credentials: map[string]any{
						"model_mapping": map[string]any{"grok-4.6": "grok-4.6"},
					},
				},
			},
		},
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/models?client_version=0.147.0", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{ID: groupID, Platform: service.PlatformComposite},
	})

	h.CodexModels(c)

	require.Equal(t, http.StatusOK, rec.Code)
	var got codexModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	want := service.FilterCodexModelIDsForGroup(openai.DefaultModelIDs(), nil)
	want = append(want, service.FilterCodexModelIDsForGroup(service.ServableClientFacingIDs(context.Background(), service.PlatformGrok, nil, nil), nil)...)
	require.ElementsMatch(t, want, codexModelSlugsForTest(got.Models))
}

func TestGatewayModels_UnmappedOpenAIAccountsSupplementMappedModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const groupID int64 = 28
	const sparkModel = "gpt-5.3-codex-spark"
	const alias = "team-coder"
	parentID := int64(1)
	accounts := []service.Account{
		{ID: parentID, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth},
		{
			ID: 2, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			ParentAccountID: &parentID, QuotaDimension: "spark",
			Credentials: map[string]any{"model_mapping": map[string]any{sparkModel: sparkModel}},
		},
		{
			ID: 3, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Credentials: map[string]any{"model_mapping": map[string]any{alias: "gpt-5.6-sol"}},
		},
	}
	tests := []struct {
		name     string
		accounts []service.Account
		config   service.GroupModelAllowlist
		want     []string
	}{
		{
			name:     "unmapped parent and Spark shadow retain defaults and aliases",
			accounts: accounts,
			want:     append(openai.DefaultModelIDs(), alias),
		},
		{
			name:     "unmapped API key account also contributes defaults",
			accounts: append([]service.Account{{ID: 4, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}}, accounts[1:]...),
			want:     append(openai.DefaultModelIDs(), alias),
		},
		{
			name:     "unmapped accounts alone retain default response shape",
			accounts: accounts[:1],
			want:     service.ServableClientFacingIDs(context.Background(), service.PlatformOpenAI, nil, nil),
		},
		{
			name:     "custom list can select defaults and aliases",
			accounts: accounts,
			config:   service.GroupModelAllowlist{Enabled: true, Models: []string{alias, "gpt-5.6-sol", sparkModel, "unknown-model"}},
			want:     []string{alias, "gpt-5.6-sol", sparkModel},
		},
		{
			name:     "unavailable custom selection remains empty",
			accounts: accounts,
			config:   service.GroupModelAllowlist{Enabled: true, Models: []string{"unknown-model"}},
			want:     []string{},
		},
		{
			name:     "mapped accounts alone do not gain defaults",
			accounts: accounts[1:],
			want:     []string{sparkModel, alias},
		},
		{
			name:     "unmapped accounts from another platform do not add defaults",
			accounts: append([]service.Account{{ID: 4, Platform: service.PlatformAnthropic}}, accounts[1:]...),
			want:     []string{sparkModel, alias},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{
				byGroup: map[int64][]service.Account{groupID: tt.accounts},
			})
			for range 2 {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
				c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
					Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, ModelAllowlist: tt.config},
				})
				h.Models(c)
				require.Equal(t, http.StatusOK, rec.Code)
				var got gatewayModelsResponseForTest
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
				require.Equal(t, "list", got.Object)
				require.ElementsMatch(t, tt.want, modelIDsForTest(got.Data))
				for _, model := range got.Data {
					require.Equal(t, "model", model.Object, model.ID)
					require.Positive(t, model.Created, model.ID)
					require.Equal(t, "openai", model.OwnedBy, model.ID)
					require.Empty(t, model.CreatedAt, model.ID)
				}
				if tt.config.Enabled {
					require.Equal(t, tt.want, modelIDsForTest(got.Data))
				}
			}
		})
	}
	require.Empty(t, accounts[0].GetModelMapping())
	require.True(t, accounts[0].IsModelSupported("gpt-future-model"))
	require.False(t, accounts[1].IsModelSupported("gpt-5.6-sol"))
}

func TestGatewayCodexModels_GeneratedManifestUsesFinalBodyETag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const groupID int64 = 122
	h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{
		byGroup: map[int64][]service.Account{
			groupID: {{
				ID:       1,
				Platform: service.PlatformDeepseek,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"deepseek-v4-pro": "deepseek-v4-pro"},
				},
			}},
		},
	})
	group := &service.Group{ID: groupID, Platform: service.PlatformDeepseek}

	first := httptest.NewRecorder()
	firstContext, _ := gin.CreateTestContext(first)
	firstContext.Request = httptest.NewRequest(http.MethodGet, "/models?client_version=0.147.0", nil)
	firstContext.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{Group: group})
	h.CodexModels(firstContext)

	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)
	require.Equal(t, service.CodexModelsManifestETag(first.Body.Bytes()), etag)

	second := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(second)
	secondContext.Request = httptest.NewRequest(http.MethodGet, "/models?client_version=0.147.0", nil)
	secondContext.Request.Header.Set("If-None-Match", "W/"+etag)
	secondContext.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{Group: group})
	h.CodexModels(secondContext)

	require.Equal(t, http.StatusNotModified, second.Code)
	require.Empty(t, second.Body.Bytes())
	require.Equal(t, etag, second.Header().Get("ETag"))
}

// Scenario: group model_allowlist limits the generated Codex manifest.
func TestGatewayCodexModels_CustomModelsListFiltersCompositeManifest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const groupID int64 = 121
	h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{
		byGroup: map[int64][]service.Account{
			groupID: {
				{
					ID:       1,
					Platform: service.PlatformOpenAI,
					Credentials: map[string]any{
						"model_mapping": map[string]any{"gpt-5.5": "gpt-5.5"},
					},
				},
				{
					ID:       2,
					Platform: service.PlatformGrok,
					Credentials: map[string]any{
						"model_mapping": map[string]any{"grok-4.6": "grok-4.6"},
					},
				},
			},
		},
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/models?client_version=0.147.0", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformComposite,
			ModelAllowlist: service.GroupModelAllowlist{
				Enabled: true,
				Models:  []string{"grok-4.6"},
			},
		},
	})

	h.CodexModels(c)

	require.Equal(t, http.StatusOK, rec.Code)
	var got codexModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, []string{"grok-4.6"}, codexModelSlugsForTest(got.Models))
}

func codexModelSlugsForTest(models []struct {
	Slug                     string                       `json:"slug"`
	SupportedReasoningLevels []codexReasoningLevelForTest `json:"supported_reasoning_levels"`
	InputModalities          []string                     `json:"input_modalities"`
	ModelMessages            map[string]json.RawMessage   `json:"model_messages"`
	TruncationPolicy         map[string]json.RawMessage   `json:"truncation_policy"`
	AvailabilityNUX          json.RawMessage              `json:"availability_nux"`
	Upgrade                  json.RawMessage              `json:"upgrade"`
}) []string {
	slugs := make([]string, 0, len(models))
	for _, model := range models {
		slugs = append(slugs, model.Slug)
	}
	return slugs
}

func codexReasoningEffortsForTest(levels []codexReasoningLevelForTest) []string {
	efforts := make([]string, 0, len(levels))
	for _, level := range levels {
		efforts = append(efforts, level.Effort)
	}
	return efforts
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
	assertGrokGatewayReasoningEfforts(t, 4409, "grok-4.5", []gatewayReasoningEffortOptionForTest{
		{Value: "low", Label: "Low"},
		{Value: "medium", Label: "Medium"},
		{Value: "high", Label: "High", Default: true},
	})
}

func TestGatewayModels_Grok46AdvertisesXHighReasoningEffortForGrokBuild(t *testing.T) {
	xhighEfforts := []gatewayReasoningEffortOptionForTest{
		{Value: "low", Label: "Low"},
		{Value: "medium", Label: "Medium"},
		{Value: "high", Label: "High", Default: true},
		{Value: "xhigh", Label: "xHigh"},
	}
	tests := []struct {
		groupID int64
		model   string
	}{
		{groupID: 4410, model: "grok-4.6"},
		{groupID: 4411, model: "grok-4.6-latest"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assertGrokGatewayReasoningEfforts(t, tt.groupID, tt.model, xhighEfforts)
		})
	}
}

func assertGrokGatewayReasoningEfforts(t *testing.T, groupID int64, modelID string, want []gatewayReasoningEffortOptionForTest) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				groupID: {
					{
						ID:       1,
						Platform: service.PlatformGrok,
						Credentials: map[string]any{
							"model_mapping": map[string]any{modelID: modelID},
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
	var model *gatewayModelItemForTest
	wantID := xai.ResolveGrokTextResponsesModelID(modelID)
	for i := range got.Data {
		if got.Data[i].ID == wantID {
			model = &got.Data[i]
			break
		}
	}
	require.NotNil(t, model, "%s must be present in grok native catalog union", wantID)
	require.True(t, model.SupportsReasoningEffort)
	require.Equal(t, "high", model.ReasoningEffort)
	require.Equal(t, want, model.ReasoningEfforts)
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

// Scenario: a Composite group with only Anthropic accounts must not inherit Antigravity Gemini defaults.
func TestGatewayCodexModels_CompositeAnthropicDoesNotAdvertiseAntigravityDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(64)
	h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{
		byGroup: map[int64][]service.Account{
			groupID: {{ID: 1, Platform: service.PlatformAnthropic}},
		},
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/models?client_version=0.147.0", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{ID: groupID, Platform: service.PlatformComposite},
	})

	h.CodexModels(c)

	require.Equal(t, http.StatusOK, rec.Code)
	var got codexModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	slugs := codexModelSlugsForTest(got.Models)
	require.Contains(t, slugs, "claude-opus-4-6")
	require.NotContains(t, slugs, "gemini-2.5-flash")
}

// Scenario: Antigravity retains its own Claude and Gemini defaults inside Composite groups.
func TestGatewayModels_CompositeAntigravityAdvertisesAntigravityDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(65)
	h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{
		byGroup: map[int64][]service.Account{
			groupID: {{ID: 1, Platform: service.PlatformAntigravity}},
		},
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{ID: groupID, Platform: service.PlatformComposite},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)
	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	ids := modelIDsForTest(got.Data)
	require.Contains(t, ids, "claude-opus-4-6")
	require.Contains(t, ids, "gemini-2.5-flash")
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
			ModelAllowlist: service.GroupModelAllowlist{
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
			ModelAllowlist: service.GroupModelAllowlist{
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

func TestGatewayModels_CompositeCustomModelsListFiltersAcrossConcretePlatforms(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(33)
	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				groupID: {
					{
						ID:       1,
						Platform: service.PlatformOpenAI,
						Credentials: map[string]any{
							"model_mapping": map[string]any{
								"gpt-5.4": "gpt-5.4",
								"gpt-5.5": "gpt-5.5",
							},
						},
					},
					{
						ID:       2,
						Platform: service.PlatformGemini,
						Credentials: map[string]any{
							"model_mapping": map[string]any{
								"gemini-2.5-flash": "gemini-2.5-flash",
							},
						},
					},
					{
						ID:       3,
						Platform: service.PlatformAntigravity,
						Credentials: map[string]any{
							"model_mapping": map[string]any{
								"ag-custom-model": "ag-custom-model",
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
			Platform: service.PlatformComposite,
			ModelAllowlist: service.GroupModelAllowlist{
				Enabled: true,
				Models:  []string{"gemini-2.5-flash", "missing-model", "ag-custom-model", "gpt-5.5"},
			},
		},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, []string{"gemini-2.5-flash", "ag-custom-model", "gpt-5.5"}, modelIDsForTest(got.Data))
}

func TestGatewayModels_CompositeUnmappedAccountsFallbackToLinkedPlatformsOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(34)
	h := newGatewayModelsHandlerForTest(
		&gatewayModelsAccountRepoStub{
			byGroup: map[int64][]service.Account{
				groupID: {
					{ID: 1, Platform: service.PlatformOpenAI},
					{ID: 2, Platform: service.PlatformGrok},
				},
			},
		},
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{ID: groupID, Platform: service.PlatformComposite},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	ids := modelIDsForTest(got.Data)
	require.Contains(t, ids, "gpt-5.5")
	require.Contains(t, ids, "grok-4.3")
	require.NotContains(t, ids, "claude-sonnet-4-6")
	require.NotContains(t, ids, "gemini-2.5-flash")
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
			ModelAllowlist: service.GroupModelAllowlist{
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
			ModelAllowlist: service.GroupModelAllowlist{
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
			ModelAllowlist: service.GroupModelAllowlist{
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
			ModelAllowlist: service.GroupModelAllowlist{
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
			ModelAllowlist: service.GroupModelAllowlist{
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
			ModelAllowlist: service.GroupModelAllowlist{
				Enabled: true,
				// codex-auto-review is empirically servable; when listed explicitly it
				// stays in the custom list like other SSOT ids (non-servable junk still drops).
				Models: requestedModels,
			},
		},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	fallback := service.ServableClientFacingIDs(context.Background(), service.PlatformOpenAI, nil, nil)
	expected := filterModelsByCustomList(nil, fallback, requestedModels)
	require.Equal(t, expected, modelIDsForTest(got.Data))
	require.Contains(t, expected, "codex-auto-review", "explicitly listed servable id must survive custom-list filtering")
	for _, junk := range []string{"gpt-image-2", "legacy-gpt-2024", "gpt-not-a-real-id-zzz"} {
		require.NotContains(t, expected, junk)
	}
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
			ModelAllowlist: service.GroupModelAllowlist{
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
