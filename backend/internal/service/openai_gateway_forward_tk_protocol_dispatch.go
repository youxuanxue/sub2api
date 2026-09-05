package service

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
)

// tkTryRouteOpenAIForwardProtocol handles TokenKey early protocol routing after
// the Grok early-return and before the main /v1/responses path.
//
// When an API-key body normalization mutates the payload, outBody carries the
// updated bytes so Forward can refresh requestView / originalBody. Callers must
// treat outBody as authoritative whenever handled is false.
func (s *OpenAIGatewayService) tkTryRouteOpenAIForwardProtocol(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	reqModel string,
) (result *OpenAIForwardResult, outBody []byte, handled bool, err error) {
	outBody = body

	if target, planned := protocolExecutionTarget(ctx); planned {
		switch target {
		case protocolrouter.ProtocolMessages:
			result, err = s.forwardResponsesViaNativeAnthropic(ctx, c, account, outBody, reqModel)
			return result, outBody, true, err
		case protocolrouter.ProtocolChatCompletions:
			result, err = s.forwardResponsesViaRawChatCompletions(ctx, c, account, outBody)
			return result, outBody, true, err
		case protocolrouter.ProtocolResponses:
			// Continue through the native Responses transport below.
		default:
			return nil, outBody, true, fmt.Errorf("unsupported selected protocol target %q", target)
		}
	}

	// CN 供应商 anthropic 协议账号：/v1/responses 入站是交叉协议组合
	// （Responses 客户端 × Anthropic 上游），转成 Anthropic 请求走原生端点。
	// 不能落到下面的 raw-CC 分支——其 URL 构造会把 anthropic base 当 CC base 用。
	if account.IsAnthropicProtocol() {
		result, err = s.forwardResponsesViaNativeAnthropic(ctx, c, account, outBody, reqModel)
		return result, outBody, true, err
	}
	if account.IsOpenAIApiKey() {
		if normalized, changed, normalizeErr := normalizeOpenAIParallelToolCallsWithoutTools(outBody); normalizeErr != nil {
			return nil, outBody, true, normalizeErr
		} else if changed {
			outBody = normalized
		}
		if normalized, changed, normalizeErr := normalizeOpenAIAPIKeyStoreFalseReasoningReplay(outBody, isOpenAIResponsesCompactPath(c)); normalizeErr != nil {
			return nil, outBody, true, normalizeErr
		} else if changed {
			outBody = normalized
		}
	}

	if shouldForwardOpenAIResponsesViaRawChatCompletions(account) {
		result, err = s.forwardResponsesViaRawChatCompletions(ctx, c, account, outBody)
		return result, outBody, true, err
	}
	return nil, outBody, false, nil
}

func shouldForwardOpenAIResponsesViaRawChatCompletions(account *Account) bool {
	if account == nil || account.Type != AccountTypeAPIKey {
		return false
	}
	if account.IsCNProvider() {
		// CN 的显式协议配置优先于异步探针 Extra；adaptive 仅 DeepSeek 有原生
		// Responses，Kimi/GLM 回退 Chat Completions。
		switch account.GetAPIProtocol() {
		case APIProtocolChatCompletions:
			return true
		case APIProtocolAdaptive:
			return account.Platform != PlatformDeepseek
		case APIProtocolResponses, APIProtocolAnthropic:
			return false
		default:
			return false
		}
	}
	// Dual-stack MaaS relays advertise /v1/responses (probe treats 400 as
	// "endpoint exists") but only implement Chat + Anthropic Messages.
	if account.IsOpenAICloudwiseRelay() || account.IsOpenAITokenseaRelay() {
		return true
	}
	return !openai_compat.ShouldUseResponsesAPI(account.Extra)
}
