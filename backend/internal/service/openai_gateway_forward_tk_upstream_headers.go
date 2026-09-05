package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// tkApplyOpenAIForwardUpstreamHeaders applies Codex/OAuth outbound headers after
// auth + ChatGPT Host/account headers + whitelist copy, and before
// ApplyHeaderOverrides. ChatGPT Host/chatgpt-account-id stay in
// buildUpstreamRequest (before whitelist) — do not move them here.
func (s *OpenAIGatewayService) tkApplyOpenAIForwardUpstreamHeaders(
	ctx context.Context,
	c *gin.Context,
	req *http.Request,
	account *Account,
	body []byte,
	promptCacheKey string,
	isCodexCLI bool,
) error {
	// 客户端回带的 x-codex-turn-state 若已知由其他账号铸造（failover 换号），
	// 剥离后再出站——异账号 blob 与本账号的（指纹收敛后）出站身份自相矛盾。
	s.guardOpenAICodexTurnStateEcho(c, account, req.Header)
	if account.UsesOpenAICodexProtocol() {
		compatMessagesBridge := isOpenAICompatMessagesBridgeContext(c) || isOpenAICompatMessagesBridgeBody(body)
		// 清除客户端透传的 session 头，后续用隔离后的值重新设置，防止跨用户会话碰撞。
		clientConversationID := strings.TrimSpace(req.Header.Get("conversation_id"))
		req.Header.Del("conversation_id")
		req.Header.Del("session_id")

		if compatMessagesBridge {
			req.Header.Del("OpenAI-Beta")
			req.Header.Del("originator")
		} else {
			req.Header.Set("originator", resolveOpenAIUpstreamOriginator(c, isCodexCLI))
		}
		apiKeyID := getAPIKeyIDFromContext(c)
		if isOpenAIResponsesCompactPath(c) {
			req.Header.Set("accept", "application/json")
			if req.Header.Get("version") == "" {
				req.Header.Set("version", CodexCanonicalClientVersion())
			}
			compactSession := resolveOpenAICompactSessionID(c)
			req.Header.Set("session_id", isolateOpenAISessionID(apiKeyID, compactSession))
		} else {
			req.Header.Set("accept", "text/event-stream")
			if req.Header.Get("version") == "" {
				req.Header.Set("version", codexCLIVersion)
			}
		}
		if promptCacheKey != "" {
			isolated := isolateOpenAISessionID(apiKeyID, promptCacheKey)
			req.Header.Set("session_id", isolated)
			if !compatMessagesBridge || clientConversationID != "" {
				req.Header.Set("conversation_id", isolated)
			}
		}
	} else if isOpenAIResponsesCompactPath(c) {
		// compact 上游是 unary JSON 协议：API-key 账号也显式声明 Accept，
		// 避免 OpenAI 兼容网关按 SSE 返回（#3777 期望行为 4）。
		req.Header.Set("accept", "application/json")
	}

	// Apply custom User-Agent if configured
	if account.IsOpenAIOAuthLike() {
		inboundUA := ""
		if c != nil {
			inboundUA = c.GetHeader("User-Agent")
		}
		s.applyOpenAICodexUserAgent(ctx, req, account, inboundUA)
	} else {
		customUA := account.GetOpenAIUserAgent()
		if customUA != "" {
			req.Header.Set("user-agent", customUA)
		}
	}

	// 若开启 ForceCodexCLI，则强制将上游 User-Agent 伪装为规范 Codex 身份。
	// 用于网关未透传/改写 User-Agent 时，仍能命中 Codex 侧识别逻辑。
	if s.cfg != nil && s.cfg.Gateway.ForceCodexCLI {
		req.Header.Set("user-agent", CodexCanonicalUserAgent())
	}

	// 指纹收敛：使用 Forward() 中预计算的收敛 ID 改写出站头，与请求体使用同一份 IDs。
	applyStagedCodexFingerprintHeaders(c, account, req.Header)

	// 终态收口：强制统一 OAuth 出站身份（User-Agent / originator / version 同源自洽）。
	// 客户端自报身份不参与构造，浏览器型 UA 也因此不会再到达上游（原浏览器 UA 兜底已被吸收）。
	if account.IsOpenAIOAuthLike() {
		enforceCodexIdentityHeadersWithUA(req.Header, s.codexIdentityOverrideUA(account))
	}

	return nil
}
