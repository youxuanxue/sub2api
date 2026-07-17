package handler

// TK: 专属倍率值仅对 admin 可见。
//
// 运营场景：管理员给客户配置了 per-user 专属倍率（折扣/加价），但不希望
// 客户在自己的页面（API 密钥徽章、分组选择器、可用渠道、模型价格提示）
// 看到倍率数值。计费路径不受影响——gateway 计费仍按真实生效倍率执行，
// 这里只收敛"展示端点"的下发内容。
//
// 注入点（均为 thin hook）：
//   - GetUserGroupRates (api_key_handler.go)：非 admin 返回空 map。
//   - MePricingCatalogHandler.Get (me_pricing_catalog_handler_tk.go)：
//     置 opts.HideUserRateOverrides，catalog 的倍率提示回落到分组默认值。

import (
	"github.com/Wei-Shaw/sub2api/internal/domain"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// tkHideUserRateValues reports whether per-user rate override values must be
// hidden from the current requester. Hidden for everyone except admin; a
// missing role in context (should not happen on authenticated routes) also
// hides, failing closed.
func tkHideUserRateValues(c *gin.Context) bool {
	role, ok := middleware2.GetUserRoleFromContext(c)
	return !ok || role != domain.RoleAdmin
}
