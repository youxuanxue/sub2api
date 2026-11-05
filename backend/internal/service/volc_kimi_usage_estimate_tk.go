package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tokenestimate"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// Volc Agent Plan kimi-k3 sometimes answers HTTP 200 with correct content but
// all-zero usage (non-stream) or no usage chunk (stream). cn-prod 2026-11-03/04:
// the zero rate follows the account, not the request shape. Product decision
// (2026-11-04): bill a local estimate instead of $0. Real upstream usage always
// wins. See docs/approved/volc-kimi-chat-compatibility.md.
const (
	// VolcKimiEstimatedBillingTier stamps usage_logs.billing_tier, mirroring
	// geminiweb.EstimatedBillingTier / kiro-estimated.
	VolcKimiEstimatedBillingTier = "volc-kimi-estimated"
	volcKimiUsageEstimatedKind   = "volc_kimi_usage_estimated"
	volcKimiUsageEstimatedMsg    = "upstream returned a successful kimi-k3 response without billable usage; billed a local estimate"
)

// requiresVolcKimiUsageEstimate scopes the fallback to the same supplier
// recognition and model family as normalizeVolcKimiChatRequest.
func requiresVolcKimiUsageEstimate(account *Account, model string) bool {
	return isNewAPIVolcEngineAgentPlanAccount(account) && resolveMoonshotThinkingFamily(model) == moonshotThinkingK3
}

// openAIUsageLogBillingTier persists only the Volc estimate marker on the
// OpenAI usage path. Other OpenAI-path tiers keep their historical behavior
// (not written by this RecordUsage).
func openAIUsageLogBillingTier(result *OpenAIForwardResult) *string {
	if result == nil || result.BillingTier != VolcKimiEstimatedBillingTier {
		return nil
	}
	tier := result.BillingTier
	return &tier
}

// estimateVolcKimiInputTokens counts what the upstream prompt sees: message
// text (string or text parts), assistant tool_calls, tool results and tool
// declarations. Images/video are skipped, like the Kiro and Gemini Web owners.
func estimateVolcKimiInputTokens(body []byte) int {
	parts := make([]string, 0, 8)
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		switch {
		case content.Type == gjson.String:
			parts = append(parts, content.String())
		case content.IsArray():
			content.ForEach(func(_, part gjson.Result) bool {
				if t := part.Get("text"); t.Exists() {
					parts = append(parts, t.String())
				}
				return true
			})
		}
		if rc := msg.Get("reasoning_content"); rc.Type == gjson.String {
			parts = append(parts, rc.String())
		}
		msg.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
			parts = append(parts, call.Get("function.name").String(), call.Get("function.arguments").String())
			return true
		})
		return true
	})
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() && len(tools.Array()) > 0 {
		parts = append(parts, tools.Raw)
	}
	return tokenestimate.Count(strings.TrimSpace(strings.Join(parts, "\n")))
}

// volcKimiOutputText collects delivered output from a chat.completion body or
// one chat.completion.chunk payload.
func volcKimiOutputText(payload gjson.Result) string {
	parts := make([]string, 0, 4)
	payload.Get("choices").ForEach(func(_, choice gjson.Result) bool {
		for _, node := range []gjson.Result{choice.Get("message"), choice.Get("delta")} {
			if !node.Exists() {
				continue
			}
			parts = append(parts, node.Get("reasoning_content").String(), node.Get("content").String())
			node.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
				parts = append(parts, call.Get("function.name").String(), call.Get("function.arguments").String())
				return true
			})
		}
		return true
	})
	return strings.Join(parts, "")
}

// estimateVolcKimiUsage returns (usage, true) when usage carries no billable
// tokens and the estimate is non-empty. Real usage is never replaced.
func estimateVolcKimiUsage(usage OpenAIUsage, requestBody []byte, outputText string) (OpenAIUsage, bool) {
	if hasBillableGrokChatUsage(usage) || usage.ImageOutputTokens > 0 {
		return usage, false
	}
	estimated := OpenAIUsage{
		InputTokens:  estimateVolcKimiInputTokens(requestBody),
		OutputTokens: tokenestimate.Count(strings.TrimSpace(outputText)),
	}
	if estimated.InputTokens <= 0 && estimated.OutputTokens <= 0 {
		return usage, false
	}
	return estimated, true
}

