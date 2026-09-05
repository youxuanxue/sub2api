package service

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// tkApplyClaudeOAuthMimicry detects Claude Code clients and, for OAuth non-CC
// traffic, applies system rewrite / normalize / tool-name rewrite.
// Returns the updated reqModel plus CC detection flags used later in Forward.
func (s *GatewayService) tkApplyClaudeOAuthMimicry(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *ParsedRequest,
	reqModel string,
	groupAdmitsNonCC bool,
	getBody func() []byte,
	replaceBody func([]byte) error,
) (newReqModel string, isClaudeCode bool, shouldMimicClaudeCode bool, err error) {
	newReqModel = reqModel
	body := getBody()

	// Claude Code 客户端判定：UA 匹配 claude-cli/* 且携带 metadata.user_id。
	// 真正的 Claude Code 客户端自带完整的 system prompt、cache_control 断点和 header，
	// 不需要代理做任何 body 级别的 mimicry；强行替换反而会破坏客户端的缓存策略
	// （长 system prompt 被替换为 ~45 tokens 的短 prompt，低于 Anthropic 1024 token
	// 最低缓存门槛，导致系统级缓存失效）。
	//
	// 对于非 Claude Code 的第三方客户端（opencode 等），仍然走完整 mimicry。
	userAgent := ""
	if c != nil && c.Request != nil {
		userAgent = c.GetHeader("User-Agent")
	}
	isClaudeCode = IsClaudeCodeClient(ctx) || isClaudeCodeClient(userAgent, parsed.MetadataUserID)

	// 补充判定：上游 API 网关（如 new-api）转发真实 Claude Code 流量时，
	// UA 会变成 Go-http-client 但 body 保留了完整的 Claude Code 特征
	// （billing attribution block + metadata.user_id）。此时如果仍走 mimicry
	// 重写 system prompt，会破坏 Anthropic prompt cache 的前缀匹配——
	// 导致 messages 级缓存永远 miss、cache_creation 每轮全量重写。
	// 通过检查 body 中的 billing attribution block 来识别被代理的真实 CC 流量。
	if !isClaudeCode && parsed.MetadataUserID != "" {
		isClaudeCode = systemHasBillingAttributionBlock(body)
	}

	shouldMimicClaudeCode = account.IsOAuth() && !isClaudeCode

	if !shouldMimicClaudeCode {
		return newReqModel, isClaudeCode, shouldMimicClaudeCode, nil
	}

	// 与 Parrot 对齐：OAuth 账号无条件重写 system（即使客户端已发了 Claude Code
	// 风格的 system prompt）。原因：第三方工具（opencode 等）会发 "You are Claude
	// Code..." system prompt 但缺少 billing attribution block，导致 Anthropic
	// 检测到"有 CC prompt 但无 billing block"的不一致而判为 third-party。
	// Parrot 的 transform_request 从不检查客户端 system 内容，直接覆盖。
	systemRewritten := false
	canonicalHaikuMimicry := s.isCanonicalAnthropicOAuth(account) &&
		(s.settingService.IsAnthropicCanonicalHaikuMimicryEnabled(ctx) || groupAdmitsNonCC)
	if shouldRewriteSystemForNonCCMimicry(newReqModel, canonicalHaikuMimicry) {
		systemRaw, _ := parsed.SystemValue()
		systemPromptInjectionEnabled, systemPrompt, systemPromptBlocks := s.claudeOAuthSystemPromptInjectionSettings(ctx)
		if systemPromptInjectionEnabled {
			if err := replaceBody(rewriteSystemForNonClaudeCodeWithPromptBlocks(body, systemRaw, systemPrompt, systemPromptBlocks)); err != nil {
				return newReqModel, isClaudeCode, shouldMimicClaudeCode, err
			}
			body = getBody()
			systemRewritten = true
		}
	}

	// system 被重写时保留 CC prompt 的 cache_control: ephemeral（匹配真实 Claude Code 行为）；
	// 未重写时（haiku / 注入开关关闭）剥离客户端 cache_control，与原有行为一致。
	// 两种情况下 enforceCacheControlLimit 都会兜底处理上限。
	normalizeOpts := claudeOAuthNormalizeOptions{stripSystemCacheControl: !systemRewritten}
	if s.identityService != nil {
		clientHeaders := http.Header{}
		if c != nil && c.Request != nil {
			clientHeaders = c.Request.Header
		}
		fp, fpErr := s.identityService.GetOrCreateFingerprint(ctx, account.ID, clientHeaders, resolveTLSProfileNameForAccount(s.tlsFPProfileService, account))
		if fpErr == nil && fp != nil {
			// metadata 透传开启时跳过 metadata 注入
			_, mimicMPT, _ := s.settingService.GetGatewayForwardingSettings(ctx)
			if !mimicMPT {
				if metadataUserID := s.buildOAuthMetadataUserID(parsed, account, fp); metadataUserID != "" {
					normalizeOpts.injectMetadata = true
					normalizeOpts.metadataUserID = metadataUserID
				}
			}
		}
	}

	var normalizedBody []byte
	normalizedBody, newReqModel = normalizeClaudeOAuthRequestBody(body, newReqModel, normalizeOpts)
	if err := replaceBody(normalizedBody); err != nil {
		return newReqModel, isClaudeCode, shouldMimicClaudeCode, err
	}
	body = getBody()

	// D/E/F: 可选 messages cache 策略 + 工具名混淆 + tools[-1] 断点
	// 与 forward_as_chat_completions / forward_as_responses 路径对齐，
	// 原生 /v1/messages 路径也走同一套可配置字段级改写。
	if err := replaceBody(s.rewriteMessageCacheControlIfEnabled(ctx, body)); err != nil {
		return newReqModel, isClaudeCode, shouldMimicClaudeCode, err
	}
	body = getBody()
	if rw := buildToolNameRewriteFromBody(body); rw != nil {
		if err := replaceBody(applyToolNameRewriteToBody(body, rw)); err != nil {
			return newReqModel, isClaudeCode, shouldMimicClaudeCode, err
		}
		if c != nil {
			c.Set(toolNameRewriteKey, rw)
		}
	} else {
		if err := replaceBody(applyToolsLastCacheBreakpoint(body)); err != nil {
			return newReqModel, isClaudeCode, shouldMimicClaudeCode, err
		}
	}

	return newReqModel, isClaudeCode, shouldMimicClaudeCode, nil
}
