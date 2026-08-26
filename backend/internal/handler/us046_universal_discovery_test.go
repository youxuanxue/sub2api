package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type us046CapabilityKeySource struct {
	key *service.APIKey
	err error
}

func (s *us046CapabilityKeySource) GetByID(context.Context, int64) (*service.APIKey, error) {
	return s.key, s.err
}

type us046CapabilitySource struct {
	models     []service.UniversalCapability
	byProtocol map[service.UniversalProtocol][]service.UniversalCapability
	err        error
	protocol   service.UniversalProtocol
}

func (s *us046CapabilitySource) List(_ context.Context, _ *service.APIKey, protocol service.UniversalProtocol) ([]service.UniversalCapability, error) {
	s.protocol = protocol
	if s.byProtocol != nil {
		return s.byProtocol[protocol], s.err
	}
	return s.models, s.err
}

func us046CapabilityRouter(keys apiKeyCapabilityKeySource, capabilities apiKeyCapabilitySource, userID int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := &APIKeyHandler{capabilityKeys: keys, capabilities: capabilities}
	r := gin.New()
	r.GET("/api/v1/me/api-keys/:id/capabilities", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID})
		h.GetCapabilities(c)
	})
	return r
}

func TestUS046_CapabilityEndpointReturnsOwnedKeyCapabilities(t *testing.T) {
	models := []service.UniversalCapability{{
		ID:         "gpt-5",
		Protocols:  []service.UniversalProtocol{service.UniversalProtocolOpenAI},
		Modalities: []service.UniversalModality{service.UniversalModalityChat},
	}}
	source := &us046CapabilitySource{models: models}
	r := us046CapabilityRouter(
		&us046CapabilityKeySource{key: &service.APIKey{ID: 7, UserID: 42, RoutingMode: service.RoutingModeUniversal}},
		source,
		42,
	)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/me/api-keys/7/capabilities?protocol=openai", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, service.UniversalProtocolOpenAI, source.protocol)
	var body struct {
		Data APIKeyCapabilitiesResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, int64(7), body.Data.APIKeyID)
	require.Equal(t, service.RoutingModeUniversal, body.Data.RoutingMode)
	require.Equal(t, models, body.Data.Models)
}

func TestUS046_CapabilityEndpointRejectsForeignKey(t *testing.T) {
	source := &us046CapabilitySource{}
	r := us046CapabilityRouter(
		&us046CapabilityKeySource{key: &service.APIKey{ID: 7, UserID: 99, RoutingMode: service.RoutingModeUniversal}},
		source,
		42,
	)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/me/api-keys/7/capabilities", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Empty(t, source.protocol, "foreign key must not reach capability calculation")
}

func TestUS046_CapabilityEndpointInternalFailureIsNotEmptySuccess(t *testing.T) {
	r := us046CapabilityRouter(
		&us046CapabilityKeySource{key: &service.APIKey{ID: 7, UserID: 42, RoutingMode: service.RoutingModeUniversal}},
		&us046CapabilitySource{err: errors.New("account repository unavailable")},
		42,
	)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/me/api-keys/7/capabilities", nil))
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.NotContains(t, w.Body.String(), `"data":[]`)
}

