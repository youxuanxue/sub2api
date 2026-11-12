package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tokenestimate"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// Zero-usage estimate owner (gateway-wide, all platforms, all text models).
//
// Product decisions: 2026-11-04 "如果上游就返回 usage 为 0，我们网关可以通过估算兜底吧。
// 不然就亏了。" and 2026-11-05 "应该全平台全模型统一吧？遵循 ssot 原则。"
// Contract: docs/approved/usage-estimate-fallback-ssot.md.
//
// Rules owned here and nowhere else:
//   - Real upstream usage always wins: any non-zero billable bucket (input,
//     output, cache read, cache creation, image input/output) disables the
//     estimate.
//   - A response that delivered no output text is not estimated (silent
//     refusal class), and neither are error statuses or in-band stream errors.
//   - Media results (images, video, audio) are priced by their own units and
//     are never estimated.
//   - Native estimators (Kiro, Gemini Web) already settle from the same
//     tokenizer by design; this owner skips them so nothing double-estimates.
//   - Grok non-stream missing usage still fails over before any write
//     (openai_gateway_usage_integrity.go); only results that reach a billing
//     choke point after output was written are estimated here.
//
// Billing choke points call GatewayService.ApplyUsageEstimateFallback
// (ForwardResult) or OpenAIGatewayService.ApplyOpenAIUsageEstimateFallback
// (OpenAIForwardResult) right before the
// RecordUsage task is submitted. A forward that returned an error, or whose
// client disconnected, keeps the usage the upstream reported (partial
// delivery is not a successful response). The delivered output comes from the
// usage-estimate capture writer (usage_estimate_capture_tk.go), which also
// writes the estimate back into the client-visible usage.
const (
	// UsageEstimatedBillingTier stamps usage_logs.billing_tier for every
	// estimate minted by this owner. It is a label only; it does not change
	// price resolution. Native estimators keep their historical tiers.
	UsageEstimatedBillingTier = "usage-estimated"
	usageEstimatedOpsKind     = "usage_estimated"
	usageEstimatedOpsMessage  = "upstream returned a successful response without billable usage; billed a local estimate"
)

// usageEstimateNativeEstimator names the platforms that estimate locally by
// design (no upstream usage exists). The owner never adds a second estimate.
func usageEstimateNativeEstimator(account *Account) (string, bool) {
	switch {
	case account == nil:
		return "", false
	case account.Platform == PlatformKiro:
		return "kiro", true
	case isGeminiWebAccount(account):
		return "gemini-web", true
	}
	return "", false
}

// usageEstimateTokens is the protocol-neutral estimate.
type usageEstimateTokens struct {
	Input  int
	Output int
}

func (u usageEstimateTokens) empty() bool { return u.Input <= 0 && u.Output <= 0 }

func claudeUsageHasBillableTokens(u ClaudeUsage) bool {
	return u.InputTokens > 0 || u.OutputTokens > 0 ||
		u.CacheReadInputTokens > 0 || u.CacheCreationInputTokens > 0 ||
		u.CacheCreation5mTokens > 0 || u.CacheCreation1hTokens > 0 ||
		u.ImageOutputTokens > 0
}

func openAIUsageHasBillableTokens(u OpenAIUsage) bool {
	return u.InputTokens > 0 || u.OutputTokens > 0 ||
		u.CacheReadInputTokens > 0 || u.CacheCreationInputTokens > 0 ||
		u.CacheCreation5mTokens > 0 || u.CacheCreation1hTokens > 0 ||
		u.ImageInputTokens > 0 || u.ImageCacheReadTokens > 0 || u.ImageOutputTokens > 0
}

