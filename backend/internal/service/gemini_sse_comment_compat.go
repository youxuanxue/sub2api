package service

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// geminiClientRejectsSSEComments 判断下游是不是 go-genai / python-genai。
//
// Gemini 原生流的空闲心跳是空白帧 "\n"（geminiNativeSSEKeepaliveFrame）。":\n\n" 会留在
// @google/genai 缓冲里，流结束抛 Incomplete JSON segment at the end。这两个 SDK 还不能接受
// 裸换行：它会粘到下一个以 "\n\n" 切分的事件上，前缀检查失败，所以原生流转发对它们关闭流内心跳。
//   - go-genai（Antigravity CLI）：非 "data:" 前缀直接报 "invalid stream chunk"。
//   - python-genai：把非 "data:" 行拼进 JSON 再 json.loads。
//
// js-genai（gl-node）使用规范的 event-stream 解析器，返回 false，继续收空白帧。识别依据是
// User-Agent / X-Goog-Api-Client 里同时出现 "google-genai-sdk/" 与 "gl-go/" 或 "gl-python/"。
func geminiClientRejectsSSEComments(clientHint string) bool {
	hint := strings.ToLower(strings.TrimSpace(clientHint))
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
