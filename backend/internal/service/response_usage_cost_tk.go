package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// responseUsageCostBillingKey holds per-request channel/pricing snapshot so
// response writers can compute the same ActualCost RecordUsage will bill.
const responseUsageCostBillingKey = "tk_response_usage_cost_billing"
const responseUsageCostPrecomputedKey = "tk_response_usage_cost_precomputed"

type responseUsageCostBilling struct {
	PricingAt time.Time
	ChannelUsageFields
	// ServiceTier is the client-requested billable tier (OpenAI service_tier /
	// Anthropic speed=fast normalized). Empty when not declared.
	ServiceTier string
}

// BindResponseUsageCostBillingWithTier freezes channel/pricing snapshot and the
// client-requested service tier so response-side cost preview matches RecordUsage.
func BindResponseUsageCostBillingWithTier(c *gin.Context, pricingAt time.Time, fields ChannelUsageFields, serviceTier string) {
	if c == nil {
		return
	}
	c.Set(responseUsageCostBillingKey, responseUsageCostBilling{
		PricingAt:          pricingAt,
		ChannelUsageFields: fields,
		ServiceTier:        strings.TrimSpace(serviceTier),
	})
}

// TakePrecomputedResponseUsageCost returns and clears a cost stashed during response write.
func TakePrecomputedResponseUsageCost(c *gin.Context) *CostBreakdown {
	if c == nil {
		return nil
	}
	v, ok := c.Get(responseUsageCostPrecomputedKey)
	if !ok {
		return nil
	}
	c.Set(responseUsageCostPrecomputedKey, nil)
	cost, _ := v.(*CostBreakdown)
	return cost
}

func stashPrecomputedResponseUsageCost(c *gin.Context, cost *CostBreakdown) {
	if c == nil || cost == nil {
		return
	}
	c.Set(responseUsageCostPrecomputedKey, cost)
}

func responseUsageCostBillingFromContext(c *gin.Context) (responseUsageCostBilling, bool) {
	if c == nil {
		return responseUsageCostBilling{}, false
	}
	v, ok := c.Get(responseUsageCostBillingKey)
	if !ok {
		return responseUsageCostBilling{}, false
	}
	snap, ok := v.(responseUsageCostBilling)
	return snap, ok
}

// InjectUsageCostJSON sets usage.cost (and nested message/response.usage.cost when
// present) to the client-visible ActualCost. No-op when cost < 0 or no usage object.
func InjectUsageCostJSON(body []byte, cost float64) []byte {
	if len(body) == 0 || cost < 0 {
		return body
	}
	for _, path := range []string{"usage", "message.usage", "response.usage"} {
		if !gjson.GetBytes(body, path).Exists() {
			continue
		}
		next, err := sjson.SetBytes(body, path+".cost", cost)
		if err != nil {
			continue
		}
		body = next
	}
	return body
}

// InjectUsageCostSSEDataLine injects usage.cost into an SSE `data: {...}` line.
// Non-data lines and [DONE] are returned unchanged.
func InjectUsageCostSSEDataLine(line string, cost float64) string {
	if cost < 0 {
		return line
	}
	payload, ok := extractOpenAISSEDataLine(line)
	if !ok {
		return line
	}
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" || trimmed == "[DONE]" {
		return line
	}
	patched := InjectUsageCostJSON([]byte(trimmed), cost)
	if string(patched) == trimmed {
		return line
	}
	prefix := "data:"
	if strings.HasPrefix(line, "data: ") {
		prefix = "data: "
	}
	return prefix + string(patched)
}

// InjectUsageCostSSEBlock patches every data: line inside a multi-line SSE event block.
func InjectUsageCostSSEBlock(block string, cost float64) string {
	if cost < 0 || block == "" {
		return block
	}
	lines := strings.Split(block, "\n")
	changed := false
	for i, line := range lines {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		next := InjectUsageCostSSEDataLine(line, cost)
		if next != line {
			lines[i] = next
			changed = true
		}
	}
	if !changed {
		return block
	}
	return strings.Join(lines, "\n")
}

