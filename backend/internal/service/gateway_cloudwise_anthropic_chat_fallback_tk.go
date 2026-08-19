package service

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
)

// shouldForwardCloudwiseAnthropicViaChatCompletions reports whether an inbound
// Anthropic /v1/messages request on a CloudWise dual-stack account must use
// /v1/chat/completions. Claude stays on native messages; glm/kimi/minimax/
// deepseek follow the same chat fallback as OpenAI-platform CloudWise (#95).
func shouldForwardCloudwiseAnthropicViaChatCompletions(account *Account, parsed *ParsedRequest) bool {
	if !isCloudwiseRelayAccount(account) || parsed == nil {
		return false
	}
	if parsed.Body != nil && len(parsed.Body.Bytes()) > 0 {
		return !shouldForwardNativeAnthropicMessagesForModel(parsed.Body.Bytes())
	}
	return !tkIsForwardableAnthropicModelName(parsed.Model)
}

func (s *GatewayService) forwardCloudwiseAnthropicViaChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *ParsedRequest,
) (*ForwardResult, error) {
	if parsed == nil || parsed.Body == nil {
		return nil, fmt.Errorf("parse request: empty request")
	}
	openai := &OpenAIGatewayService{
		cfg:                  s.cfg,
		httpUpstream:         s.httpUpstream,
		responseHeaderFilter: s.responseHeaderFilter,
	}
	defaultMapped := parsed.Model
	if account != nil {
		if mapped := account.GetMappedModel(parsed.Model); mapped != "" {
			defaultMapped = mapped
		}
	}
	out, err := openai.forwardAnthropicViaRawChatCompletions(ctx, c, account, parsed.Body.Bytes(), defaultMapped)
	if out == nil {
		return nil, err
	}
	return &ForwardResult{
		RequestID:                     out.RequestID,
		Usage:                         claudeUsageFromOpenAIUsage(out.Usage),
		Model:                         out.Model,
		UpstreamModel:                 out.UpstreamModel,
		UpstreamResponseModel:         out.UpstreamResponseModel,
		UpstreamResponseModelConflict: out.UpstreamResponseModelConflict,
		Stream:                        out.Stream,
		Duration:                      out.Duration,
		FirstTokenMs:                  out.FirstTokenMs,
		ClientDisconnect:              out.ClientDisconnect,
		ReasoningEffort:               out.ReasoningEffort,
	}, err
}

func claudeUsageFromOpenAIUsage(usage OpenAIUsage) ClaudeUsage {
	return ClaudeUsage{
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
		ImageOutputTokens:        usage.ImageOutputTokens,
	}
}