func TestUS046_DiscoveryEndpointsUseNativeSchemas(t *testing.T) {
	openAICapabilities := []service.UniversalCapability{
		{ID: "gpt-5", Modalities: []service.UniversalModality{service.UniversalModalityChat}},
		{ID: "text-embedding-3-large", Modalities: []service.UniversalModality{service.UniversalModalityEmbedding}},
		{ID: "gpt-image-1", Modalities: []service.UniversalModality{service.UniversalModalityImage}},
		{ID: "sora-2", Modalities: []service.UniversalModality{service.UniversalModalityVideo}},
	}
	anthropicCapability := service.UniversalCapability{ID: "claude-sonnet-4-6", Modalities: []service.UniversalModality{service.UniversalModalityChat}}
	geminiCapability := service.UniversalCapability{ID: "gemini-2.5-pro", Modalities: []service.UniversalModality{service.UniversalModalityChat}}
	antigravityCapability := service.UniversalCapability{ID: "gemini-3-pro-preview", Modalities: []service.UniversalModality{service.UniversalModalityChat}}
	source := &us046CapabilitySource{byProtocol: map[service.UniversalProtocol][]service.UniversalCapability{
		service.UniversalProtocolOpenAI:      openAICapabilities,
		service.UniversalProtocolAnthropic:   {anthropicCapability},
		service.UniversalProtocolGemini:      {geminiCapability},
		service.UniversalProtocolAntigravity: {antigravityCapability},
	}}
	h := &GatewayHandler{tkCapabilities: source}
	key := &service.APIKey{ID: 7, UserID: 42, RoutingMode: service.RoutingModeUniversal}

	t.Run("openai", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		c.Request.Header.Set("Authorization", "Bearer sk-test")
		c.Set(string(middleware.ContextKeyAPIKey), key)
		h.Models(c)

		require.Equal(t, http.StatusOK, w.Code)
		var body struct {
			Data []map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		ids := make([]string, 0, len(body.Data))
		for _, model := range body.Data {
			id, ok := model["id"].(string)
			require.True(t, ok)
			ids = append(ids, id)
		}
		require.ElementsMatch(t, []string{"gpt-5", "text-embedding-3-large", "gpt-image-1", "sora-2"}, ids)
		require.Equal(t, "openai", body.Data[0]["owned_by"])
		require.NotNil(t, body.Data[0]["created"])
		require.NotContains(t, body.Data[0], "created_at")
	})

	t.Run("anthropic", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		c.Request.Header.Set("x-api-key", "sk-test")
		c.Request.Header.Set("anthropic-version", "2023-06-01")
		c.Set(string(middleware.ContextKeyAPIKey), key)
		h.Models(c)

		require.Equal(t, http.StatusOK, w.Code)
		var body struct {
			Data []map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "claude-sonnet-4-6", body.Data[0]["id"])
		require.NotNil(t, body.Data[0]["created_at"])
		require.NotContains(t, body.Data[0], "owned_by")
	})

	t.Run("gemini", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
		c.Set(string(middleware.ContextKeyAPIKey), key)
		h.GeminiV1BetaListModels(c)

		require.Equal(t, http.StatusOK, w.Code)
		var body struct {
			Models []struct {
				Name                       string   `json:"name"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "models/gemini-2.5-pro", body.Models[0].Name)
		require.Contains(t, body.Models[0].SupportedGenerationMethods, "generateContent")
	})

	t.Run("antigravity", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/antigravity/models", nil)
		c.Set(string(middleware.ContextKeyAPIKey), key)
		h.AntigravityModels(c)

		require.Equal(t, http.StatusOK, w.Code)
		var body struct {
			Data []map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "gemini-3-pro-preview", body.Data[0]["id"])
	})
}

func TestUS046_UniversalDiscoveryFailureIs500NotEmptyList(t *testing.T) {
	key := &service.APIKey{ID: 7, UserID: 42, RoutingMode: service.RoutingModeUniversal}
	source := &us046CapabilitySource{err: errors.New("database unavailable")}

	t.Run("openai", func(t *testing.T) {
		h := &GatewayHandler{tkCapabilities: source}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		c.Set(string(middleware.ContextKeyAPIKey), key)

		h.Models(c)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.NotContains(t, w.Body.String(), `"data":[]`)
	})

	t.Run("gemini", func(t *testing.T) {
		core, logs := observer.New(zap.WarnLevel)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
		c.Request = c.Request.WithContext(logger.IntoContext(c.Request.Context(), zap.New(core)))
		c.Set(string(middleware.ContextKeyAPIKey), key)

		h := &GatewayHandler{tkCapabilities: source}
		h.GeminiV1BetaListModels(c)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.NotContains(t, w.Body.String(), `"models":[]`)
		entries := logs.FilterMessage("capability_discovery_failed").All()
		require.Len(t, entries, 1)
		require.Equal(t, "gateway.gemini_models", entries[0].ContextMap()["component"])
		require.Equal(t, string(service.UniversalProtocolGemini), entries[0].ContextMap()["protocol"])
	})

	t.Run("antigravity", func(t *testing.T) {
		core, logs := observer.New(zap.WarnLevel)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/antigravity/models", nil)
		c.Request = c.Request.WithContext(logger.IntoContext(c.Request.Context(), zap.New(core)))
		c.Set(string(middleware.ContextKeyAPIKey), key)

		h := &GatewayHandler{tkCapabilities: source}
		h.AntigravityModels(c)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.NotContains(t, w.Body.String(), `"data":[]`)
		entries := logs.FilterMessage("capability_discovery_failed").All()
		require.Len(t, entries, 1)
		require.Equal(t, "gateway.antigravity_models", entries[0].ContextMap()["component"])
		require.Equal(t, string(service.UniversalProtocolAntigravity), entries[0].ContextMap()["protocol"])
		require.Contains(t, entries[0].ContextMap()["error"], "database unavailable")
	})

	t.Run("codex", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/backend-api/codex/models", nil)
		c.Set(string(middleware.ContextKeyAPIKey), key)

		h := &OpenAIGatewayHandler{tkCapabilities: source}
		h.CodexModels(c)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.NotContains(t, w.Body.String(), `"models":[]`)
	})
}

func TestUS046_CodexDiscoverySelectsAuthorizedOpenAIGroup(t *testing.T) {
	group := service.UniversalSelectedGroup{ID: 31, Name: "Codex", Platform: service.PlatformOpenAI}
	source := &us046CapabilitySource{byProtocol: map[service.UniversalProtocol][]service.UniversalCapability{
		service.UniversalProtocolCodex: {{
			ID: "gpt-5-codex",
			Routes: []service.UniversalCapabilityRoute{{
				Protocol: service.UniversalProtocolCodex,
				Modality: service.UniversalModalityChat,
				Group:    group,
			}},
		}},
	}}
	h := &OpenAIGatewayHandler{tkCapabilities: source}
	key := &service.APIKey{ID: 7, UserID: 42, RoutingMode: service.RoutingModeUniversal}

	resolved, allowedModelIDs, err := h.resolveCodexDiscoveryAPIKey(context.Background(), key)
	require.NoError(t, err)
	require.NotSame(t, key, resolved)
	require.Nil(t, key.Group, "metadata resolution must not mutate the authenticated key")
	require.Equal(t, group.ID, resolved.Group.ID)
	require.Equal(t, service.PlatformOpenAI, resolved.Group.Platform)
	require.Equal(t, map[string]struct{}{"gpt-5-codex": {}}, allowedModelIDs)
}

func TestUS046_CodexDiscoveryReturnsNativeEmptyManifestWithoutCapability(t *testing.T) {
	for _, path := range []string{
		"/v1/models?client_version=0.144.0",
		"/backend-api/codex/models?client_version=0.144.0",
	} {
		t.Run(path, func(t *testing.T) {
			h := &OpenAIGatewayHandler{tkCapabilities: &us046CapabilitySource{}}
			key := &service.APIKey{ID: 7, UserID: 42, RoutingMode: service.RoutingModeUniversal}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, path, nil)
			c.Set(string(middleware.ContextKeyAPIKey), key)

			h.CodexModels(c)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var body struct {
				Models []json.RawMessage `json:"models"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Empty(t, body.Models)
		})
	}
}

