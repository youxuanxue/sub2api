package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type availableModelsAdminService struct {
	*stubAdminService
	account service.Account
}

func (s *availableModelsAdminService) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	if s.account.ID == id {
		acc := s.account
		return &acc, nil
	}
	return s.stubAdminService.GetAccount(context.Background(), id)
}

func setupAvailableModelsRouter(adminSvc service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.GET("/api/v1/admin/accounts/:id/models", handler.GetAvailableModels)
	return router
}

func TestAccountHandlerGetAvailableModels_AllPlatformsUseMinimalOptionDTO(t *testing.T) {
	floor, err := service.AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	antigravityMapping := floor.Platforms[service.PlatformAntigravity]
	require.NotEmpty(t, antigravityMapping)

	accounts := []service.Account{
		{ID: 901, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive},
		{ID: 902, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive},
		{ID: 903, Platform: service.PlatformGemini, Type: service.AccountTypeOAuth, Status: service.StatusActive},
		{
			ID: 904, Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth, Status: service.StatusActive,
			Credentials: map[string]any{"model_mapping": anyModelMappingFromStringMap(antigravityMapping)},
		},
		{
			ID: 905, Platform: service.PlatformNewAPI, Type: service.AccountTypeAPIKey, Status: service.StatusActive,
			Credentials: map[string]any{"model_mapping": map[string]any{"shape-model": "shape-model"}},
		},
		{ID: 906, Platform: service.PlatformKiro, Type: service.AccountTypeOAuth, Status: service.StatusActive},
		{ID: 907, Platform: service.PlatformGrok, Type: service.AccountTypeOAuth, Status: service.StatusActive},
	}

	for _, account := range accounts {
		account := account
		t.Run(account.Platform, func(t *testing.T) {
			svc := &availableModelsAdminService{stubAdminService: newStubAdminService(), account: account}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(
				http.MethodGet,
				fmt.Sprintf("/api/v1/admin/accounts/%d/models", account.ID),
				nil,
			)
			setupAvailableModelsRouter(svc).ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)

			var resp struct {
				Data []map[string]any `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.NotEmpty(t, resp.Data)
			for _, option := range resp.Data {
				require.Len(t, option, 2, "response must expose only id and display_name: %v", option)
				id, idOK := option["id"].(string)
				displayName, displayNameOK := option["display_name"].(string)
				require.True(t, idOK)
				require.True(t, displayNameOK)
				require.NotEmpty(t, id)
				require.NotEmpty(t, displayName)
			}
		})
	}
}

func modelIDSet(models []struct {
	ID string `json:"id"`
}) map[string]bool {
	ids := make(map[string]bool, len(models))
	for _, m := range models {
		ids[m.ID] = true
	}
	return ids
}

func availableModelIDs(models []struct {
	ID string `json:"id"`
}) []string {
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	return ids
}

func idsFromKiroAdminTestModels() []string {
	models := service.KiroAdminTestModels()
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	return ids
}

func anyModelMappingFromIDs(ids []string) map[string]any {
	out := make(map[string]any, len(ids))
	for _, id := range ids {
		out[id] = id
	}
	return out
}

func anyModelMappingFromStringMap(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type syncUpstreamHTTPUpstream struct {
	resp *http.Response
	err  error
}

func (u *syncUpstreamHTTPUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	if u.err != nil {
		return nil, u.err
	}
	return u.resp, nil
}

func (u *syncUpstreamHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func setupSyncUpstreamModelsRouter(adminSvc service.AdminService, upstream service.HTTPUpstream) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	accountTestSvc := service.NewAccountTestService(
		nil,
		nil,
		nil,
		nil,
		nil,
		nil, // kiroGatewayService
		nil, // rateLimitService
		upstream,
		&config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		nil,
	)
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, accountTestSvc, nil, nil, nil, nil, nil, nil)
	router.POST("/api/v1/admin/accounts/:id/models/sync-upstream", handler.SyncUpstreamModels)
	return router
}

func TestAccountHandlerGetAvailableModels_GrokUsesXAIModels(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       44,
			Name:     "grok-oauth",
			Platform: service.PlatformGrok,
			Type:     service.AccountTypeOAuth,
			Status:   service.StatusActive,
			Credentials: map[string]any{
				"model_mapping": map[string]any{
					"grok-4.3": "grok-4.3",
				},
			},
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/44/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	require.Equal(t, "grok-4.3", resp.Data[0].ID)
}

func TestAccountHandlerGetAvailableModels_GrokDefaultsToXAIModelsWithoutMapping(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       45,
			Name:     "grok-oauth-defaults",
			Platform: service.PlatformGrok,
			Type:     service.AccountTypeOAuth,
			Status:   service.StatusActive,
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/45/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Data)

	var ids []string
	for _, model := range resp.Data {
		id := model.ID
		ids = append(ids, id)
		require.NotContains(t, strings.ToLower(id), "claude")
	}
	require.ElementsMatch(t,
		service.ServableClientFacingIDs(context.Background(), service.PlatformGrok, nil, nil),
		ids,
		"grok defaults must mirror the unified servable SSOT")
}

func TestAccountHandlerGetAvailableModels_OpenAIOAuthUsesExplicitModelMapping(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       42,
			Name:     "openai-oauth",
			Platform: service.PlatformOpenAI,
			Type:     service.AccountTypeOAuth,
			Status:   service.StatusActive,
			Credentials: map[string]any{
				"model_mapping": map[string]any{
					"gpt-5": "gpt-5.1",
				},
			},
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/42/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	require.Equal(t, "gpt-5", resp.Data[0].ID)
}

func TestAccountHandlerGetAvailableModels_OpenAIOAuthPassthroughFallsBackToDefaults(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       43,
			Name:     "openai-oauth-passthrough",
			Platform: service.PlatformOpenAI,
			Type:     service.AccountTypeOAuth,
			Status:   service.StatusActive,
			Credentials: map[string]any{
				"model_mapping": map[string]any{
					"gpt-5": "gpt-5.1",
				},
			},
			Extra: map[string]any{
				"openai_passthrough": true,
			},
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/43/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Data)
	require.ElementsMatch(t,
		service.ServableClientFacingIDs(context.Background(), service.PlatformOpenAI, nil, nil),
		availableModelIDs(resp.Data),
		"OpenAI admin defaults must mirror the unified servable SSOT")
}

func TestAccountHandlerGetAvailableModels_OpenAINoMappingDropsAdvertisedDead(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       44,
			Name:     "openai-oauth",
			Platform: service.PlatformOpenAI,
			Type:     service.AccountTypeOAuth,
			Status:   service.StatusActive,
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/44/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Data)
	require.ElementsMatch(t,
		service.ServableClientFacingIDs(context.Background(), service.PlatformOpenAI, nil, nil),
		availableModelIDs(resp.Data),
		"OpenAI admin defaults must mirror the unified servable SSOT")
}

func TestAccountHandlerGetAvailableModels_GeminiOAuthDropsAdvertisedDead(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       45,
			Name:     "gemini-oauth",
			Platform: service.PlatformGemini,
			Type:     service.AccountTypeOAuth,
			Status:   service.StatusActive,
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/45/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Data)
	require.ElementsMatch(t,
		service.ServableClientFacingIDs(context.Background(), service.PlatformGemini, nil, nil),
		availableModelIDs(resp.Data),
		"Gemini admin defaults must mirror the unified servable SSOT")
}

func TestAccountHandlerGetAvailableModels_GeminiOAuthMappingUsesServableIntersection(t *testing.T) {
	servable := service.ServableClientFacingIDs(context.Background(), service.PlatformGemini, nil, nil)
	require.GreaterOrEqual(t, len(servable), 2, "Gemini SSOT needs at least two ids for this intersection test")
	mapped := []string{servable[0], servable[1]}
	mapping := anyModelMappingFromIDs(mapped)
	mapping["gemini-not-a-real-id-zzz"] = "gemini-not-a-real-id-zzz"
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       46,
			Name:     "gemini-oauth-mapped",
			Platform: service.PlatformGemini,
			Type:     service.AccountTypeOAuth,
			Status:   service.StatusActive,
			Credentials: map[string]any{
				"model_mapping": mapping,
			},
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/46/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	ids := modelIDSet(resp.Data)
	require.False(t, ids["gemini-not-a-real-id-zzz"], "unservable mapped preview must not appear")
	require.ElementsMatch(t, mapped, availableModelIDs(resp.Data),
		"Gemini mapped response must be the intersection of account mapping and servable SSOT")
}

// TestAccountHandlerGetAvailableModels_NewAPI_DoesNotReturnClaudeCatalog is the
// regression guard for the audit P1 finding: before fix, GetAvailableModels
// fell through the Claude branch for fifth-platform `newapi` accounts, returning
// claude.DefaultModels which is a meaningless model list for OpenAI-compat
// upstreams. Post-fix it must return mapping keys (or an empty array), never
// the Claude catalog.
func TestAccountHandlerGetAvailableModels_NewAPI_ReturnsModelMappingKeys(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:          501,
			Name:        "newapi-moonshot",
			Platform:    service.PlatformNewAPI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			ChannelType: 25, // moonshot
			Credentials: map[string]any{
				"api_key":  "k",
				"base_url": "https://api.moonshot.ai",
				"model_mapping": map[string]any{
					"gpt-4o-mini":  "moonshot-v1-8k",
					"claude-haiku": "moonshot-v1-32k",
				},
			},
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/501/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	ids := make(map[string]bool, len(resp.Data))
	for _, m := range resp.Data {
		ids[m.ID] = true
	}
	require.True(t, ids["gpt-4o-mini"], "expected mapping key gpt-4o-mini in newapi available models, got %v", ids)
	require.True(t, ids["claude-haiku"], "expected mapping key claude-haiku in newapi available models, got %v", ids)
	require.False(t, ids["claude-3-5-sonnet-20241022"], "must NOT return Claude default catalog for newapi accounts")
	require.Len(t, resp.Data, 2)
}

func TestAccountHandlerGetAvailableModels_NewAPI_NoMappingReturnsEmpty(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:          502,
			Name:        "newapi-no-mapping",
			Platform:    service.PlatformNewAPI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			ChannelType: 1,
			Credentials: map[string]any{
				"api_key":  "k",
				"base_url": "https://example.com",
			},
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/502/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Empty(t, resp.Data)
}

func TestAccountHandlerGetAvailableModels_NewAPI_VertexNoMappingReturnsServablePreset(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:          503,
			Name:        "newapi-vertex-no-mapping",
			Platform:    service.PlatformNewAPI,
			Type:        service.AccountTypeServiceAccount,
			Status:      service.StatusActive,
			ChannelType: 41,
			Credentials: map[string]any{
				"service_account_json": `{"project_id":"p"}`,
				"location":             "us-central1",
			},
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/503/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Data)
	require.ElementsMatch(t,
		service.AccountModelMappingPresetIDs(context.Background(), service.PlatformNewAPI, 41, nil),
		availableModelIDs(resp.Data),
		"Vertex newapi fallback must mirror the model_mapping preset SSOT")
}

func TestAccountHandlerGetAvailableModels_KiroOAuthUsesShortModelIDs(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       601,
			Name:     "kiro-us5",
			Platform: service.PlatformKiro,
			Type:     service.AccountTypeOAuth,
			Status:   service.StatusActive,
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/601/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.ElementsMatch(t, idsFromKiroAdminTestModels(), availableModelIDs(resp.Data))
	ids := modelIDSet(resp.Data)
	require.True(t, ids["claude-haiku-4-5"], "Kiro serves Haiku 4.5 on CodeWhisperer")
	require.False(t, ids["claude-sonnet-4-5-20250929"], "admin list exposes short IDs only; dated IDs are normalized at forward time")
}

func TestAccountHandlerGetAvailableModels_GrokUsesGrokCatalog(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       603,
			Name:     "grok-us4",
			Platform: service.PlatformGrok,
			Type:     service.AccountTypeAPIKey,
			Status:   service.StatusActive,
			Credentials: map[string]any{
				"api_key":  "edge-key",
				"base_url": "https://api-us4.tokenkey.dev",
			},
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/603/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Data)
	require.Equal(t, service.GrokDefaultTestModelID, resp.Data[0].ID, "grok chat probe default must be first")
	ids := modelIDSet(resp.Data)
	require.ElementsMatch(t,
		service.ServableClientFacingIDs(context.Background(), service.PlatformGrok, nil, nil),
		availableModelIDs(resp.Data),
		"grok account test must default from the grok catalog SSOT")
	require.False(t, ids["claude-sonnet-4-6"], "grok must not fall through to Claude catalog")
}

func TestAccountHandlerGetAvailableModels_AntigravityUsesLiveCatalog(t *testing.T) {
	floor, err := service.AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	mapping, ok := floor.Platforms[service.PlatformAntigravity]
	require.True(t, ok, "antigravity floor must be exported by the model_mapping SSOT")
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       701,
			Name:     "antigravity-or1-ls-b",
			Platform: service.PlatformAntigravity,
			Type:     service.AccountTypeOAuth,
			Status:   service.StatusActive,
			Credentials: map[string]any{
				"model_mapping": anyModelMappingFromStringMap(mapping),
			},
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/701/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Data)
	require.Equal(t, service.AntigravityDefaultTestModelID, resp.Data[0].ID,
		"antigravity chat probe default must be first")
	ids := modelIDSet(resp.Data)
	require.ElementsMatch(t,
		service.ServableClientFacingIDs(context.Background(), service.PlatformAntigravity, nil, nil),
		availableModelIDs(resp.Data),
		"antigravity admin test must expose the current live catalog SSOT")
	require.False(t, ids["gpt-oss-120b-medium"], "antigravity admin test must not offer gpt-oss")
}

func TestAccountHandlerGetAvailableModels_KiroMirrorStubUsesKiroCatalog(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       602,
			Name:     "kiro-us5",
			Platform: service.PlatformAnthropic,
			Type:     service.AccountTypeAPIKey,
			Status:   service.StatusActive,
			Credentials: map[string]any{
				"api_key":         "edge-key",
				"base_url":        "https://api-us5.tokenkey.dev",
				"mirror_platform": "kiro",
			},
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/602/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.ElementsMatch(t, idsFromKiroAdminTestModels(), availableModelIDs(resp.Data))
	ids := modelIDSet(resp.Data)
	require.False(t, ids["claude-sonnet-4-5-20250929"], "prod Kiro mirror stubs must not expose Anthropic dated test IDs")
}

func TestAccountHandlerGetAvailableModels_OpenAISparkShadowReturnsMappingModels(t *testing.T) {
	parentID := int64(100)
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:              44,
			Name:            "openai-spark-shadow",
			Platform:        service.PlatformOpenAI,
			Type:            service.AccountTypeOAuth,
			Status:          service.StatusActive,
			ParentAccountID: &parentID,
			QuotaDimension:  service.QuotaDimensionSpark,
			Credentials: map[string]any{
				"model_mapping": map[string]any{
					"gpt-5.3-codex-spark": "gpt-5.3-codex-spark",
				},
			},
		},
	}
	router := setupAvailableModelsRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/44/models", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	require.ElementsMatch(t, []string{
		"gpt-5.3-codex-spark",
	}, ids, "影子可用模型由 model_mapping 派生（非写死）")
}

func TestAccountHandlerSyncUpstreamModels_ConfigErrorReturnsBadRequest(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       44,
			Name:     "openai-apikey-missing-key",
			Platform: service.PlatformOpenAI,
			Type:     service.AccountTypeAPIKey,
			Status:   service.StatusActive,
			Credentials: map[string]any{
				"base_url": "https://openai.example.com/v1",
			},
		},
	}
	router := setupSyncUpstreamModelsRouter(svc, &syncUpstreamHTTPUpstream{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/44/models/sync-upstream", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "No OpenAI API key is available")
}

func TestAccountHandlerSyncUpstreamModels_UpstreamErrorDoesNotExposeBody(t *testing.T) {
	svc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       45,
			Name:     "openai-apikey-upstream-error",
			Platform: service.PlatformOpenAI,
			Type:     service.AccountTypeAPIKey,
			Status:   service.StatusActive,
			Credentials: map[string]any{
				"api_key":  "openai-key",
				"base_url": "https://openai.example.com/v1",
			},
		},
	}
	upstream := &syncUpstreamHTTPUpstream{resp: &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"SECRET_TOKEN should not be exposed"}`)),
	}}
	router := setupSyncUpstreamModelsRouter(svc, upstream)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/45/models/sync-upstream", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "Upstream model list request failed with HTTP 502")
	require.NotContains(t, rec.Body.String(), "SECRET_TOKEN")
}
