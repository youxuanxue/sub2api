package admin

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	newapifusion "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// TKChannelAdminHandler wires TokenKey New API admin endpoints without modifying upstream-shaped ChannelHandler.
type TKChannelAdminHandler struct {
	gatewayService *service.GatewayService
	adminService   service.AdminService
}

// NewTKChannelAdminHandler constructs TokenKey-only admin channel helpers.
func NewTKChannelAdminHandler(gatewayService *service.GatewayService, adminService service.AdminService) *TKChannelAdminHandler {
	return &TKChannelAdminHandler{
		gatewayService: gatewayService,
		adminService:   adminService,
	}
}

// TokenKey / new-api admin extensions: channel-type catalog and upstream model listing.

type aggregatedGroupModelsRequest struct {
	GroupIDs []int64 `json:"group_ids"`
	Platform string  `json:"platform" binding:"required,oneof=anthropic openai gemini antigravity newapi"`
}

// AggregatedGroupModels returns a deduplicated model id list for channel UI suggestions:
// union of GetAvailableModels (account model_mapping keys) and model_routing keys for each group.
// POST /api/v1/admin/channels/aggregated-group-models
func (h *TKChannelAdminHandler) AggregatedGroupModels(c *gin.Context) {
	if h.gatewayService == nil || h.adminService == nil {
		response.Error(c, http.StatusInternalServerError, "aggregated models unavailable")
		return
	}
	var req aggregatedGroupModelsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("VALIDATION_ERROR", err.Error()))
		return
	}
	if len(req.GroupIDs) == 0 {
		response.Success(c, gin.H{"models": []string{}})
		return
	}

	seen := make(map[int64]struct{})
	uniq := make([]int64, 0, len(req.GroupIDs))
	for _, id := range req.GroupIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		response.Success(c, gin.H{"models": []string{}})
		return
	}

	modelSet := make(map[string]struct{})
	ctx := c.Request.Context()

	for _, gid := range uniq {
		g, err := h.adminService.GetGroup(ctx, gid)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				response.ErrorFrom(c, infraerrors.NotFound("GROUP_NOT_FOUND", "group not found"))
			} else {
				response.Error(c, http.StatusInternalServerError, "failed to fetch group")
			}
			return
		}
		if g == nil {
			response.ErrorFrom(c, infraerrors.NotFound("GROUP_NOT_FOUND", "group not found"))
			return
		}
		if g.Platform != req.Platform {
			response.ErrorFrom(c, infraerrors.BadRequest(
				"GROUP_PLATFORM_MISMATCH",
				"group platform does not match requested platform",
			))
			return
		}
		for k := range g.ModelRouting {
			k = strings.TrimSpace(k)
			if k != "" {
				modelSet[k] = struct{}{}
			}
		}
		gidCopy := gid
		for _, m := range h.gatewayService.GetAvailableModels(ctx, &gidCopy, req.Platform) {
			m = strings.TrimSpace(m)
			if m != "" {
				modelSet[m] = struct{}{}
			}
		}
	}

	out := make([]string, 0, len(modelSet))
	for m := range modelSet {
		out = append(out, m)
	}
	sort.Strings(out)
	response.Success(c, gin.H{"models": out})
}

// ListChannelTypes returns New API channel type catalog.
// GET /api/v1/admin/channel-types
func (h *TKChannelAdminHandler) ListChannelTypes(c *gin.Context) {
	response.Success(c, newapifusion.ListChannelTypes())
}

// ListChannelTypeModels returns default model IDs per channel type (same source as new-api "填入相关模型").
// GET /api/v1/admin/channel-type-models
func (h *TKChannelAdminHandler) ListChannelTypeModels(c *gin.Context) {
	response.Success(c, newapifusion.ChannelTypeModelsJSON())
}

type fetchUpstreamModelsRequest struct {
	BaseURL     string `json:"base_url" binding:"max=2048"`
	ChannelType int    `json:"channel_type" binding:"required"`
	APIKey      string `json:"api_key" binding:"max=65536"`
	// AccountID optional: when api_key is empty, load api_key from this admin account (edit-account fetch without retyping key).
	AccountID int64 `json:"account_id"`
}

// FetchUpstreamModels lists model ids from the upstream provider (same behavior as new-api POST /api/channel/fetch_models).
// POST /api/v1/admin/channel-types/fetch-upstream-models
func (h *TKChannelAdminHandler) FetchUpstreamModels(c *gin.Context) {
	var req fetchUpstreamModelsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("VALIDATION_ERROR", err.Error()))
		return
	}
	if !newapifusion.IsKnownChannelType(req.ChannelType) {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_CHANNEL_TYPE", "invalid channel_type"))
		return
	}
	if !newapifusion.UpstreamModelFetchAllowed(req.ChannelType) {
		response.ErrorFrom(c, infraerrors.BadRequest("UPSTREAM_FETCH_NOT_SUPPORTED", "this channel type does not support upstream model listing"))
		return
	}
	base := strings.TrimSpace(req.BaseURL)
	if base != "" {
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			response.ErrorFrom(c, infraerrors.BadRequest("INVALID_BASE_URL", "base_url must be a valid http(s) URL"))
			return
		}
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" && req.AccountID > 0 {
		if h.adminService == nil {
			response.Error(c, http.StatusInternalServerError, "admin service unavailable")
			return
		}
		acc, err := h.adminService.GetAccount(c.Request.Context(), req.AccountID)
		if err != nil || acc == nil {
			response.ErrorFrom(c, infraerrors.NotFound("ACCOUNT_NOT_FOUND", "account not found"))
			return
		}
		if acc.Platform != "newapi" || acc.Type != "apikey" {
			response.ErrorFrom(c, infraerrors.BadRequest("INVALID_ACCOUNT", "account is not a newapi apikey account"))
			return
		}
		if acc.ChannelType != req.ChannelType {
			response.ErrorFrom(c, infraerrors.BadRequest("CHANNEL_TYPE_MISMATCH", "account channel_type does not match request"))
			return
		}
		apiKey = strings.TrimSpace(acc.GetCredential("api_key"))
	}
	if apiKey == "" {
		response.ErrorFrom(c, infraerrors.BadRequest("VALIDATION_ERROR", "api_key is required (or provide account_id to use stored credentials)"))
		return
	}

	models, err := newapifusion.FetchUpstreamModelList(c.Request.Context(), base, req.ChannelType, apiKey)
	if err != nil {
		response.Error(c, http.StatusBadGateway, "failed to fetch upstream models")
		return
	}
	response.Success(c, gin.H{"models": models})
}
