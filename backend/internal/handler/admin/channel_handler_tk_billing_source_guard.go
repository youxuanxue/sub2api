package admin

import (
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TK billing_model_source 确认闸（docs/approved/priced-or-it-doesnt-ship.md 复审 B1 的缓解）。
//
// channels.billing_model_source 决定计费按【哪个模型名】查价：
//   - channel_mapped（默认、安全）：按上游/映射后的模型名计费 —— 与价格闸判定的
//     模型名是同一个，闸键 == 账键 由构造保证。
//   - requested：按客户端【映射前】的模型名计费。原生 gemini/anthropic 路径上闸也判
//     requested 名，但 catch-all 映射 {"*": 有价} 会让【映射后】的名有价、而 requested
//     名无价 —— 计费于是按一个闸已用另一把键放行过的键收费。这正是复审 B1 的 $0 漏计：
//     一个配置开关能悄悄重新捅开价格闸刚堵上的洞。
//   - upstream：同类的键发散风险。
//
// 所以把这个开关拨到非默认值是一次【刻意的、影响计费的】动作。本闸要求任何把它设为
// requested/upstream 的 create/update 显式携带 confirm_billing_model_source=true，
// 把「静默改默认」变成「两把键的人类确认」。channel_mapped 与省略/空值（Update 保持原值）
// 自由放行。
//
// 注：prod 当前 0 个 requested/upstream 渠道 —— 本闸把它【保持】在 0，而不是去做账层手术。
func tkRequireBillingModelSourceConfirm(source string, confirmed bool) error {
	switch source {
	case service.BillingModelSourceRequested, service.BillingModelSourceUpstream:
		if !confirmed {
			return infraerrors.BadRequest(
				"BILLING_MODEL_SOURCE_CONFIRM_REQUIRED",
				"将渠道 billing_model_source 设为 \""+source+"\" 会改变计费所用的模型名，"+
					"可能与价格闸判定的模型名不一致而产生 $0 漏计"+
					"（见 docs/approved/priced-or-it-doesnt-ship.md 复审 B1）。"+
					"确认风险后，请在请求体中附带 confirm_billing_model_source=true 再提交。",
			)
		}
	}
	return nil
}
