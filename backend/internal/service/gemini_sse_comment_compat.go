package service

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// geminiClientRejectsSSEComments 判断下游会不会被非 "data:" SSE 帧打断。
//
// Gemini 原生流的空闲心跳是空白帧 "\n"（geminiNativeSSEKeepaliveFrame）。":\n\n" 会留在
// @google/genai 缓冲里，流结束抛 Incomplete JSON segment at the end。下列客户端还不能接受
// 裸换行：它会粘到下一个以 "\n\n" 切分的事件上，前缀变成 "\ndata"，报
// iterateResponseStream: invalid stream chunk：
//   - go-genai / python-genai：User-Agent 或 X-Goog-Api-Client 含 google-genai-sdk/ 且
//     gl-go/ 或 gl-python/
//   - Antigravity CLI（agy）：线上 UA 是 antigravity/cli/… (aidev_client; …)，内部仍用
//     go-genai 切帧，但不带 google-genai-sdk/ 标记——漏检时 thought 扣住期间的空分隔行
//     会写成奇数个 "\n"，首个 data 帧直接中断
//
// js-genai（gl-node）与 Gemini CLI 使用规范/可吞空白的解析器，返回 false，继续收空白帧。
func geminiClientRejectsSSEComments(clientHint string) bool {
	hint := strings.ToLower(strings.TrimSpace(clientHint))
	if hint == "" {
		return false
	}
	// Real agy wire identity (fingerprint owner BuildUserAgent). Must be checked
	// before google-genai-sdk, because aidev_client never carries that marker.
	if strings.Contains(hint, "aidev_client") || strings.Contains(hint, "antigravity/cli/") {
		return true
	}
	if !strings.Contains(hint, "google-genai-sdk/") {
		return false
	}
	return strings.Contains(hint, "gl-go/") || strings.Contains(hint, "gl-python/")
}

// downstreamRejectsSSEComments 从请求头识别下游客户端（同时看 User-Agent 与 X-Goog-Api-Client）。
func downstreamRejectsSSEComments(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	return geminiClientRejectsSSEComments(c.GetHeader("User-Agent")) ||
		geminiClientRejectsSSEComments(c.GetHeader("X-Goog-Api-Client"))
}
