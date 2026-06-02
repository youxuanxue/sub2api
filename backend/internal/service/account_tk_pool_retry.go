package service

// tkExtraPoolModeRetryableStatusCodes 是 TK 在 upstream 默认
// pool-mode 同账号重试集 {401,403,429}（defaultPoolModeRetryableStatusCodes）
// 之上**追加**的状态码。
//
// 503/529：TK 的 pool_mode 账号全部是 prod→edge（或 prod→兼容网关）转发 stub ——
// edge 透回的 529（上游过载）/ 503（no available accounts）再打同一上游 URL，会
// 轮换到下个真实账号或等其 overload/session 窗恢复。因此这两类瞬时错误应触发
// **同账号重试 = 池内轮换**，而不是立刻换 prod 账号、把瞬时错误透给客户端
// （现场：edge us1 整体瞬时 529/503 时 prod 单 stub 直接耗尽透出）。
//
// 为什么不直接改 upstream 的 defaultPoolModeRetryableStatusCodes：上游特意把
// 502/503/504 排除在默认外（默认开启会改变所有 pool 部署行为）。这里以
// upstream 默认为基、并集 TK 追加项，既保留上游常量/函数与其单测不变（零 merge
// 冲突），又让 TK 部署的转发 stub 默认获得 503/529 池内轮换。per-account 的
// pool_mode_retry_status_codes 显式配置仍然优先覆盖本默认（显式空列表可全关）。
var tkExtraPoolModeRetryableStatusCodes = []int{503, 529}

// tkIsPoolModeRetryableStatus 按 TK 默认（= upstream 默认 ∪ {503,529}）判断状态码
// 是否应触发同账号重试。作为 account.IsPoolModeRetryableStatus 在账号未显式配置
// 时的回退。
func tkIsPoolModeRetryableStatus(statusCode int) bool {
	if isPoolModeRetryableStatus(statusCode) {
		return true
	}
	for _, c := range tkExtraPoolModeRetryableStatusCodes {
		if c == statusCode {
			return true
		}
	}
	return false
}
