package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"

	"github.com/gin-gonic/gin"
)

// BatchPassiveUsageRequest 批量被动用量请求体。
type BatchPassiveUsageRequest struct {
	AccountIDs []int64 `json:"account_ids" binding:"required"`
}

// GetBatchPassiveUsage 批量获取多个账号的被动用量窗口。
// POST /api/v1/admin/accounts/usage/batch
//
// 背景：账号列表页每行 OAuth/SetupToken/Gemini 账号原先在 onMounted 各自打一次
// GET /admin/accounts/:id/usage?source=passive，一页 N 个账号即 N 个并发 XHR
// （客户端 usageLoadQueue 已退化为直通，无法节流）。本端点把整页一次取回，前端
// 通过 AccountUsageCell 的 usageOverride 注入，单查扇出归一为一次请求。
//
// 返回 { "usage": { "<id>": UsageInfo } }，每个 UsageInfo 与单查
// (source=passive) 逐字节一致。无法服务被动用量的账号被静默省略（前端 cell 显示
// 「-」），与单查报错后的降级一致。
func (h *AccountHandler) GetBatchPassiveUsage(c *gin.Context) {
	var req BatchPassiveUsageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	accountIDs := normalizeInt64IDList(req.AccountIDs)
	if len(accountIDs) == 0 {
		response.Success(c, gin.H{"usage": map[string]any{}})
		return
	}

	usage := h.accountUsageService.GetPassiveUsageBatch(c.Request.Context(), accountIDs)
	response.Success(c, gin.H{"usage": usage})
}
