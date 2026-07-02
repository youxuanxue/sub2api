package admin

import (
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

var accountModelMappingPresetPlatforms = map[string]struct{}{
	service.PlatformAnthropic:   {},
	service.PlatformOpenAI:      {},
	service.PlatformGemini:      {},
	service.PlatformAntigravity: {},
	service.PlatformGrok:        {},
	service.PlatformKiro:        {},
	service.PlatformNewAPI:      {},
}

// GetModelMappingPresets returns empirically verified model_mapping preset IDs.
// GET /api/v1/admin/accounts/model-mapping-presets?platform=grok&channel_type=0
func (h *AccountHandler) GetModelMappingPresets(c *gin.Context) {
	platform := strings.TrimSpace(strings.ToLower(c.Query("platform")))
	if platform == "claude" {
		platform = service.PlatformAnthropic
	}
	if platform == "xai" {
		platform = service.PlatformGrok
	}
	if platform == "" {
		response.ErrorFrom(c, infraerrors.BadRequest("VALIDATION_ERROR", "platform is required"))
		return
	}
	if _, ok := accountModelMappingPresetPlatforms[platform]; !ok {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_PLATFORM", "unsupported platform"))
		return
	}

	channelType := 0
	if raw := strings.TrimSpace(c.Query("channel_type")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			response.ErrorFrom(c, infraerrors.BadRequest("VALIDATION_ERROR", "invalid channel_type"))
			return
		}
		channelType = parsed
	}

	ids, err := h.adminService.GetAccountModelMappingPresetIDs(c.Request.Context(), platform, channelType)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if ids == nil {
		ids = []string{}
	}
	response.Success(c, gin.H{"model_ids": ids})
}
