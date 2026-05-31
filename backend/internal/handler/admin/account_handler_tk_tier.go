package admin

import (
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"

	"github.com/gin-gonic/gin"
)

// applyTierRequest is the body for POST /admin/accounts/:id/apply-tier.
type applyTierRequest struct {
	Tier string `json:"tier" binding:"required"`
}

// ApplyTier applies an embedded tier baseline to a single anthropic account on
// THIS deployment (concurrency/priority/extra + canonical TLS binding + operator
// Σ sync). It does NOT propagate to other edges/prod — fleet fan-out stays with
// the ops/anthropic pipeline.
func (h *AccountHandler) ApplyTier(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "Invalid account ID")
		return
	}

	var req applyTierRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "tier is required")
		return
	}

	if h.accountTierService == nil {
		response.Error(c, http.StatusInternalServerError, "account tier service unavailable")
		return
	}

	account, err := h.accountTierService.ApplyTier(c.Request.Context(), id, req.Tier)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "Failed to apply tier: "+err.Error())
		return
	}

	response.Success(c, account)
}