// previewClaudeClientUsageCost computes ActualCost for client-visible usage.cost.
// Settlement is owned solely by settleClaudeCustomerFacingCost (same path as RecordUsage).
func (s *GatewayService) previewClaudeClientUsageCost(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	usage ClaudeUsage,
	model, upstreamModel string,
) *CostBreakdown {
	if s == nil || s.billingService == nil {
		return nil
	}
	apiKey := getAPIKeyFromContext(c)
	if apiKey == nil || apiKey.User == nil || apiKey.Group == nil {
		return nil
	}
	if usage.ImageOutputTokens > 0 {
		// Media/image paths are out of v1 scope for response cost.
		return nil
	}

	snap, _ := responseUsageCostBillingFromContext(c)
	pricingAt := snap.PricingAt
	if pricingAt.IsZero() {
		if frozen, ok := gatewayTokenRequestPricingAtFromContext(ctx); ok {
			pricingAt = frozen
		} else {
			pricingAt = timezone.Now()
		}
	}

	result := &ForwardResult{
		Usage:                         usage,
		Model:                         model,
		UpstreamModel:                 upstreamModel,
		UpstreamResponseModel:         observedUpstreamResponseModel(c),
		UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
		UpstreamResponseServiceTier:   observedUpstreamResponseServiceTier(c),
	}
	if snap.ServiceTier != "" {
		tier := snap.ServiceTier
		result.ServiceTier = &tier
	}
	ApplyForwardServiceTierBillingResolution(result)
	// Mirror RecordUsage's ForceCacheBilling mutation before settle so PrecomputedCost
	// cannot diverge from the ledger on sticky session switches.
	if IsForceCacheBilling(ctx) && result.Usage.InputTokens > 0 {
		result.Usage.CacheReadInputTokens += result.Usage.InputTokens
		result.Usage.InputTokens = 0
	}
	if overrideTarget, ok := s.resolveCacheTTLUsageOverrideTarget(ctx, account); ok {
		applyCacheTTLOverride(&result.Usage, overrideTarget)
	}

	settled, err := s.settleClaudeCustomerFacingCost(ctx, &claudeCustomerFacingCostInput{
		Result:             result,
		APIKey:             apiKey,
		User:               apiKey.User,
		Account:            account,
		PricingAt:          pricingAt,
		ChannelUsageFields: snap.ChannelUsageFields,
		Opts:               &recordUsageOpts{},
	})
	if err != nil || settled == nil || settled.Cost == nil {
		return nil
	}
	return settled.Cost
}

// previewOpenAIClientUsageCost computes ActualCost for OpenAI-shaped token responses.
// Settlement is owned solely by settleOpenAICustomerFacingCost (same path as RecordUsage).
func (s *OpenAIGatewayService) previewOpenAIClientUsageCost(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	result *OpenAIForwardResult,
) *CostBreakdown {
	if s == nil || s.billingService == nil || result == nil || account == nil {
		return nil
	}
	if result.ImageCount > 0 || result.VideoCount > 0 || result.WebSearchCalls > 0 || result.AudioUsage != nil || result.SearchCount > 0 {
		return nil
	}
	apiKey := getAPIKeyFromContext(c)
	if apiKey == nil || apiKey.User == nil || apiKey.Group == nil {
		return nil
	}

	snap, _ := responseUsageCostBillingFromContext(c)
	pricingAt := snap.PricingAt
	if pricingAt.IsZero() {
		pricingAt = timezone.Now()
	}

	billingAccount := account
	if account.IsShadow() && s.accountRepo != nil {
		if resolved, err := resolveCredentialAccount(ctx, s.accountRepo, account); err == nil && resolved != nil {
			billingAccount = resolved
		}
	}

	preview := *result
	if snap.ServiceTier != "" && preview.ServiceTier == nil {
		tier := snap.ServiceTier
		preview.ServiceTier = &tier
	}
	ApplyOpenAIServiceTierBillingResolution(billingAccount, &preview)

	settled, err := s.settleOpenAICustomerFacingCost(ctx, &openAICustomerFacingCostInput{
		Result:             &preview,
		APIKey:             apiKey,
		User:               apiKey.User,
		Account:            account,
		BillingAccount:     billingAccount,
		PricingAt:          pricingAt,
		ChannelUsageFields: snap.ChannelUsageFields,
	})
	if err != nil || settled == nil || settled.Cost == nil {
		return nil
	}
	return settled.Cost
}
