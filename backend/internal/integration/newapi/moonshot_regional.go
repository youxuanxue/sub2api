package newapi

import "strings"

// MoonshotAlternateRegionalBase 在官方 cn/ai 根域名间互换，供测试或工具使用。
// 生产 relay 热路径不再在 401/403 上换域重试；区域在保存账号时由 moonshot_resolve_save.go 并行探测并写入 base_url（方案 B）。
func MoonshotAlternateRegionalBase(base string) string {
	b := strings.TrimSpace(strings.TrimRight(base, "/"))
	if b == "" {
		return ""
	}
	if strings.Contains(b, "api.moonshot.cn") {
		return strings.Replace(b, "api.moonshot.cn", "api.moonshot.ai", 1)
	}
	if strings.Contains(b, "api.moonshot.ai") {
		return strings.Replace(b, "api.moonshot.ai", "api.moonshot.cn", 1)
	}
	return ""
}
