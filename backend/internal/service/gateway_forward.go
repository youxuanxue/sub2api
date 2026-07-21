package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"

	"github.com/gin-gonic/gin"
)

// 重试相关常量
const (
	// 最大尝试次数（包含首次请求）。过多重试会导致请求堆积与资源耗尽。
	maxRetryAttempts = 5

	// 指数退避：第 N 次失败后的等待 = retryBaseDelay * 2^(N-1)，并且上限为 retryMaxDelay。
	retryBaseDelay = 300 * time.Millisecond
	retryMaxDelay  = 3 * time.Second

	// 最大重试耗时（包含请求本身耗时 + 退避等待时间）。
	// 用于防止极端情况下 goroutine 长时间堆积导致资源耗尽。
	maxRetryElapsed = 10 * time.Second
)

func (s *GatewayService) shouldRetryUpstreamError(account *Account, statusCode int) bool {
	// OAuth/Setup Token 账号：仅 403 重试
	if account.IsOAuth() {
		return statusCode == 403
	}

	// API Key 账号：未配置的错误码重试
	return !account.ShouldHandleErrorCode(statusCode)
}

// shouldFailoverUpstreamError determines whether an upstream error should trigger account failover.
func (s *GatewayService) shouldFailoverUpstreamError(statusCode int) bool {
	switch statusCode {
	case 401, 403, 429, 529:
		return true
	default:
		return statusCode >= 500
	}
}

