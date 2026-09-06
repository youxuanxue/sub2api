package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// geminiCLITmpDirRegex 用于从 Gemini CLI 请求体中提取 tmp 目录的哈希值
// 匹配格式: /Users/xxx/.gemini/tmp/[64位十六进制哈希]
var geminiCLITmpDirRegex = regexp.MustCompile(`/\.gemini/tmp/([A-Fa-f0-9]{64})`)

// extractGeminiCLISessionHash 从 Gemini CLI 请求中提取会话标识。
// 组合 x-gemini-api-privileged-user-id header 和请求体中的 tmp 目录哈希。
//
// 会话标识生成策略：
//  1. 从请求体中提取 tmp 目录哈希（64位十六进制）
//  2. 从 header 中提取 privileged-user-id（UUID）
//  3. 组合两者生成 SHA256 哈希作为最终的会话标识
//
// 如果找不到 tmp 目录哈希，返回空字符串（不使用粘性会话）。
//
// extractGeminiCLISessionHash extracts session identifier from Gemini CLI requests.
// Combines x-gemini-api-privileged-user-id header with tmp directory hash from request body.
func extractGeminiCLISessionHash(c *gin.Context, body []byte) string {
	// 1. 从请求体中提取 tmp 目录哈希
	match := geminiCLITmpDirRegex.FindSubmatch(body)
	if len(match) < 2 {
		return "" // 没有找到 tmp 目录，不使用粘性会话
	}
	tmpDirHash := string(match[1])

	// 2. 提取 privileged-user-id
	privilegedUserID := strings.TrimSpace(c.GetHeader("x-gemini-api-privileged-user-id"))

	// 3. 组合生成最终的 session hash
	if privilegedUserID != "" {
		// 组合两个标识符：privileged-user-id + tmp 目录哈希
		combined := privilegedUserID + ":" + tmpDirHash
		hash := sha256.Sum256([]byte(combined))
		return hex.EncodeToString(hash[:])
	}

	// 如果没有 privileged-user-id，直接使用 tmp 目录哈希
	return tmpDirHash
}

// tkSaveGeminiDigestSession persists the Gemini content-digest session used for
// sticky fallback matching. No-op when digest fallback was not used or hashes
// are empty — same gates as the previous inline block in GeminiV1Beta.
func (h *GatewayHandler) tkSaveGeminiDigestSession(
	ctx context.Context,
	reqLog *zap.Logger,
	groupID int64,
	useDigestFallback bool,
	geminiDigestChain, geminiPrefixHash, geminiSessionUUID, matchedDigestChain string,
	accountID int64,
) {
	if !useDigestFallback || geminiDigestChain == "" || geminiPrefixHash == "" {
		return
	}
	if err := h.gatewayService.SaveGeminiSession(
		ctx,
		groupID,
		geminiPrefixHash,
		geminiDigestChain,
		geminiSessionUUID,
		accountID,
		matchedDigestChain,
	); err != nil {
		reqLog.Warn("gemini.digest_session_save_failed", zap.Int64("account_id", accountID), zap.Error(err))
	}
}

// truncateDigestChain 截断摘要链用于日志显示
func truncateDigestChain(chain string) string {
	if len(chain) <= 50 {
		return chain
	}
	return chain[:50] + "..."
}

// safeShortPrefix 返回字符串前 n 个字符；长度不足时返回原字符串。
// 用于日志展示，避免切片越界。
func safeShortPrefix(value string, n int) string {
	if n <= 0 || len(value) <= n {
		return value
	}
	return value[:n]
}