func TestUS046_CodexDiscoveryPathsUseAuthorizedOpenAIGroup(t *testing.T) {
	for _, path := range []string{
		"/v1/models?client_version=0.144.0",
		"/backend-api/codex/models?client_version=0.144.0",
	} {
		t.Run(path, func(t *testing.T) {
			h, upstream, groupID := newCodexModelsFailoverTestHandler(http.StatusServiceUnavailable)
			upstream.successBody = `{"models":[{"slug":"gpt-5.6-sol","custom_model":"keep"},{"slug":"gpt-5.6-terra"}],"custom_top":"keep"}`
			h.tkCapabilities = &us046CapabilitySource{byProtocol: map[service.UniversalProtocol][]service.UniversalCapability{
				service.UniversalProtocolCodex: {{
					ID: "gpt-5.6-sol",
					Routes: []service.UniversalCapabilityRoute{{
						Protocol: service.UniversalProtocolCodex,
						Modality: service.UniversalModalityChat,
						Group: service.UniversalSelectedGroup{
							ID:       groupID,
							Name:     "Codex",
							Platform: service.PlatformOpenAI,
						},
					}},
				}},
			}}
			key := &service.APIKey{ID: 7, UserID: 42, RoutingMode: service.RoutingModeUniversal}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, path, nil)
			c.Set(string(middleware.ContextKeyAPIKey), key)

			h.CodexModels(c)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var body struct {
				Models []map[string]any `json:"models"`
				Custom string           `json:"custom_top"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, "keep", body.Custom)
			require.Equal(t, []map[string]any{{"slug": "gpt-5.6-sol", "custom_model": "keep"}}, body.Models)
			require.Nil(t, key.Group, "metadata discovery must not mutate the authenticated key")
		})
	}
}