// ApplyUsageEstimateFallback is the billing choke point hook for the
// Anthropic / Gemini / Antigravity / Bedrock / Claude-compat result type.
// inboundBody is the client request body; the final upstream body wins when a
// forward path stashed it. The estimate is priced once here: the same
// CostBreakdown becomes PrecomputedCost and the client usage.cost.
func (s *GatewayService) ApplyUsageEstimateFallback(ctx context.Context, c *gin.Context, account *Account, result *ForwardResult, inboundBody []byte, forwardErr error) bool {
	defer releaseUsageEstimateCapture(c)
	if result == nil || forwardErr != nil || result.ClientDisconnect || result.ImageCount > 0 || result.AudioUsage != nil || claudeUsageHasBillableTokens(result.Usage) {
		return false
	}
	return applyUsageEstimate(c, account, result.Model, result.UpstreamModel, result.RequestID, result.Stream, inboundBody, func(est usageEstimateTokens) *CostBreakdown {
		result.Usage = ClaudeUsage{InputTokens: est.Input, OutputTokens: est.Output}
		result.BillingTier = UsageEstimatedBillingTier
		result.PrecomputedCost = s.previewClaudeClientUsageCost(ctx, c, account, result.Usage, result.Model, result.UpstreamModel)
		return result.PrecomputedCost
	})
}

// ApplyOpenAIUsageEstimateFallback is the same hook for the OpenAI-compatible
// result type (OpenAI/Codex, newapi bridge and raw, Grok, OpenRouter, Volc).
func (s *OpenAIGatewayService) ApplyOpenAIUsageEstimateFallback(ctx context.Context, c *gin.Context, account *Account, result *OpenAIForwardResult, inboundBody []byte, forwardErr error) bool {
	defer releaseUsageEstimateCapture(c)
	if result == nil || forwardErr != nil || result.ClientDisconnect || result.ImageCount > 0 || result.VideoCount > 0 || result.AudioUsage != nil || openAIUsageHasBillableTokens(result.Usage) {
		return false
	}
	return applyUsageEstimate(c, account, result.Model, result.UpstreamModel, result.RequestID, result.Stream, inboundBody, func(est usageEstimateTokens) *CostBreakdown {
		result.Usage = OpenAIUsage{InputTokens: est.Input, OutputTokens: est.Output}
		result.BillingTier = UsageEstimatedBillingTier
		result.PrecomputedCost = s.previewOpenAIClientUsageCost(ctx, c, account, result)
		return result.PrecomputedCost
	})
}

// releaseUsageEstimateCapture writes held client bytes at the billing choke
// point, whatever the estimate decision: the response is final once the
// forward returned, and nothing after this point (usage submission may run
// inline when the record pool overflows under the default "sync" policy) may
// delay delivery.
func releaseUsageEstimateCapture(c *gin.Context) {
	_ = usageEstimateCaptureFromContext(c).release()
}

// applyUsageEstimate decides, then lets the caller stamp and price its result
// type, then hands the billed numbers and cost to the client usage writer.
func applyUsageEstimate(c *gin.Context, account *Account, model, upstreamModel, requestID string, stream bool, inboundBody []byte, stamp func(usageEstimateTokens) *CostBreakdown) bool {
	capture := usageEstimateCaptureFromContext(c)
	est, ok := decideUsageEstimate(c, account, model, upstreamModel, requestID, stream, inboundBody)
	if !ok {
		return false
	}
	cost := stamp(est)
	var total *float64
	if cost != nil {
		t := cost.TotalCost
		total = &t
	}
	capture.applyEstimate(est, total)
	return true
}

func decideUsageEstimate(c *gin.Context, account *Account, model, upstreamModel, requestID string, stream bool, inboundBody []byte) (usageEstimateTokens, bool) {
	if c == nil {
		return usageEstimateTokens{}, false
	}
	if _, native := usageEstimateNativeEstimator(account); native {
		return usageEstimateTokens{}, false
	}
	capture := usageEstimateCaptureFromContext(c)
	if capture == nil || !capture.deliveredSuccess() || len(GetOpsStreamErrors(c)) > 0 {
		return usageEstimateTokens{}, false
	}
	output := strings.TrimSpace(capture.deliveredOutputText())
	if output == "" {
		return usageEstimateTokens{}, false
	}
	requestBody := usageEstimateRequestBody(c, inboundBody)
	input := tokenestimate.Count(usageEstimateRequestText(requestBody))
	// K3 image tokens share the text input price. Count them only after a
	// zero-usage decision, and only for this family: other models keep the
	// text-only estimate. Remote image URLs stay uncounted.
	if isMoonshotK3FamilyModel(model) || isMoonshotK3FamilyModel(upstreamModel) {
		input += tokenestimate.KimiK3ImageInputTokens(requestBody)
	}
	est := usageEstimateTokens{
		Input:  input,
		Output: tokenestimate.Count(output),
	}
	if est.empty() {
		return usageEstimateTokens{}, false
	}
	recordUsageEstimated(c, account, firstNonEmpty(upstreamModel, model), requestID, capture.protocol, stream, est)
	return est, true
}