// setVolcKimiEstimatedUsageJSON writes the billed estimate into a chat usage
// object so client-visible usage matches what is billed. Detail objects are
// dropped: the estimate has no cache/reasoning split.
func setVolcKimiEstimatedUsageJSON(payload []byte, usage OpenAIUsage) []byte {
	next, err := sjson.SetBytes(payload, "usage", map[string]any{
		"prompt_tokens":     usage.InputTokens,
		"completion_tokens": usage.OutputTokens,
		"total_tokens":      usage.InputTokens + usage.OutputTokens,
	})
	if err != nil {
		return payload
	}
	return next
}

// volcKimiEstimatedUsageChunk builds the usage-only chunk appended before
// [DONE] when the upstream stream carried none (OpenAI include_usage shape).
func volcKimiEstimatedUsageChunk(id, model string, created int64, usage OpenAIUsage) string {
	chunk := []byte(`{"object":"chat.completion.chunk","choices":[]}`)
	chunk, _ = sjson.SetBytes(chunk, "id", id)
	chunk, _ = sjson.SetBytes(chunk, "created", created)
	chunk, _ = sjson.SetBytes(chunk, "model", model)
	return "data: " + string(setVolcKimiEstimatedUsageJSON(chunk, usage))
}

func recordVolcKimiUsageEstimated(c *gin.Context, account *Account, requestID, model string, stream bool, usage OpenAIUsage) {
	accountID, accountName := int64(0), ""
	if account != nil {
		accountID, accountName = account.ID, account.Name
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:           opsUpstreamProxyID(account),
		ProxyName:         opsUpstreamProxyName(account),
		Platform:          PlatformNewAPI,
		AccountID:         accountID,
		AccountName:       accountName,
		UpstreamRequestID: strings.TrimSpace(requestID),
		Kind:              volcKimiUsageEstimatedKind,
		Message:           volcKimiUsageEstimatedMsg,
	})
	logger.L().Warn("openai chat_completions raw: volc kimi usage estimated",
		zap.String("code", volcKimiUsageEstimatedKind),
		zap.Int64("account_id", accountID),
		zap.String("upstream_request_id", strings.TrimSpace(requestID)),
		zap.String("upstream_model", model),
		zap.Bool("stream", stream),
		zap.Int("estimated_input_tokens", usage.InputTokens),
		zap.Int("estimated_output_tokens", usage.OutputTokens),
	)
}

const volcKimiEstimateRequestBodyKey = "tk_volc_kimi_estimate_request_body"

// stashVolcKimiEstimateRequestBody keeps the final outbound body for the
// response readers, which do not receive it. Only Volc kimi-k3 requests stash.
func stashVolcKimiEstimateRequestBody(c *gin.Context, account *Account, model string, body []byte) {
	if c == nil || !requiresVolcKimiUsageEstimate(account, model) {
		return
	}
	c.Set(volcKimiEstimateRequestBodyKey, body)
}

func volcKimiEstimateRequestBody(c *gin.Context) []byte {
	if c == nil {
		return nil
	}
	v, _ := c.Get(volcKimiEstimateRequestBodyKey)
	body, _ := v.([]byte)
	return body
}

