package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type candidateDiscoverySourceStub struct {
	us046CapabilitySource
	accounts []service.Account
}

func (*candidateDiscoverySourceStub) CandidateSchedulingEnabled() bool { return true }

func (s *candidateDiscoverySourceStub) DiscoverCandidates(ctx context.Context, key *service.APIKey, protocol service.UniversalProtocol) ([]service.UniversalCapability, []service.Account, error) {
	models, err := s.List(ctx, key, protocol)
	return models, s.accounts, err
}

func TestUS050_DirectDiscoveryUsesSupportProjectionAndKeepsClientSchemas(t *testing.T) {
	for _, tc := range []struct {
		name       string
		path       string
		model      string
		protocol   service.UniversalProtocol
		anthropic  bool
		handle     func(*GatewayHandler, *gin.Context)
		assertBody func(*testing.T, map[string]any)
	}{
		{
			name: "openai custom list", path: "/v1/models", model: "gpt-5.4", protocol: service.UniversalProtocolOpenAI,
			handle: (*GatewayHandler).Models,
			assertBody: func(t *testing.T, body map[string]any) {
				models, ok := body["data"].([]any)
				require.True(t, ok)
				require.Len(t, models, 1)
				model, ok := models[0].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "gpt-5.4", model["id"])
				require.Equal(t, "model", model["object"])
			},
		},
		{
			name: "anthropic native under openai billing label", path: "/v1/models", model: "claude-fable-5", protocol: service.UniversalProtocolAnthropic, anthropic: true,
			handle: (*GatewayHandler).Models,
			assertBody: func(t *testing.T, body map[string]any) {
				models, ok := body["data"].([]any)
				require.True(t, ok)
				require.Len(t, models, 1)
				model, ok := models[0].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "claude-fable-5", model["id"])
				require.Contains(t, models[0], "created_at")
				require.NotContains(t, models[0], "owned_by")
			},
		},
		{
			name: "gemini under openai billing label", path: "/v1beta/models", model: "gemini-2.5-pro", protocol: service.UniversalProtocolGemini,
			handle: (*GatewayHandler).GeminiV1BetaListModels,
			assertBody: func(t *testing.T, body map[string]any) {
				models, ok := body["models"].([]any)
				require.True(t, ok)
				require.Len(t, models, 1)
				model, ok := models[0].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "models/gemini-2.5-pro", model["name"])
				require.Contains(t, model["supportedGenerationMethods"], "generateContent")
			},
		},
		{
			name: "antigravity under openai billing label", path: "/antigravity/models", model: "gemini-3-pro-preview", protocol: service.UniversalProtocolAntigravity,
			handle: (*GatewayHandler).AntigravityModels,
			assertBody: func(t *testing.T, body map[string]any) {
				models, ok := body["data"].([]any)
				require.True(t, ok)
				require.Len(t, models, 1)
				model, ok := models[0].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "gemini-3-pro-preview", model["id"])
				require.Equal(t, "model", model["type"])
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := &service.Group{ID: 10, Platform: service.PlatformOpenAI, Status: service.StatusActive}
			group.ModelsListConfig.Enabled = true
			group.ModelsListConfig.Models = []string{tc.model, "not-authorized"}
			key := &service.APIKey{ID: 7, RoutingMode: service.RoutingModeDirect, Group: group, GroupID: &group.ID}
			source := &candidateDiscoverySourceStub{us046CapabilitySource: us046CapabilitySource{models: []service.UniversalCapability{
				{ID: tc.model, Modalities: []service.UniversalModality{service.UniversalModalityChat}},
				{ID: "another-supported-model", Modalities: []service.UniversalModality{service.UniversalModalityChat}},
			}}}
			h := &GatewayHandler{tkCapabilities: source}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.anthropic {
				c.Request.Header.Set("anthropic-version", "2023-06-01")
			}
			c.Set(string(middleware.ContextKeyAPIKey), key)
			tc.handle(h, c)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Equal(t, tc.protocol, source.protocol)
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			tc.assertBody(t, body)
			require.Same(t, group, key.Group)
		})
	}
}

func TestUS050_GeminiModelLookupUsesSupportWithoutGroupPlatformGate(t *testing.T) {
	for _, model := range []string{"gemini-2.5-pro", "not-authorized"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models/"+model, nil)
		c.Params = gin.Params{{Key: "model", Value: model}}
		key := &service.APIKey{RoutingMode: service.RoutingModeDirect, Group: &service.Group{ID: 10, Platform: service.PlatformOpenAI}}
		c.Set(string(middleware.ContextKeyAPIKey), key)
		h := &GatewayHandler{tkCapabilities: &candidateDiscoverySourceStub{us046CapabilitySource: us046CapabilitySource{models: []service.UniversalCapability{
			{ID: "gemini-2.5-pro", Modalities: []service.UniversalModality{service.UniversalModalityChat}},
		}}}}
		h.GeminiV1BetaGetModel(c)
		if model == "not-authorized" {
			require.Equal(t, http.StatusNotFound, w.Code)
		} else {
			require.Equal(t, http.StatusOK, w.Code)
			require.Contains(t, w.Body.String(), `"name":"models/gemini-2.5-pro"`)
		}
	}
}

func candidateManifestAccount(id int64) service.Account {
	return service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{
			"api_key": fmt.Sprintf("sk-%d", id), "base_url": fmt.Sprintf("https://upstream-%d.example/v1", id),
		}}
}

func TestUS050_CodexDiscoveryUsesActualAccountsAndPreservesManifest(t *testing.T) {
	for _, universal := range []bool{false, true} {
		h, upstream, _ := newCodexModelsFailoverTestHandler(http.StatusServiceUnavailable)
		upstream.successBody = `{"client_version":"0.1","models":[{"slug":"gpt-5.6-sol","display_name":"Live model","context_window":256000},{"slug":"not-authorized"}]}`
		h.tkCapabilities = &candidateDiscoverySourceStub{us046CapabilitySource: us046CapabilitySource{models: []service.UniversalCapability{
			{ID: "gpt-5.6-sol", Modalities: []service.UniversalModality{service.UniversalModalityChat}},
			{ID: "peer-model", Modalities: []service.UniversalModality{service.UniversalModalityChat}},
		}}, accounts: []service.Account{candidateManifestAccount(1), candidateManifestAccount(2)}}
		key := &service.APIKey{ID: 7, RoutingMode: service.RoutingModeUniversal}
		if !universal {
			key.RoutingMode = service.RoutingModeDirect
			key.Group = &service.Group{ID: 10, Platform: service.PlatformAnthropic, Status: service.StatusActive}
			key.GroupID = &key.Group.ID
		}
		originalGroup := key.Group
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/models?client_version=0.1", nil)
		c.Request.Header.Set("If-None-Match", `W/"unfiltered"`)
		c.Set(string(middleware.ContextKeyAPIKey), key)
		h.CodexModels(c)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, []int64{1, 2}, upstream.calls())
		require.Same(t, originalGroup, key.Group)
		var body struct {
			ClientVersion string `json:"client_version"`
			Models        []struct {
				Slug          string `json:"slug"`
				DisplayName   string `json:"display_name"`
				ContextWindow int    `json:"context_window"`
			} `json:"models"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "0.1", body.ClientVersion)
		require.Len(t, body.Models, 2)
		require.Equal(t, "Live model", body.Models[0].DisplayName)
		require.Equal(t, 256000, body.Models[0].ContextWindow)
		require.Equal(t, "peer-model", body.Models[1].Slug)
		require.Equal(t, service.CodexModelsManifestETag(w.Body.Bytes()), w.Header().Get("ETag"))
	}
}
