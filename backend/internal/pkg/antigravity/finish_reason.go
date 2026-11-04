package antigravity

import "strings"

// GeminiContentFilterFinishReason 报告 finishReason 是否属于 Gemini 内容策略停机。
// 比较前先 TrimSpace 再 ToUpper，大小写与首尾空白不敏感。
// 集合与 gemini-cli 遥测的 CONTENT_FILTER 归类一致，并纳入同语义的图片枚举。
func GeminiContentFilterFinishReason(reason string) bool {
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "SAFETY", "RECITATION", "LANGUAGE", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII",
		"IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION":
		return true
	default:
		return false
	}
}