// applyVolcKimiBufferedUsageEstimate fills a non-billable non-stream usage
// with the estimate, in both the billed result and the client body. Responses
// with no delivered output stay untouched (silent-refusal class).
func applyVolcKimiBufferedUsageEstimate(c *gin.Context, account *Account, model, requestID string, usage OpenAIUsage, respBody []byte) (OpenAIUsage, []byte, bool) {
	if !requiresVolcKimiUsageEstimate(account, model) {
		return usage, respBody, false
	}
	output := volcKimiOutputText(gjson.ParseBytes(respBody))
	if strings.TrimSpace(output) == "" {
		return usage, respBody, false
	}
	estimated, ok := estimateVolcKimiUsage(usage, volcKimiEstimateRequestBody(c), output)
	if !ok {
		return usage, respBody, false
	}
	recordVolcKimiUsageEstimated(c, account, requestID, model, false, estimated)
	return estimated, setVolcKimiEstimatedUsageJSON(respBody, estimated), true
}

// volcKimiStreamUsageEstimator owns the stream fallback. Upstream usage-only
// chunks with non-billable usage are patched in place; a stream that reaches
// [DONE] without billable usage gets one usage-only chunk inserted before
// [DONE] (the include_usage shape the gateway already forces upstream). If the
// stream ends without [DONE], Finalize still bills the estimate without a
// client chunk. Nothing happens when no output was delivered.
type volcKimiStreamUsageEstimator struct {
	enabled     bool
	c           *gin.Context
	account     *Account
	model       string
	clientModel string
	requestID   string
	output      strings.Builder
	id          string
	created     int64
	estimated   bool
}

func newVolcKimiStreamUsageEstimator(c *gin.Context, account *Account, upstreamModel, clientModel, requestID string) *volcKimiStreamUsageEstimator {
	return &volcKimiStreamUsageEstimator{
		enabled:     requiresVolcKimiUsageEstimate(account, upstreamModel),
		c:           c,
		account:     account,
		model:       upstreamModel,
		clientModel: clientModel,
		requestID:   requestID,
	}
}

// Observe records delivered output and chunk identity from one data payload.
func (e *volcKimiStreamUsageEstimator) Observe(payload string) {
	if e == nil || !e.enabled {
		return
	}
	root := gjson.Parse(payload)
	if e.id == "" {
		e.id = root.Get("id").String()
	}
	if e.created == 0 {
		e.created = root.Get("created").Int()
	}
	_, _ = e.output.WriteString(volcKimiOutputText(root))
}

func (e *volcKimiStreamUsageEstimator) estimate(usage *OpenAIUsage) bool {
	if e == nil || !e.enabled || e.estimated || usage == nil || strings.TrimSpace(e.output.String()) == "" {
		return false
	}
	estimated, ok := estimateVolcKimiUsage(*usage, volcKimiEstimateRequestBody(e.c), e.output.String())
	if !ok {
		return false
	}
	*usage = estimated
	e.estimated = true
	recordVolcKimiUsageEstimated(e.c, e.account, e.requestID, e.model, true, estimated)
	return true
}

// PatchUsageOnlyLine rewrites a non-billable usage-only chunk with the estimate.
func (e *volcKimiStreamUsageEstimator) PatchUsageOnlyLine(line string, usage *OpenAIUsage) string {
	payload, ok := extractOpenAISSEDataLine(line)
	if !ok || !e.estimate(usage) {
		return line
	}
	return "data: " + string(setVolcKimiEstimatedUsageJSON([]byte(strings.TrimSpace(payload)), *usage))
}

// UsageLineBeforeDone returns the estimated usage-only chunk to insert before
// [DONE], or "" when upstream usage is billable or nothing was delivered.
func (e *volcKimiStreamUsageEstimator) UsageLineBeforeDone(usage *OpenAIUsage) string {
	if !e.estimate(usage) {
		return ""
	}
	return volcKimiEstimatedUsageChunk(e.id, e.clientModel, e.created, *usage)
}

// Finalize bills the estimate for streams that ended without [DONE].
func (e *volcKimiStreamUsageEstimator) Finalize(usage *OpenAIUsage) {
	e.estimate(usage)
}

func (e *volcKimiStreamUsageEstimator) BillingTier() string {
	if e == nil || !e.estimated {
		return ""
	}
	return VolcKimiEstimatedBillingTier
}
