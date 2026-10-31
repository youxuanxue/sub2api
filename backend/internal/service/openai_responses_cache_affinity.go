package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// deriveResponsesPromptCacheKey supplies an upstream cache identity when a
// Codex OAuth HTTP Responses request has a stable session/content identity but
// no prompt_cache_key. The input is the pre-transform body so appended tool
// history cannot change the key. Only a digest leaves the gateway.
func (s *OpenAIGatewayService) deriveResponsesPromptCacheKey(c *gin.Context, account *Account, body []byte) string {
	if account == nil || account.Type != AccountTypeOAuth || !account.IsOpenAI() || !account.UsesOpenAICodexProtocol() ||
		isOpenAIResponsesCompactPath(c) || isOpenAICompatMessagesBridgeContext(c) || isOpenAICompatMessagesBridgeBody(body) {
		return ""
	}
	// An explicit field, including an explicit empty value, belongs to the
	// caller. Do not silently replace it with a gateway-generated identity.
	if gjson.GetBytes(body, "prompt_cache_key").Exists() {
		return ""
	}
	apiKeyID := getAPIKeyIDFromContext(c)
	if apiKeyID <= 0 {
		return ""
	}
	if explicitOpenAIRequestSessionID(c, body) == "" && !hasOpenAIContentSessionUserAnchor(body) {
		return ""
	}
	sessionHash := s.GenerateSessionHash(c, body)
	if sessionHash == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("openai-responses-cache:v1:%d:%s", apiKeyID, sessionHash)))
	return fmt.Sprintf("compat_resp_%x", sum[:])
}

// logOpenAIResponsesCacheIdentity records only non-sensitive routing evidence
// at the final outbound boundary. Raw prompt text, credentials and IDs never
// enter the log; identity values are represented by short SHA-256 digests.
func logOpenAIResponsesCacheIdentity(ctx context.Context, account *Account, req *http.Request, body []byte, promptCacheKey string) {
	if ctx == nil {
		ctx = context.Background()
	}
	if account == nil || account.Type != AccountTypeOAuth || !account.UsesOpenAICodexProtocol() {
		return
	}
	bodyKey := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	sessionID := ""
	conversationID := ""
	if req != nil {
		sessionID = strings.TrimSpace(req.Header.Get("session_id"))
		conversationID = strings.TrimSpace(req.Header.Get("conversation_id"))
	}
	logger.FromContext(ctx).Debug("openai responses cache identity",
		zap.String("component", "service.openai_cache"),
		zap.Int64("account_id", account.ID),
		zap.Bool("prompt_cache_key_present", bodyKey != ""),
		zap.Bool("prompt_cache_key_explicit_zero", bodyKey == "" && gjson.GetBytes(body, "prompt_cache_key").Exists()),
		zap.String("prompt_cache_key_sha256", hashSensitiveValueForLog(firstNonEmptyString(promptCacheKey, bodyKey))),
		zap.String("session_id_sha256", hashSensitiveValueForLog(sessionID)),
		zap.String("conversation_id_sha256", hashSensitiveValueForLog(conversationID)),
		zap.Bool("session_conversation_aligned", sessionID != "" && sessionID == conversationID),
		zap.Bool("prompt_cache_key_matches_body", strings.TrimSpace(promptCacheKey) == bodyKey),
	)
}
