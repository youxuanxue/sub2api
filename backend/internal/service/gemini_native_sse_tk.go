package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
	"github.com/gin-gonic/gin"
)

func isGeminiNativeSSEKeepalive(line string) bool {
	line = strings.TrimSpace(line)
	return line == "" || strings.HasPrefix(line, ":")
}

// Native Gemini errors use a complete data frame, without an event: prefix.
// Do not emit a successful finishReason or [DONE] on a failed stream.
func geminiNativeSSEErrorFrame(status int, message string) string {
	payload, _ := json.Marshal(googleapi.ErrorResponse{Error: googleapi.ErrorDetail{
		Code: status, Message: message, Status: googleapi.HTTPStatusToGoogleStatus(status),
	}})
	return fmt.Sprintf("data: %s\n\n", payload)
}

// WriteGeminiNativeStreamTruncatedFrame 在已经提交的 SSE 响应末尾补截断错误帧。
// 换号耗尽且只写过空白心跳时，状态码改不了，客户端仍然需要这个帧而不是裸 EOF。
func WriteGeminiNativeStreamTruncatedFrame(c *gin.Context) {
	if c == nil || c.Writer == nil {
		return
	}
	_, _ = fmt.Fprint(c.Writer, geminiNativeSSEErrorFrame(http.StatusBadGateway, GeminiNativeStreamTruncatedCode))
	c.Writer.Flush()
}
