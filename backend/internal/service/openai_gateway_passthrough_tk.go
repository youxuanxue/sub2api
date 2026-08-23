package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// tkPrepareOpenAIPassthroughRequestBody applies TokenKey-specific request-body
// normalization before upstream forwarding (Codex OAuth, API-key sticky, tool
// alias). Returns updated body and stream flag; writes client response and
// returns error on hard reject (e.g. missing Codex instructions).
func (s *OpenAIGatewayService) tkPrepareOpenAIPassthroughRequestBody(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	reqModel string,
	requestedModel string,
	reqStream bool,
) ([]byte, bool, error) {
	if account != nil && account.UsesOpenAICodexProtocol() {
		if rejectReason := detectOpenAIPassthroughInstructionsRejectReason(reqModel, body); rejectReason != "" {
			rejectMsg := "OpenAI codex passthrough requires a non-empty instructions field"
			MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
			logOpenAIPassthroughInstructionsRejected(ctx, c, account, reqModel, rejectReason, body)
			c.JSON(http.StatusForbidden, gin.H{
				"error": gin.H{
					"type":    "forbidden_error",
					"message": rejectMsg,
				},
			})
			return body, reqStream, fmt.Errorf("openai passthrough rejected before upstream: %s", rejectReason)
		}
		if isOpenAICodexModel(reqModel) && !gjson.GetBytes(body, "instructions").Exists() {
			nextBody, setErr := sjson.SetBytes(body, "instructions", defaultCodexSynthInstructions(reqModel))
			if setErr != nil {
				return body, reqStream, fmt.Errorf("set passthrough codex instructions: %w", setErr)
			}
			body = nextBody
		}

		normalizedBody, normalized, err := normalizeOpenAIPassthroughOAuthBody(body, isOpenAIResponsesCompactPath(c))
		if err != nil {
			return body, reqStream, err
		}
		if normalized {
			body = normalizedBody
		}
		reqStream = gjson.GetBytes(body, "stream").Bool()

		stageCodexFingerprintIDs(c, nil)
		// 指纹收敛：与非透传路径同门控（仅 OAuth、legacy compact 形态跳过）。
		// 一次性解析收敛 ID：请求体 client_metadata 在此改写（raw 字节外科
		// 手术，透传热路径禁全量 Unmarshal），出站头改写由请求构造器读取
		// context 中的同一份 IDs 完成（turn_id 等随机字段两侧必须一致）。
		if !isOpenAIResponsesCompactPath(c) {
			var clientHeaders http.Header
			if c != nil && c.Request != nil {
				clientHeaders = c.Request.Header
			}
			fpIDs := resolveCodexFingerprintIDsFromRequest(account, clientHeaders)
			if fpIDs != nil {
				fpBody, fpChanged, fpErr := applyCodexFingerprintClientMetadataRaw(body, fpIDs)
				if fpErr != nil {
					return body, reqStream, fpErr
				}
				if fpChanged {
					body = fpBody
				}
			}
			stageCodexFingerprintIDs(c, fpIDs)
		}
	}
	if account != nil && account.Type == AccountTypeAPIKey {
		normalizedBody, normalized, err := normalizeOpenAIPassthroughAPIKeyBody(body)
		if err != nil {
			return body, reqStream, err
		}
		if normalized {
			body = normalizedBody
		}
		injectedBody, _, err := applyStickyToOpenAIResponsesBody(ctx, c, s.settingService, account, body, requestedModel)
		if err != nil {
			return body, reqStream, err
		}
		body = injectedBody
	}
	if account != nil && account.IsOpenAI() {
		normalizedBody, normalized, normalizeErr := normalizeOpenAIResponsesWebSocketCompatibilityBody(body, account)
		if normalizeErr != nil {
			return body, reqStream, fmt.Errorf("normalize passthrough Responses compatibility: %w", normalizeErr)
		}
		if normalized {
			body = normalizedBody
		}
		if account.IsOpenAIOAuthLike() {
			aliasedBody, reverse, aliased, aliasErr := aliasOpenAIOAuthReservedToolNamesBody(body)
			if aliasErr != nil {
				return body, reqStream, aliasErr
			}
			mergeCodexToolNameReverse(c, reverse)
			if aliased {
				body = aliasedBody
			}
		}
	}
	return body, reqStream, nil
}