func retryBackoffDelay(attempt int) time.Duration {
	// attempt 从 1 开始，表示第 attempt 次请求刚失败，需要等待后进行第 attempt+1 次请求。
	if attempt <= 0 {
		return retryBaseDelay
	}
	delay := retryBaseDelay * time.Duration(1<<(attempt-1))
	if delay > retryMaxDelay {
		return retryMaxDelay
	}
	return delay
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Forward 转发请求到Claude API
func (s *GatewayService) Forward(ctx context.Context, c *gin.Context, account *Account, parsed *ParsedRequest) (*ForwardResult, error) {
	startTime := time.Now()
	if parsed == nil {
		return nil, fmt.Errorf("parse request: empty request")
	}

	// Web Search 模拟：纯 web_search 请求时，直接调用搜索 API 构造响应
	if account != nil && s.shouldEmulateWebSearch(ctx, account, parsed.GroupID, parsed.Body.Bytes()) {
		return s.handleWebSearchEmulation(ctx, c, account, parsed)
	}

	if account != nil && account.IsAnthropicAPIKeyPassthroughEnabled() {
		passthroughBody := parsed.Body.Bytes()
		passthroughModel := parsed.Model
		if passthroughModel != "" {
			if mappedModel := account.GetMappedModel(passthroughModel); mappedModel != passthroughModel {
				passthroughBody = s.replaceModelInBody(passthroughBody, mappedModel)
				logger.LegacyPrintf("service.gateway", "Passthrough model mapping: %s -> %s (account: %s)", parsed.Model, mappedModel, account.Name)
				passthroughModel = mappedModel
			}
		}
		return s.forwardAnthropicAPIKeyPassthroughWithInput(ctx, c, account, anthropicPassthroughForwardInput{
			Body:          passthroughBody,
			Parsed:        parsed,
			RequestModel:  passthroughModel,
			OriginalModel: parsed.Model,
			RequestStream: parsed.Stream,
			StartTime:     startTime,
		})
	}

	if account != nil && account.IsAnthropicOAuthPassthroughEnabled() {
		return s.forwardAnthropicOAuthPassthroughWithInput(ctx, c, account, anthropicPassthroughForwardInput{
			Body:          parsed.Body.Bytes(),
			Parsed:        parsed,
			RequestModel:  parsed.Model,
			OriginalModel: parsed.Model,
			RequestStream: parsed.Stream,
			StartTime:     startTime,
		})
	}

	if account != nil && account.IsBedrock() {
		return s.forwardBedrock(ctx, c, account, parsed, startTime)
	}

	if account != nil && account.IsKiro() {
		if s.kiroGateway == nil {
			return nil, fmt.Errorf("kiro gateway service not configured")
		}
		result, err := s.kiroGateway.Forward(ctx, c, account, parsed, startTime)
		if err != nil && s.rateLimitService != nil {
			var failoverErr *UpstreamFailoverError
			if errors.As(err, &failoverErr) {
				s.rateLimitService.HandleUpstreamError(ctx, account, failoverErr.StatusCode, failoverErr.ResponseHeaders, failoverErr.ResponseBody)
			}
		}
		return result, err
	}

	// Beta policy: evaluate once; block check + cache filter set for buildUpstreamRequest.
	// Always overwrite the cache to prevent stale values from a previous retry with a different account.
	if account.Platform == PlatformAnthropic && c != nil {
		policy := s.evaluateBetaPolicy(ctx, c.GetHeader("anthropic-beta"), account, parsed.Model)
		if policy.blockErr != nil {
			return nil, policy.blockErr
		}
		filterSet := policy.filterSet
		if filterSet == nil {
			filterSet = map[string]struct{}{}
		}
		c.Set(betaPolicyFilterSetKey, filterSet)
	}

	body := parsed.Body.Bytes()
	replaceBody := func(next []byte) error {
		if err := parsed.ReplaceBody(next); err != nil {
			return fmt.Errorf("rewrite request body: %w", err)
		}
		body = parsed.Body.Bytes()
		return nil
	}
	reqModel := parsed.Model
	reqStream := parsed.Stream
	originalModel := reqModel

	// TK: strip the Claude Code 1M-context model alias suffix before model
	// mapping / scheduling / pricing. See gateway_anthropic_context_window_alias_tk.go.
	if account.Platform == PlatformAnthropic {
		if bare, aliased := tkStripContextWindowModelAlias(reqModel); aliased {
			if err := replaceBody(s.replaceModelInBody(body, bare)); err != nil {
				return nil, err
			}
			logger.LegacyPrintf("service.gateway",
				"TK context-window alias stripped before forward (prevents Anthropic 404 + silent 200K fallback, claude-code #60913): %s -> %s account=%d",
				reqModel, bare, account.ID)
			reqModel, parsed.Model, originalModel = bare, bare, bare
		}
	}

	// TK canonical-OAuth ingress gates. cc_only=false groups admit non-CC
	// traffic and complete the disguise on egress via haiku mimicry below.
	groupAdmitsNonCC := s.tkGroupAdmitsNonCC(ctx, parsed)
	if c != nil && c.Request != nil && s.isCanonicalAnthropicOAuth(account) {
		if s.settingService.IsAnthropicCanonicalIngressStrictEnabled(ctx) {
			if err := checkCanonicalIngressUAStrict(c.Request.Header); err != nil {
				return nil, err
			}
		} else if !groupAdmitsNonCC {
			if err := checkCanonicalIngressUA(c.Request.Header); err != nil {
				return nil, err
			}
		}
		if newModel, remapped := remapDeprecatedOpusOnCanonical(reqModel); remapped {
			if err := replaceBody(s.replaceModelInBody(body, newModel)); err != nil {
				return nil, err
			}
			logger.LegacyPrintf("service.gateway",
				"Canonical OAuth model remap: %s -> %s (account: %s)",
				reqModel, newModel, account.Name)
			reqModel, parsed.Model = newModel, newModel
		}
	}

	// === DEBUG: 打印客户端原始请求（headers + body 摘要）===
	if c != nil && c.Request != nil {
		s.debugLogGatewaySnapshot("CLIENT_ORIGINAL", c.Request.Header, body, map[string]string{
			"account":      fmt.Sprintf("%d(%s)", account.ID, account.Name),
			"account_type": string(account.Type),
			"model":        reqModel,
			"stream":       strconv.FormatBool(reqStream),
		})
	}

	// TK: normalize Anthropic native request body before downstream rewrites.
	if account.Platform == PlatformAnthropic {
		body = s.tkNormalizeAnthropicRequestBody(ctx, c, body, account)
	}

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
	isClaudeCode := IsClaudeCodeClient(ctx) || isClaudeCodeClient(userAgent, parsed.MetadataUserID)
	shouldMimicClaudeCode := account.IsOAuth() && !isClaudeCode

	if shouldMimicClaudeCode {
		// 与 Parrot 对齐：OAuth 账号无条件重写 system（即使客户端已发了 Claude Code
		// 风格的 system prompt）。原因：第三方工具（opencode 等）会发 "You are Claude
		// Code..." system prompt 但缺少 billing attribution block，导致 Anthropic
		// 检测到"有 CC prompt 但无 billing block"的不一致而判为 third-party。
		// Parrot 的 transform_request 从不检查客户端 system 内容，直接覆盖。
		systemRewritten := false
		canonicalHaikuMimicry := s.isCanonicalAnthropicOAuth(account) &&
			(s.settingService.IsAnthropicCanonicalHaikuMimicryEnabled(ctx) || groupAdmitsNonCC)
		if shouldRewriteSystemForNonCCMimicry(reqModel, canonicalHaikuMimicry) {
			systemRaw, _ := parsed.SystemValue()
			systemPromptInjectionEnabled, systemPrompt, systemPromptBlocks := s.claudeOAuthSystemPromptInjectionSettings(ctx)
			if systemPromptInjectionEnabled {
				if err := replaceBody(rewriteSystemForNonClaudeCodeWithPromptBlocks(body, systemRaw, systemPrompt, systemPromptBlocks)); err != nil {
					return nil, err
				}
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
			fp, err := s.identityService.GetOrCreateFingerprint(ctx, account.ID, clientHeaders, resolveTLSProfileNameForAccount(s.tlsFPProfileService, account))
			if err == nil && fp != nil {
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
		normalizedBody, reqModel = normalizeClaudeOAuthRequestBody(body, reqModel, normalizeOpts)
		if err := replaceBody(normalizedBody); err != nil {
			return nil, err
		}

		// D/E/F: 可选 messages cache 策略 + 工具名混淆 + tools[-1] 断点
		// 与 forward_as_chat_completions / forward_as_responses 路径对齐，
		// 原生 /v1/messages 路径也走同一套可配置字段级改写。
		if err := replaceBody(s.rewriteMessageCacheControlIfEnabled(ctx, body)); err != nil {
			return nil, err
		}
		if rw := buildToolNameRewriteFromBody(body); rw != nil {
			if err := replaceBody(applyToolNameRewriteToBody(body, rw)); err != nil {
				return nil, err
			}
			if c != nil {
				c.Set(toolNameRewriteKey, rw)
			}
		} else {
			if err := replaceBody(applyToolsLastCacheBreakpoint(body)); err != nil {
				return nil, err
			}
		}
	}

	// 客户端 dateline 归一化：仅对 Anthropic OAuth/SetupToken 账号生效。
	// 抹除 "Today's date is …" 语句里可能被注入的隐写指纹（4 种撇号 × 2 种日期
	// 分隔符），还原为 ASCII 撇号 + "-" 分隔符。运行在 mimicry 分支之外，
	// 保证真实 Claude Code 客户端注入的指纹同样被清洗。
	if next, ok := s.normalizeClientDatelineIfEnabled(ctx, account, body); ok {
		if err := replaceBody(next); err != nil {
			return nil, err
		}
	}

	// 强制执行 cache_control 块数量限制（最多 4 个）
	if err := replaceBody(enforceCacheControlLimit(body)); err != nil {
		return nil, err
	}

	// 应用模型映射：
	// - APIKey 账号：使用账号级别的显式映射（如果配置），否则透传原始模型名
	// - OAuth/SetupToken 账号：使用 Anthropic 标准映射（短ID → 长ID）
	mappedModel := reqModel
	mappingSource := ""
	if account.Type == AccountTypeAPIKey {
		mappedModel = account.GetMappedModel(reqModel)
		if mappedModel != reqModel {
			mappingSource = "account"
		}
	}
	if mappingSource == "" && account.Platform == PlatformAnthropic && account.Type == AccountTypeServiceAccount {
		if candidate, matched := account.ResolveMappedModel(reqModel); matched {
			mappedModel = candidate
			mappingSource = "account"
		} else {
			normalized := normalizeVertexAnthropicModelID(claude.NormalizeModelID(reqModel))
			if normalized != reqModel {
				mappedModel = normalized
				mappingSource = "vertex"
			}
		}
	}
	if mappingSource == "" && account.Platform == PlatformAnthropic && account.Type != AccountTypeAPIKey {
		normalized := claude.NormalizeModelID(reqModel)
		if normalized != reqModel {
			mappedModel = normalized
			mappingSource = "prefix"
		}
	}
	if mappedModel != reqModel {
		// 替换请求体中的模型名
		if err := replaceBody(s.replaceModelInBody(body, mappedModel)); err != nil {
			return nil, err
		}
		reqModel = mappedModel
		parsed.Model = mappedModel
		logger.LegacyPrintf("service.gateway", "Model mapping applied: %s -> %s (account: %s, source=%s)", originalModel, mappedModel, account.Name, mappingSource)
	}
	if account.Platform == PlatformAnthropic {
		if replacement, deprecated := tkIsDeprecatedAnthropicModel(mappedModel); deprecated {
			TkWriteAnthropicDeprecatedModelError(c, mappedModel, replacement)
			return nil, fmt.Errorf("anthropic model %q is retired (suggest %q)", mappedModel, replacement)
		}
	}

	if !s.tkPricedServingGate(ctx, c, tkGateWireAnthropic, account.Platform, originalModel, originalModel) {
		return nil, fmt.Errorf("priced serving gate: model %q not priced for platform %q", originalModel, account.Platform)
	}

	if handled, ncErr := s.tkModelNotFoundShortCircuit(c, account, mappedModel); handled {
		return nil, ncErr
	}

	if s.shouldInjectAnthropicCacheTTL1h(ctx, account) {
		if err := replaceBody(injectAnthropicCacheControlTTL1h(body)); err != nil {
			return nil, err
		}
	}

	// 获取凭证
	token, tokenType, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	// 获取代理URL（自定义 base URL 模式下，proxy 通过 buildCustomRelayURL 作为查询参数传递）
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		if !account.IsCustomBaseURLEnabled() || account.GetCustomBaseURL() == "" {
			proxyURL = account.Proxy.URL()
		}
	}

	// 解析 TLS 指纹 profile（同一请求生命周期内不变，避免重试循环中重复解析）
	tlsProfile := resolveOpsTLSFingerprintProfile(c, s.tlsFPProfileService, account)

	// 调试日志：记录即将转发的账号信息
	logger.LegacyPrintf("service.gateway", "[Forward] Using account: ID=%d Name=%s Platform=%s Type=%s TLSFingerprint=%v Proxy=%s",
		account.ID, account.Name, account.Platform, account.Type, tlsProfile, proxyURL)
	// Pre-filter: sanitize invalid UTF-8 / lone surrogate escapes, strip empty
	// text blocks, drop explicit disabled thinking for Fable, and strip fields
	// rejected by newer Anthropic models before upstream.
	if err := replaceBody(tkApplyAnthropicRequestCompatibilityRules(account, tkStripFableDisabledThinking(StripEmptyTextBlocks(TkSanitizeRequestBody(body, account))))); err != nil {
		return nil, err
	}
	// Pre-filter: strip web-search history blocks the upstream cannot accept
	// (emulation-synthesized server_tool_use / web_search_tool_result always;
	// genuine ones additionally for passback-required upstreams). See
	// FilterWebSearchHistoryBlocks. reqModel 此时已是映射后的模型 ID。
	if err := replaceBody(FilterWebSearchHistoryBlocks(body, reqModel)); err != nil {
		return nil, err
	}
	// Pre-filter: remove thinking blocks with missing/invalid signatures before forwarding.
	// Clients (e.g. Claude Code) sometimes send multi-turn conversations where a historical
	// assistant message contains a thinking block that is missing the required "signature" field,
	// causing upstream to reject the request with 400 "thinking.signature: Field required".
	// FilterThinkingBlocks removes only the invalid blocks; thinking blocks with valid signatures
	// are preserved. This avoids relying solely on the post-error retry path, which can time out
	// (maxRetryElapsed = 10s) for long conversations before the retry budget is exhausted.
	//
	// 仅 anthropic-strict 模型族执行此过滤；passback-required 上游 (DeepSeek/Kimi/GLM 等)
	// 要求历史 thinking block 原样回传，过滤反而制造 400。reqModel 此时已是映射后的模型 ID。
	if err := replaceBody(FilterThinkingBlocks(body, reqModel)); err != nil {
		return nil, err
	}
	// TK: ToolSearch dynamic loading can perturb historical signed thinking
	// blocks; strip those before the first upstream call.
	if err := replaceBody(TkPrefilterToolSearchHistoricalThinking(body, reqModel)); err != nil {
		return nil, err
	}
	// Chinese LLM thinking.type 协议差异补正（如 MiniMax 只接受 adaptive；Anthropic-SDK
	// 客户端默认发 enabled）。仅对 passback-required 上游生效（claude-* 不会进来）。
	if ResolveThinkingProtocol(reqModel) == ThinkingProtocolPassbackRequired {
		if rewritten, applied := NormalizeChineseLLMThinking(body, reqModel); applied {
			if err := replaceBody(rewritten); err != nil {
				return nil, err
			}
			logger.LegacyPrintf("service.gateway", "Account %d: rewrote thinking.type for %s (Anthropic-SDK default 'enabled' -> vendor-specific)", account.ID, reqModel)
		}
	}
	if account.Platform == PlatformAnthropic {
		body = s.applySigPreemptIfArmed(ctx, c, account, body, reqModel)
	}
	if account.Platform == PlatformAnthropic {
		if err := s.tkRejectInvalidAnthropicToolContext(ctx, c, account, body, s.tkRequiresClaudeCodeSystemSurface(ctx, c, account), false); err != nil {
			return nil, err
		}
	}
	if account.Platform == PlatformAnthropic {
		stickyBody, _, err := applyStickyToAnthropicMessagesBody(ctx, c, s.settingService, account, body, reqModel, isClaudeCode)
		if err != nil {
			return nil, err
		}
		if err := replaceBody(stickyBody); err != nil {
			return nil, err
		}
	}
	setOpsUpstreamRequestBody(c, body)

	// 重试循环
	var resp *http.Response
	lastWireBody := body
	retryStart := time.Now()
	for attempt := 1; attempt <= maxRetryAttempts; attempt++ {
		// 构建上游请求（每次重试需要重新构建，因为请求体需要重新读取）
		upstreamCtx, releaseUpstreamCtx := detachStreamUpstreamContext(ctx, reqStream)
		upstreamReq, wireBody, err := s.buildUpstreamRequest(upstreamCtx, c, account, body, token, tokenType, reqModel, reqStream, shouldMimicClaudeCode)
		releaseUpstreamCtx()
		if err != nil {
			return nil, err
		}
		// 记录本次实际发送的 wire body；只有请求成功后才写回 ParsedRequest，避免 400 retry 基于已签名 CCH 再改写。
		lastWireBody = wireBody

		// 发送请求
		hwka := s.beginHeaderWaitKeepalive(c, reqStream)
		resp, err = s.httpUpstream.DoWithTLS(upstreamReq, proxyURL, account.ID, account.Concurrency, tlsProfile)
		hwka.stop()
		if err != nil {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			// Ensure the client receives an error response (handlers assume Forward writes on non-failover errors).
			safeErr := sanitizeUpstreamErrorMessage(err.Error())
			setOpsUpstreamError(c, 0, safeErr, "")
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: 0,
				UpstreamURL:        safeUpstreamURL(upstreamReq.URL.String()),
				Kind:               "request_error",
				Message:            safeErr,
			})
			// The outbound request inherits the caller context on non-streaming
			// requests. If that caller disconnected, let the handler terminate the
			// request as 499; writing a 502 here mislabels client cancellation as an
			// upstream failure and there is no downstream client left to receive it.
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				return nil, err
			}
			c.JSON(http.StatusBadGateway, gin.H{
				"type": "error",
				"error": gin.H{
					"type":    "upstream_error",
					"message": "Upstream request failed",
				},
			})
			return nil, fmt.Errorf("upstream request failed: %s", safeErr)
		}

		// 优先检测thinking block签名错误（400）并重试一次
		if resp.StatusCode == 400 {
			respBody, readErr := s.readUpstreamErrorBody(resp)
			if readErr == nil {
				_ = resp.Body.Close()
				tkRecordAnthropicSamplingParamRuleFrom400(account, reqModel, body, resp.StatusCode, respBody)
				tkRecordAnthropicThinkingRuleFrom400(account, reqModel, body, resp.StatusCode, respBody)

				if s.shouldRectifySignatureError(ctx, account, respBody, reqModel) {
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
						Platform:           account.Platform,
						AccountID:          account.ID,
						AccountName:        account.Name,
						UpstreamStatusCode: resp.StatusCode,
						UpstreamRequestID:  resp.Header.Get("x-request-id"),
						UpstreamURL:        safeUpstreamURL(upstreamReq.URL.String()),
						Kind:               "signature_error",
						Message:            extractUpstreamErrorMessage(respBody),
						Detail: func() string {
							if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
								return truncateString(string(respBody), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
							}
							return ""
						}(),
					})
					s.armSigPreemptOnError(ctx, c, account)

					looksLikeToolSignatureError := func(msg string) bool {
						m := strings.ToLower(msg)
						return strings.Contains(m, "tool_use") ||
							strings.Contains(m, "tool_result") ||
							strings.Contains(m, "functioncall") ||
							strings.Contains(m, "function_call") ||
							strings.Contains(m, "functionresponse") ||
							strings.Contains(m, "function_response")
					}

					// 避免在重试预算已耗尽时再发起额外请求
					if time.Since(retryStart) >= maxRetryElapsed {
						resp.Body = io.NopCloser(bytes.NewReader(respBody))
						break
					}
					logger.LegacyPrintf("service.gateway", "[warn] Account %d: thinking blocks have invalid signature, retrying with filtered blocks", account.ID)

					// Conservative two-stage fallback:
					// 1) Disable thinking + thinking->text (preserve content)
					// 2) Only if upstream still errors AND error message points to tool/function signature issues:
					//    also downgrade tool_use/tool_result blocks to text.

					filteredBody := FilterThinkingBlocksForRetry(body, reqModel)
					retryCtx, releaseRetryCtx := detachStreamUpstreamContext(ctx, reqStream)
					retryReq, retryWireBody, buildErr := s.buildUpstreamRequest(retryCtx, c, account, filteredBody, token, tokenType, reqModel, reqStream, shouldMimicClaudeCode)
					releaseRetryCtx()
					if buildErr == nil {
						retryResp, retryErr := s.httpUpstream.DoWithTLS(retryReq, proxyURL, account.ID, account.Concurrency, tlsProfile)
						if retryErr == nil {
							if retryResp.StatusCode < 400 {
								// 重试请求被上游接受后同步 ParsedRequest，保证 usage/日志看到真实请求体。
								lastWireBody = retryWireBody
								if err := replaceBody(retryWireBody); err != nil {
									_ = retryResp.Body.Close()
									return nil, err
								}
								setOpsUpstreamRequestBody(c, retryWireBody)
								logger.LegacyPrintf("service.gateway", "Account %d: thinking block retry succeeded (blocks downgraded)", account.ID)
								resp = retryResp
								break
							}

							retryRespBody, retryReadErr := s.readUpstreamErrorBody(retryResp)
							_ = retryResp.Body.Close()
							if retryReadErr == nil && retryResp.StatusCode == 400 && s.isSignatureErrorPattern(ctx, account, retryRespBody) {
								appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
									Platform:           account.Platform,
									AccountID:          account.ID,
									AccountName:        account.Name,
									UpstreamStatusCode: retryResp.StatusCode,
									UpstreamRequestID:  retryResp.Header.Get("x-request-id"),
									UpstreamURL:        safeUpstreamURL(retryReq.URL.String()),
									Kind:               "signature_retry_thinking",
									Message:            extractUpstreamErrorMessage(retryRespBody),
									Detail: func() string {
										if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
											return truncateString(string(retryRespBody), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
										}
										return ""
									}(),
								})
								msg2 := extractUpstreamErrorMessage(retryRespBody)
								if looksLikeToolSignatureError(msg2) && time.Since(retryStart) < maxRetryElapsed {
									logger.LegacyPrintf("service.gateway", "Account %d: signature retry still failing and looks tool-related, retrying with tool blocks downgraded", account.ID)
									filteredBody2 := FilterSignatureSensitiveBlocksForRetry(body, reqModel)
									retryCtx2, releaseRetryCtx2 := detachStreamUpstreamContext(ctx, reqStream)
									retryReq2, retryWireBody2, buildErr2 := s.buildUpstreamRequest(retryCtx2, c, account, filteredBody2, token, tokenType, reqModel, reqStream, shouldMimicClaudeCode)
									releaseRetryCtx2()
									if buildErr2 == nil {
										retryResp2, retryErr2 := s.httpUpstream.DoWithTLS(retryReq2, proxyURL, account.ID, account.Concurrency, tlsProfile)
										if retryErr2 == nil {
											if retryResp2.StatusCode < 400 {
												// 二阶段工具块降级成功时也必须更新当前 body。
												lastWireBody = retryWireBody2
												if err := replaceBody(retryWireBody2); err != nil {
													_ = retryResp2.Body.Close()
													return nil, err
												}
												setOpsUpstreamRequestBody(c, retryWireBody2)
											}
											resp = retryResp2
											break
										}
										if retryResp2 != nil && retryResp2.Body != nil {
											_ = retryResp2.Body.Close()
										}
										appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
											Platform:           account.Platform,
											AccountID:          account.ID,
											AccountName:        account.Name,
											UpstreamStatusCode: 0,
											UpstreamURL:        safeUpstreamURL(retryReq2.URL.String()),
											Kind:               "signature_retry_tools_request_error",
											Message:            sanitizeUpstreamErrorMessage(retryErr2.Error()),
										})
										logger.LegacyPrintf("service.gateway", "Account %d: tool-downgrade signature retry failed: %v", account.ID, retryErr2)
									} else {
										logger.LegacyPrintf("service.gateway", "Account %d: tool-downgrade signature retry build failed: %v", account.ID, buildErr2)
									}
								}
							}

							// Fall back to the original retry response context.
							resp = &http.Response{
								StatusCode: retryResp.StatusCode,
								Header:     retryResp.Header.Clone(),
								Body:       io.NopCloser(bytes.NewReader(retryRespBody)),
							}
							break
						}
						if retryResp != nil && retryResp.Body != nil {
							_ = retryResp.Body.Close()
						}
						logger.LegacyPrintf("service.gateway", "Account %d: signature error retry failed: %v", account.ID, retryErr)
					} else {
						logger.LegacyPrintf("service.gateway", "Account %d: signature error retry build request failed: %v", account.ID, buildErr)
					}

					// Retry failed: restore original response body and continue handling.
					resp.Body = io.NopCloser(bytes.NewReader(respBody))
					break
				}
				// 不是签名错误（或整流器已关闭），继续检查 budget 约束
				if rejected := parseRejectedAnthropicBetas(respBody); len(rejected) > 0 {
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
						Platform:           account.Platform,
						AccountID:          account.ID,
						AccountName:        account.Name,
						UpstreamStatusCode: resp.StatusCode,
						UpstreamRequestID:  resp.Header.Get("x-request-id"),
						UpstreamURL:        safeUpstreamURL(upstreamReq.URL.String()),
						Kind:               "anthropic_beta_rejected",
						Message:            extractUpstreamErrorMessage(respBody),
						Detail: func() string {
							if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
								return truncateString(string(respBody), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
							}
							return ""
						}(),
					})
					if time.Since(retryStart) < maxRetryElapsed {
						logger.LegacyPrintf("service.gateway", "[warn] Account %d: upstream rejected anthropic-beta token(s) %v; retrying once with them dropped (manifest needs update)", account.ID, rejected)
						betaRetryCtx, releaseBetaRetryCtx := detachStreamUpstreamContext(ctx, reqStream)
						betaRetryCtx = withBetaSelfHealDrop(betaRetryCtx, rejected)
						betaRetryReq, betaWireBody, buildErr := s.buildUpstreamRequest(betaRetryCtx, c, account, body, token, tokenType, reqModel, reqStream, shouldMimicClaudeCode)
						releaseBetaRetryCtx()
						if buildErr == nil {
							betaRetryResp, retryErr := s.httpUpstream.DoWithTLS(betaRetryReq, proxyURL, account.ID, account.Concurrency, tlsProfile)
							if retryErr == nil {
								if betaRetryResp.StatusCode < 400 {
									lastWireBody = betaWireBody
									if err := replaceBody(betaWireBody); err != nil {
										_ = betaRetryResp.Body.Close()
										return nil, err
									}
									setOpsUpstreamRequestBody(c, betaWireBody)
									logger.LegacyPrintf("service.gateway", "Account %d: anthropic-beta self-heal succeeded after dropping %v", account.ID, rejected)
									resp = betaRetryResp
									break
								}
								if betaRetryResp.Body != nil {
									_ = betaRetryResp.Body.Close()
								}
								logger.LegacyPrintf("service.gateway", "Account %d: anthropic-beta self-heal retry still failed (status=%d)", account.ID, betaRetryResp.StatusCode)
							} else {
								if betaRetryResp != nil && betaRetryResp.Body != nil {
									_ = betaRetryResp.Body.Close()
								}
								logger.LegacyPrintf("service.gateway", "Account %d: anthropic-beta self-heal retry request failed: %v", account.ID, retryErr)
							}
						} else {
							logger.LegacyPrintf("service.gateway", "Account %d: anthropic-beta self-heal build request failed: %v", account.ID, buildErr)
						}
					}
					resp.Body = io.NopCloser(bytes.NewReader(respBody))
				}

				errMsg := extractUpstreamErrorMessage(respBody)
				if isThinkingTypeAdaptiveRequiredError(errMsg) {
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
						Platform:           account.Platform,
						AccountID:          account.ID,
						AccountName:        account.Name,
						UpstreamStatusCode: resp.StatusCode,
						UpstreamRequestID:  resp.Header.Get("x-request-id"),
						UpstreamURL:        safeUpstreamURL(upstreamReq.URL.String()),
						Kind:               "thinking_adaptive_error",
						Message:            errMsg,
						Detail: func() string {
							if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
								return truncateString(string(respBody), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
							}
							return ""
						}(),
					})

					rectifiedBody, applied := RectifyThinkingTypeAdaptive(body)
					if applied && time.Since(retryStart) < maxRetryElapsed {
						logger.LegacyPrintf("service.gateway", "Account %d: detected thinking.type adaptive-only error, retrying with adaptive thinking", account.ID)
						adaptiveRetryCtx, releaseAdaptiveRetryCtx := detachStreamUpstreamContext(ctx, reqStream)
						adaptiveRetryReq, adaptiveWireBody, buildErr := s.buildUpstreamRequest(adaptiveRetryCtx, c, account, rectifiedBody, token, tokenType, reqModel, reqStream, shouldMimicClaudeCode)
						releaseAdaptiveRetryCtx()
						if buildErr == nil {
							adaptiveRetryResp, retryErr := s.httpUpstream.DoWithTLS(adaptiveRetryReq, proxyURL, account.ID, account.Concurrency, tlsProfile)
							if retryErr == nil {
								if adaptiveRetryResp.StatusCode < 400 {
									lastWireBody = adaptiveWireBody
									if err := replaceBody(adaptiveWireBody); err != nil {
										_ = adaptiveRetryResp.Body.Close()
										return nil, err
									}
									setOpsUpstreamRequestBody(c, adaptiveWireBody)
								}
								resp = adaptiveRetryResp
								break
							}
							if adaptiveRetryResp != nil && adaptiveRetryResp.Body != nil {
								_ = adaptiveRetryResp.Body.Close()
							}
							logger.LegacyPrintf("service.gateway", "Account %d: thinking adaptive retry failed: %v", account.ID, retryErr)
						} else {
							logger.LegacyPrintf("service.gateway", "Account %d: thinking adaptive retry build failed: %v", account.ID, buildErr)
						}
					}
				}
				if isThinkingBudgetConstraintError(errMsg) && s.settingService.IsBudgetRectifierEnabled(ctx) {
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
						Platform:           account.Platform,
						AccountID:          account.ID,
						AccountName:        account.Name,
						UpstreamStatusCode: resp.StatusCode,
						UpstreamRequestID:  resp.Header.Get("x-request-id"),
						UpstreamURL:        safeUpstreamURL(upstreamReq.URL.String()),
						Kind:               "budget_constraint_error",
						Message:            errMsg,
						Detail: func() string {
							if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
								return truncateString(string(respBody), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
							}
							return ""
						}(),
					})

					rectifiedBody, applied := RectifyThinkingBudget(body, reqModel)
					if applied && time.Since(retryStart) < maxRetryElapsed {
						logger.LegacyPrintf("service.gateway", "Account %d: detected budget_tokens constraint error, retrying with rectified budget (budget_tokens=%d, max_tokens=%d)", account.ID, BudgetRectifyBudgetTokens, BudgetRectifyMaxTokens)
						budgetRetryCtx, releaseBudgetRetryCtx := detachStreamUpstreamContext(ctx, reqStream)
						budgetRetryReq, budgetWireBody, buildErr := s.buildUpstreamRequest(budgetRetryCtx, c, account, rectifiedBody, token, tokenType, reqModel, reqStream, shouldMimicClaudeCode)
						releaseBudgetRetryCtx()
						if buildErr == nil {
							budgetRetryResp, retryErr := s.httpUpstream.DoWithTLS(budgetRetryReq, proxyURL, account.ID, account.Concurrency, tlsProfile)
							if retryErr == nil {
								if budgetRetryResp.StatusCode < 400 {
									// budget 修正请求成功后，ParsedRequest 也要描述被接受的修正版。
									lastWireBody = budgetWireBody
									if err := replaceBody(budgetWireBody); err != nil {
										_ = budgetRetryResp.Body.Close()
										return nil, err
									}
								}
								resp = budgetRetryResp
								break
							}
							if budgetRetryResp != nil && budgetRetryResp.Body != nil {
								_ = budgetRetryResp.Body.Close()
							}
							logger.LegacyPrintf("service.gateway", "Account %d: budget rectifier retry failed: %v", account.ID, retryErr)
						} else {
							logger.LegacyPrintf("service.gateway", "Account %d: budget rectifier retry build failed: %v", account.ID, buildErr)
						}
					}
				}

				resp.Body = io.NopCloser(bytes.NewReader(respBody))
			}
		}

		if resp.StatusCode == http.StatusForbidden && account.IsOAuth() {
			peekedFatalBody, _ := s.readUpstreamErrorBody(resp)
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(peekedFatalBody))
			if s.tkIsAccountFatal403(account, peekedFatalBody) {
				logger.LegacyPrintf("service.gateway", "Account %d: account-fatal 403 (org-ban/bodyless), skipping in-place retry, failing over",
					account.ID)
				break
			}
		}
		if err, terminal := kiroSilentRefusalFromRelay(c, account, resp.Header, resp.StatusCode); terminal {
			_, _ = s.readUpstreamErrorBody(resp)
			_ = resp.Body.Close()
			return nil, err
		}

		// 检查是否需要通用重试（排除400，因为400已经在上面特殊处理过了）
		if resp.StatusCode >= 400 && resp.StatusCode != 400 && s.shouldRetryUpstreamError(account, resp.StatusCode) {
			if attempt < maxRetryAttempts {
				elapsed := time.Since(retryStart)
				if elapsed >= maxRetryElapsed {
					break
				}

				delay := retryBackoffDelay(attempt)
				remaining := maxRetryElapsed - elapsed
				if delay > remaining {
					delay = remaining
				}
				if delay <= 0 {
					break
				}

				respBody, _ := s.readUpstreamErrorBody(resp)
				_ = resp.Body.Close()
				appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
					Platform:           account.Platform,
					AccountID:          account.ID,
					AccountName:        account.Name,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  resp.Header.Get("x-request-id"),
					UpstreamURL:        safeUpstreamURL(upstreamReq.URL.String()),
					Kind:               "retry",
					Message:            extractUpstreamErrorMessage(respBody),
					Detail: func() string {
						if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
							return truncateString(string(respBody), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
						}
						return ""
					}(),
				})
				logger.LegacyPrintf("service.gateway", "Account %d: upstream error %d, retry %d/%d after %v (elapsed=%v/%v)",
					account.ID, resp.StatusCode, attempt, maxRetryAttempts, delay, elapsed, maxRetryElapsed)
				if err := sleepWithContext(ctx, delay); err != nil {
					return nil, err
				}
				continue
			}
			// 最后一次尝试也失败，跳出循环处理重试耗尽
			break
		}

		// 不需要重试（成功或不可重试的错误），跳出循环
		// DEBUG: 输出响应 headers（用于检测 rate limit 信息）
		if account.Platform == PlatformGemini && resp.StatusCode < 400 && s.cfg != nil && s.cfg.Gateway.GeminiDebugResponseHeaders {
			logger.LegacyPrintf("service.gateway", "[DEBUG] Gemini API Response Headers for account %d:", account.ID)
			for k, v := range resp.Header {
				logger.LegacyPrintf("service.gateway", "[DEBUG]   %s: %v", k, v)
			}
		}
		break
	}
	if resp == nil || resp.Body == nil {
		return nil, errors.New("upstream request failed: empty response")
	}
	defer func() { _ = resp.Body.Close() }()

	// 处理重试耗尽的情况
	if resp.StatusCode >= 400 && s.shouldRetryUpstreamError(account, resp.StatusCode) {
		if s.shouldFailoverUpstreamError(resp.StatusCode) {
			respBody, _ := s.readUpstreamErrorBody(resp)
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(respBody))

			// 调试日志：打印重试耗尽后的错误响应
			logger.LegacyPrintf("service.gateway", "[Forward] Upstream error (retry exhausted, failover): Account=%d(%s) Status=%d RequestID=%s Body=%s",
				account.ID, account.Name, resp.StatusCode, resp.Header.Get("x-request-id"), truncateString(string(respBody), 1000))

			if result, err, handled := s.tkHandleAnthropicRequestOwned429(c, account, resp, respBody); handled {
				return result, err
			}
			s.handleRetryExhaustedSideEffects(ctx, resp, account)
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-request-id"),
				Kind:               "retry_exhausted_failover",
				Message:            extractUpstreamErrorMessage(respBody),
				Detail: func() string {
					if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
						return truncateString(string(respBody), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
					}
					return ""
				}(),
			})
			return nil, &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseBody:           respBody,
				RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
			}
		}
		return s.handleRetryExhaustedError(ctx, resp, c, account)
	}

	// 处理可切换账号的错误
	if resp.StatusCode >= 400 && s.shouldFailoverUpstreamError(resp.StatusCode) {
		respBody, _ := s.readUpstreamErrorBody(resp)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))

		// 调试日志：打印上游错误响应
		logger.LegacyPrintf("service.gateway", "[Forward] Upstream error (failover): Account=%d(%s) Status=%d RequestID=%s Body=%s",
			account.ID, account.Name, resp.StatusCode, resp.Header.Get("x-request-id"), truncateString(string(respBody), 1000))

		if result, err, handled := s.tkHandleAnthropicRequestOwned429(c, account, resp, respBody); handled {
			return result, err
		}
		s.handleFailoverSideEffects(ctx, resp, account, reqModel)
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "failover",
			Message:            extractUpstreamErrorMessage(respBody),
			Detail: func() string {
				if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
					return truncateString(string(respBody), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
				}
				return ""
			}(),
		})
		return nil, &UpstreamFailoverError{
			StatusCode:             resp.StatusCode,
			ResponseBody:           respBody,
			RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
		}
	}
	if resp.StatusCode >= 400 {
		// 可选：对部分 400 触发 failover（默认关闭以保持语义）
		if resp.StatusCode == 400 && s.cfg != nil && s.cfg.Gateway.FailoverOn400 {
			respBody, readErr := s.readUpstreamErrorBody(resp)
			if readErr != nil {
				// ReadAll failed, fall back to normal error handling without consuming the stream
				return s.handleErrorResponse(ctx, resp, c, account, reqModel)
			}
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(respBody))

			if s.shouldFailoverOn400(respBody) {
				upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
				upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
				upstreamDetail := ""
				if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
					maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
					if maxBytes <= 0 {
						maxBytes = 2048
					}
					upstreamDetail = truncateString(string(respBody), maxBytes)
				}
				appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
					Platform:           account.Platform,
					AccountID:          account.ID,
					AccountName:        account.Name,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  resp.Header.Get("x-request-id"),
					Kind:               "failover_on_400",
					Message:            upstreamMsg,
					Detail:             upstreamDetail,
				})

				if s.cfg.Gateway.LogUpstreamErrorBody {
					logger.LegacyPrintf("service.gateway",
						"Account %d: 400 error, attempting failover: %s",
						account.ID,
						truncateForLog(respBody, s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes),
					)
				} else {
					logger.LegacyPrintf("service.gateway", "Account %d: 400 error, attempting failover", account.ID)
				}
				s.handleFailoverSideEffects(ctx, resp, account, reqModel)
				return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: respBody}
			}
		}
		return s.handleErrorResponse(ctx, resp, c, account, reqModel)
	}

	// 处理正常响应
	applyKiroInternalThinkingFromUpstream(c, resp.Header)

	if !bytes.Equal(lastWireBody, body) {
		// 成功后再同步最终 wire body，避免失败重试从已签名 CCH 的 body 继续派生。
		if err := replaceBody(lastWireBody); err != nil {
			return nil, err
		}
	}

	// 触发上游接受回调（提前释放串行锁，不等流完成）
	if parsed.OnUpstreamAccepted != nil {
		parsed.OnUpstreamAccepted()
	}

	var usage *ClaudeUsage
	var firstTokenMs *int
	var clientDisconnect bool
	if reqStream {
		streamResult, err := s.handleStreamingResponse(ctx, resp, c, account, startTime, originalModel, reqModel, shouldMimicClaudeCode)
		if err != nil {
			var sseErr *sseStreamErrorEventError
			if errors.As(err, &sseErr) {
				return nil, s.sseStreamErrorFailover(c, account, resp, sseErr)
			}
			return nil, err
		}
		usage = streamResult.usage
		firstTokenMs = streamResult.firstTokenMs
		clientDisconnect = streamResult.clientDisconnect
	} else {
		usage, err = s.handleNonStreamingResponse(ctx, resp, c, account, originalModel, reqModel)
		if err != nil {
			return nil, err
		}
	}

	return &ForwardResult{
		RequestID:        resp.Header.Get("x-request-id"),
		Usage:            *usage,
		Model:            originalModel, // 使用原始模型用于计费和日志
		UpstreamModel:    mappedModel,
		Stream:           reqStream,
		Duration:         time.Since(startTime),
		FirstTokenMs:     firstTokenMs,
		ClientDisconnect: clientDisconnect,
	}, nil
}

