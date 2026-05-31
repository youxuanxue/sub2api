package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// TierHandler 处理 anthropic-oauth 稳定性档位（tiers 表）的 admin CRUD。
// TK-only：tier 是 git baseline 的投影，UI 编辑为应急/本地，流水线会回刷。
type TierHandler struct {
	service *service.TierService
}

// NewTierHandler 创建 tier 处理器。
func NewTierHandler(svc *service.TierService) *TierHandler {
	return &TierHandler{service: svc}
}

// tierRequest 是 Create / Update 的请求体（全字段）。
type tierRequest struct {
	Name                      string  `json:"name"`
	Description               *string `json:"description"`
	Concurrency               int     `json:"concurrency"`
	Priority                  int     `json:"priority"`
	RateMultiplier            float64 `json:"rate_multiplier"`
	BaseRPM                   int     `json:"base_rpm"`
	MaxSessions               int     `json:"max_sessions"`
	RPMStickyBuffer           int     `json:"rpm_sticky_buffer"`
	SessionIdleTimeoutMinutes int     `json:"session_idle_timeout_minutes"`
	WindowCostLimit           float64 `json:"window_cost_limit"`
	WindowCostStickyReserve   float64 `json:"window_cost_sticky_reserve"`
	CacheTTLOverrideEnabled   bool    `json:"cache_ttl_override_enabled"`
	CacheTTLOverrideTarget    *string `json:"cache_ttl_override_target"`
	TLSProfileName            *string `json:"tls_profile_name"`
	TLSProfileID              *int64  `json:"tls_profile_id"`
}

func (r *tierRequest) toModel() *model.Tier {
	return &model.Tier{
		Name:                      r.Name,
		Description:               r.Description,
		Concurrency:               r.Concurrency,
		Priority:                  r.Priority,
		RateMultiplier:            r.RateMultiplier,
		BaseRPM:                   r.BaseRPM,
		MaxSessions:               r.MaxSessions,
		RPMStickyBuffer:           r.RPMStickyBuffer,
		SessionIdleTimeoutMinutes: r.SessionIdleTimeoutMinutes,
		WindowCostLimit:           r.WindowCostLimit,
		WindowCostStickyReserve:   r.WindowCostStickyReserve,
		CacheTTLOverrideEnabled:   r.CacheTTLOverrideEnabled,
		CacheTTLOverrideTarget:    r.CacheTTLOverrideTarget,
		TLSProfileName:            r.TLSProfileName,
		TLSProfileID:              r.TLSProfileID,
	}
}

// List GET /api/v1/admin/tiers
func (h *TierHandler) List(c *gin.Context) {
	tiers, err := h.service.List(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tiers)
}

// GetByID GET /api/v1/admin/tiers/:id
func (h *TierHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, 400, "Invalid tier ID")
		return
	}
	t, err := h.service.GetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if t == nil {
		response.Error(c, 404, "Tier not found")
		return
	}
	response.Success(c, t)
}

// Create POST /api/v1/admin/tiers
func (h *TierHandler) Create(c *gin.Context) {
	var req tierRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, 400, "invalid request body")
		return
	}
	created, err := h.service.Create(c.Request.Context(), req.toModel())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, created)
}

// Update PUT /api/v1/admin/tiers/:id
func (h *TierHandler) Update(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, 400, "Invalid tier ID")
		return
	}
	var req tierRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, 400, "invalid request body")
		return
	}
	m := req.toModel()
	m.ID = id
	updated, err := h.service.Update(c.Request.Context(), m)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, updated)
}

// Delete DELETE /api/v1/admin/tiers/:id
func (h *TierHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, 400, "Invalid tier ID")
		return
	}
	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "deleted"})
}