// tkApplyOpenAIPassthroughUpstreamHeaders applies TokenKey Codex OAuth and
// compact Accept negotiation on the upstream request after auth headers are set.
func (s *OpenAIGatewayService) tkApplyOpenAIPassthroughUpstreamHeaders(
	ctx context.Context,
	c *gin.Context,
	req *http.Request,
	account *Account,
	body []byte,
) error {
	if account.UsesOpenAICodexProtocol() {
		// Current Codex OAuth HTTP no longer negotiates the legacy Responses
		// experiment. Passthrough may receive it from an older client, so remove
		// only that token while preserving any independent beta negotiation.
		stripOpenAILegacyResponsesBeta(req.Header)
		promptCacheKey := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
		req.Host = "chatgpt.com"
		if err := resolveAndSetOpenAIChatGPTAccountHeaders(ctx, s.accountRepo, req.Header, account); err != nil {
			return fmt.Errorf("resolve chatgpt account headers: %w", err)
		}
		apiKeyID := getAPIKeyIDFromContext(c)
		// 先保存客户端原始值，再做 compact 补充，避免后续统一隔离时读到已处理的值。
		clientSessionID := strings.TrimSpace(req.Header.Get("session_id"))
		clientConversationID := strings.TrimSpace(req.Header.Get("conversation_id"))
		if isOpenAIResponsesCompactPath(c) {
			req.Header.Set("accept", "application/json")
			if req.Header.Get("version") == "" {
				req.Header.Set("version", CodexCanonicalClientVersion())
			}
			if clientSessionID == "" {
				clientSessionID = resolveOpenAICompactSessionID(c)
			}
		} else if req.Header.Get("accept") == "" {
			req.Header.Set("accept", "text/event-stream")
		}
		if req.Header.Get("originator") == "" {
			req.Header.Set("originator", resolveCodexOutboundIdentity("").originator)
		}
		// 用隔离后的 session 标识符覆盖客户端透传值，防止跨用户会话碰撞。
		if clientSessionID == "" {
			clientSessionID = promptCacheKey
		}
		if clientConversationID == "" {
			clientConversationID = promptCacheKey
		}
		if clientSessionID != "" {
			req.Header.Set("session_id", isolateOpenAISessionID(apiKeyID, clientSessionID))
		}
		if clientConversationID != "" {
			req.Header.Set("conversation_id", isolateOpenAISessionID(apiKeyID, clientConversationID))
		}
	} else if isOpenAIResponsesCompactPath(c) {
		// 透传白名单会放行客户端的 Accept: text/event-stream；compact 上游是
		// unary JSON 协议，API-key 账号同样强制 Accept，避免上游按 SSE 返回
		// （#3777 期望行为 4）。
		req.Header.Set("accept", "application/json")
	}

	// 透传模式也支持账户自定义 User-Agent 与 ForceCodexCLI 兜底。
	customUA := account.GetOpenAIUserAgent()
	if customUA != "" {
		req.Header.Set("user-agent", customUA)
	}
	if s.cfg != nil && s.cfg.Gateway.ForceCodexCLI {
		req.Header.Set("user-agent", CodexCanonicalUserAgent())
	}
	// 指纹收敛：使用 forwardOpenAIPassthrough 中预计算的收敛 ID 改写出站头，
	// 与请求体 client_metadata 共享同一份 IDs（与非透传路径相同的相对位置：
	// 会话隔离之后、终态身份收口之前）。
	applyStagedCodexFingerprintHeaders(c, account, req.Header)
	// 终态收口：透传路径的 OAuth 与非透传完全一致，同样强制统一出站身份
	// （User-Agent / originator / version 同源自洽），客户端自报身份不会到达上游。
	if account.UsesOpenAICodexProtocol() {
		enforceCodexIdentityHeadersWithUA(req.Header, s.codexIdentityOverrideUA(account))
	}
	return nil
}