// ResolveChannelMapping 委托渠道服务解析模型映射
func (s *GatewayService) ResolveChannelMapping(ctx context.Context, groupID int64, model string) ChannelMappingResult {
	if s.channelService == nil {
		return ChannelMappingResult{MappedModel: model}
	}
	return s.channelService.ResolveChannelMapping(ctx, groupID, model)
}

// ReplaceModelInBody 替换请求体中的模型名（导出供 handler 使用）
func (s *GatewayService) ReplaceModelInBody(body []byte, newModel string) []byte {
	return ReplaceModelInBody(body, newModel)
}

// IsModelRestricted 检查模型是否被渠道限制
func (s *GatewayService) IsModelRestricted(ctx context.Context, groupID int64, model string) bool {
	if s.channelService == nil {
		return false
	}
	return s.channelService.IsModelRestricted(ctx, groupID, model)
}

// ResolveChannelMappingAndRestrict 解析渠道映射。
// 模型限制检查已移至调度阶段（checkChannelPricingRestriction），restricted 始终返回 false。
func (s *GatewayService) ResolveChannelMappingAndRestrict(ctx context.Context, groupID *int64, model string) (ChannelMappingResult, bool) {
	if s.channelService == nil {
		return ChannelMappingResult{MappedModel: model}, false
	}
	return s.channelService.ResolveChannelMappingAndRestrict(ctx, groupID, model)
}
