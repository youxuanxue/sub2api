package service

// UsesOpenAI403Ladder 报告某个 platform 的 403 是否走 OpenAI 累计 403 阶梯
// （handleOpenAI403：HTML 豁免 + N 次累计 + 临时冷却 + 第 N 次永久禁用）。
//
// 这是该阶梯「覆盖哪些 platform」的单一事实来源。递增侧（handle403）与清零侧
// （成功响应）必须引用同一个判断，否则两边会漂移：修复前递增覆盖 6 个 platform
// （openai / opencodego / kimi / zhipu / deepseek / minimax），而唯一的成功清零点
// 只认 openai，于是 CN 与 OpenCodeGo 账号的 403 计数只增不减——在 180 分钟窗口里
// 零散碰到 3 次不相关的 403（坏代理、偶发权限抖动），中间夹着成千上万次成功请求，
// 第 3 次就把一个完全健康、正在正常服务的账号永久禁用。这条不依赖并发，单线程
// 即可触发。
//
// 新增走此阶梯的 platform 时只改这里，两侧自动保持一致。
func UsesOpenAI403Ladder(platform string) bool {
	return platform == PlatformOpenAI ||
		platform == PlatformOpenCodeGo ||
		IsCNProvider(platform)
}
