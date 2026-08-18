package handler

import (
	"context"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type apiKeyCapabilityKeySource interface {
	GetByID(ctx context.Context, id int64) (*service.APIKey, error)
}

type apiKeyCapabilitySource interface {
	List(ctx context.Context, apiKey *service.APIKey, protocol service.UniversalProtocol) ([]service.UniversalCapability, error)
}

type APIKeyCapabilitiesResponse struct {
	APIKeyID    int64                         `json:"api_key_id"`
	RoutingMode string                        `json:"routing_mode"`
	Models      []service.UniversalCapability `json:"models"`
}

func (h *APIKeyHandler) SetCapabilityService(keys apiKeyCapabilityKeySource, capabilities apiKeyCapabilitySource) {
	if h == nil {
		return
	}
	h.capabilityKeys = keys
	h.capabilities = capabilities
}

func (h *APIKeyHandler) GetCapabilities(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || keyID <= 0 {
		response.BadRequest(c, "Invalid key ID")
		return
	}
	protocol, ok := parseUniversalProtocol(c.Query("protocol"))
	if !ok {
		response.BadRequest(c, "Invalid protocol")
		return
	}
	if h == nil || h.capabilityKeys == nil || h.capabilities == nil {
		response.InternalError(c, "capability service unavailable")
		return
	}

	key, err := h.capabilityKeys.GetByID(c.Request.Context(), keyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if key == nil || key.UserID != subject.UserID {
		response.NotFound(c, "API key not found")
		return
	}
	models, err := h.capabilities.List(c.Request.Context(), key, protocol)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if models == nil {
		models = []service.UniversalCapability{}
	}
	response.Success(c, APIKeyCapabilitiesResponse{
		APIKeyID:    key.ID,
		RoutingMode: key.RoutingMode,
		Models:      models,
	})
}

func parseUniversalProtocol(raw string) (service.UniversalProtocol, bool) {
	protocol := service.UniversalProtocol(strings.ToLower(strings.TrimSpace(raw)))
	if protocol == "" {
		protocol = service.UniversalProtocolAll
	}
	switch protocol {
	case service.UniversalProtocolAll,
		service.UniversalProtocolAnthropic,
		service.UniversalProtocolOpenAI,
		service.UniversalProtocolGemini,
		service.UniversalProtocolCodex,
		service.UniversalProtocolAntigravity:
		return protocol, true
	default:
		return "", false
	}
}
