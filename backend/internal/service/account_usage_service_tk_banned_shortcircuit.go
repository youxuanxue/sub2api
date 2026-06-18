package service

import "strings"

// TK：被上游封禁(status=error 且 forbidden 类错误)的账号,admin 用量查询短路,不打上游。
//
// 背景:GetUsage(active) 对 OAuth 账号会拿存储 access_token 直打 Anthropic 的
// /api/oauth/usage,只判 account.Type,不判 status。于是运维一打开被封账号的卡片(或前端
// 轮询)就给已被封的 org 又加一次 403——2026-06-16 us6 事件 08:42 那条 admin-GET-403 即此。
//
// tkIsForbiddenAccountError 镜像 enrichUsageWithAccountError 的守卫:**只**短路上游被封/
// forbidden 的情况(403 / forbidden / violation / validation)。**刻意区分**——可恢复 token
// 错误(token refresh failed / invalid_client / missing_project_id / unauthenticated,见
// tryClearRecoverableAccountError)仍走实时拉取,使既有的「成功一次即清错误」自愈探测不被回退。
func tkIsForbiddenAccountError(errorMessage string) bool {
	msg := strings.ToLower(errorMessage)
	return strings.Contains(msg, "403") ||
		strings.Contains(msg, "forbidden") ||
		strings.Contains(msg, "violation") ||
		strings.Contains(msg, "validation")
}
