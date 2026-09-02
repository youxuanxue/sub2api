package admin

import (
	"errors"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type groupAccountsMutationRequest struct {
	AccountIDs              []int64 `json:"account_ids" binding:"required"`
	ConfirmMixedChannelRisk *bool   `json:"confirm_mixed_channel_risk"`
}

// ListAccounts handles listing accounts bound to a group.
// GET /api/v1/admin/groups/:id/accounts
func (h *GroupHandler) ListAccounts(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	status := strings.TrimSpace(c.Query("status"))
	search := strings.TrimSpace(c.Query("search"))
	if len(search) > 100 {
		search = search[:100]
	}
	channelType := 0
	if raw := strings.TrimSpace(c.Query("channel_type")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			response.BadRequest(c, "Invalid channel_type")
			return
		}
		channelType = parsed
	}

	accounts, total, err := h.adminService.ListGroupAccounts(
		c.Request.Context(), groupID, page, pageSize, status, search, channelType,
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	items := make([]gin.H, 0, len(accounts))
	for i := range accounts {
		mapped := dto.AccountFromServiceShallow(&accounts[i])
		if mapped == nil {
			continue
		}
		items = append(items, gin.H{
			"id":           mapped.ID,
			"name":         mapped.Name,
			"platform":     mapped.Platform,
			"type":         mapped.Type,
			"channel_type": mapped.ChannelType,
			"status":       mapped.Status,
			"schedulable":  mapped.Schedulable,
			"priority":     mapped.Priority,
			"extra":        mapped.Extra,
		})
	}
	response.Paginated(c, items, total, page, pageSize)
}

// BindAccounts handles adding accounts to a group.
// POST /api/v1/admin/groups/:id/accounts
func (h *GroupHandler) BindAccounts(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	var req groupAccountsMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	skipMixed := req.ConfirmMixedChannelRisk != nil && *req.ConfirmMixedChannelRisk
	if err := h.adminService.BindGroupAccounts(c.Request.Context(), groupID, req.AccountIDs, skipMixed); err != nil {
		var mixedErr *service.MixedChannelError
		if errors.As(err, &mixedErr) {
			c.JSON(409, gin.H{
				"error":            "mixed_channel_warning",
				"message":          mixedErr.Error(),
				"group_id":         mixedErr.GroupID,
				"group_name":       mixedErr.GroupName,
				"current_platform": mixedErr.CurrentPlatform,
				"other_platform":   mixedErr.OtherPlatform,
			})
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "accounts bound"})
}

// UnbindAccounts handles removing accounts from a group.
// DELETE /api/v1/admin/groups/:id/accounts
func (h *GroupHandler) UnbindAccounts(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	var req groupAccountsMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	if err := h.adminService.UnbindGroupAccounts(c.Request.Context(), groupID, req.AccountIDs); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "accounts unbound"})
}