// usageEstimateUpstreamBodyKey holds the final outbound body for paths whose
// wire body differs from the inbound one (provider normalization).
const usageEstimateUpstreamBodyKey = "tk_usage_estimate_upstream_body"

// StashUsageEstimateUpstreamBody records the final outbound body so the owner
// counts what the upstream actually received. It stores the slice reference
// only; callers must not mutate it afterwards.
func StashUsageEstimateUpstreamBody(c *gin.Context, body []byte) {
	if c == nil || len(body) == 0 {
		return
	}
	c.Set(usageEstimateUpstreamBodyKey, body)
}

// usageEstimateRequestBody prefers the stashed outbound body, then the ops
// upstream wire body, then the inbound body. A Gemini cloudcode envelope
// ({"request":{...}}) is unwrapped. An outbound body without countable text
// falls back to the inbound body.
func usageEstimateRequestBody(c *gin.Context, inbound []byte) []byte {
	for _, key := range []string{usageEstimateUpstreamBodyKey, OpsUpstreamRequestBodyKey} {
		v, _ := c.Get(key)
		body, _ := v.([]byte)
		if len(body) == 0 {
			continue
		}
		if inner := gjson.GetBytes(body, "request"); inner.IsObject() && inner.Get("contents").Exists() {
			body = []byte(inner.Raw)
		}
		if usageEstimateRequestText(body) != "" {
			return body
		}
	}
	if len(inbound) == 0 {
		v, _ := c.Get(OpsRequestBodyKey)
		inbound, _ = v.([]byte)
	}
	return inbound
}

func recordUsageEstimated(c *gin.Context, account *Account, model, requestID, protocol string, stream bool, est usageEstimateTokens) {
	accountID, accountName, platform := int64(0), "", ""
	if account != nil {
		accountID, accountName, platform = account.ID, account.Name, account.Platform
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:           opsUpstreamProxyID(account),
		ProxyName:         opsUpstreamProxyName(account),
		Platform:          platform,
		AccountID:         accountID,
		AccountName:       accountName,
		UpstreamRequestID: strings.TrimSpace(requestID),
		Kind:              usageEstimatedOpsKind,
		Message:           usageEstimatedOpsMessage,
		Detail: fmt.Sprintf("model=%s protocol=%s stream=%t estimated_input_tokens=%d estimated_output_tokens=%d",
			strings.TrimSpace(model), protocol, stream, est.Input, est.Output),
	})
	logger.L().Warn("gateway: usage estimated",
		zap.String("code", usageEstimatedOpsKind),
		zap.String("platform", platform),
		zap.Int64("account_id", accountID),
		zap.String("upstream_request_id", strings.TrimSpace(requestID)),
		zap.String("model", strings.TrimSpace(model)),
		zap.String("protocol", protocol),
		zap.Bool("stream", stream),
		zap.Int("estimated_input_tokens", est.Input),
		zap.Int("estimated_output_tokens", est.Output),
	)
}

// openAIUsageLogBillingTier persists only the estimate marker on the OpenAI
// usage path. Other OpenAI-path tiers keep their historical behavior (not
// written by this RecordUsage).
func openAIUsageLogBillingTier(result *OpenAIForwardResult) *string {
	if result == nil || result.BillingTier != UsageEstimatedBillingTier {
		return nil
	}
	tier := result.BillingTier
	return &tier
}
