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

// BindResponseUsageCostBilling stores channel + pricing snapshot for client-visible
// usage.cost injection. Safe to call once after channel mapping is resolved.
func BindResponseUsageCostBilling(c *gin.Context, pricingAt time.Time, fields ChannelUsageFields) {
	BindResponseUsageCostBillingWithTier(c, pricingAt, fields, "")
}

// BindResponseUsageCostBillingWithTier also freezes the client-requested service tier
// so response-side cost preview matches RecordUsage service-tier settlement.
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

// previewClaudeClientUsageCost computes ActualCost for client-visible usage.cost using
// the same token billing path as RecordUsage. Returns nil when cost must not be exposed.
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
	if overrideTarget, ok := s.resolveCacheTTLUsageOverrideTarget(ctx, account); ok {
		applyCacheTTLOverride(&result.Usage, overrideTarget)
	}

	multiplier := 1.0
	if s.cfg != nil {
		multiplier = s.cfg.Default.RateMultiplier
	}
	if apiKey.GroupID != nil {
		multiplier = s.getUserGroupRateMultiplier(ctx, apiKey.User.ID, *apiKey.GroupID, apiKey.Group.RateMultiplier)
	}
	multiplier, imageMultiplier := computePeakAwareMultipliers(apiKey, multiplier, pricingAt)

	concreteBillingModel := forwardResultBillingModel(result.Model, result.UpstreamModel)
	billingModel := concreteBillingModel
	if snap.BillingModelSource == BillingModelSourceChannelMapped && snap.ChannelMappedModel != "" {
		billingModel = snap.ChannelMappedModel
	}
	if snap.BillingModelSource == BillingModelSourceRequested && snap.OriginalModel != "" {
		billingModel = snap.OriginalModel
	}
	if apiKey.Group.Platform == PlatformComposite {
		billingModel = s.compositeBillableModel(ctx, apiKey, billingModel, concreteBillingModel)
	}
	requestedModel := result.Model
	if snap.OriginalModel != "" {
		requestedModel = snap.OriginalModel
	}
	billingModel = settleBillingOnAccountServedModel(account, requestedModel, billingModel)
	billingModel = s.billableModelWithFallback(ctx, apiKey, billingModel, result.UpstreamModel, result.Model)

	cost, err := s.calculateRecordUsageCost(ctx, result, apiKey, billingModel, multiplier, imageMultiplier, pricingAt, &recordUsageOpts{})
	if err != nil || cost == nil {
		return nil
	}

	if responseModel := responseModelBillingDeclaration(
		snap.BillingModelSource,
		result.UpstreamResponseModel,
		result.UpstreamResponseModelConflict,
		false,
	); responseModel != "" && !strings.EqualFold(responseModel, strings.TrimSpace(billingModel)) {
		if identified, responseChannelPriced := s.hasIdentifiedResponseModelPricing(ctx, responseModel, apiKey); identified {
			responseCost, responseErr := s.calculateRecordUsageCost(ctx, result, apiKey, responseModel, multiplier, imageMultiplier, pricingAt, &recordUsageOpts{})
			baselineChannelPriced := s.resolveChannelPricing(ctx, billingModel, apiKey) != nil
			if responseErr == nil && responseModelBillingAdoptable(cost, responseCost, baselineChannelPriced, responseChannelPriced) {
				cost = responseCost
			}
		}
	}
	return cost
}

// previewOpenAIClientUsageCost computes ActualCost for OpenAI-shaped token responses.
// It must mirror RecordUsage's customer-facing ActualCost path (service-tier
// settlement, response_model adoption, Free Fast → Standard ActualCost).
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

	actualInputTokens := preview.Usage.InputTokens - preview.Usage.CacheReadInputTokens - preview.Usage.CacheCreationInputTokens
	if actualInputTokens < 0 {
		actualInputTokens = 0
	}
	tokens := UsageTokens{
		InputTokens:           actualInputTokens,
		ImageInputTokens:      preview.Usage.ImageInputTokens,
		OutputTokens:          preview.Usage.OutputTokens,
		CacheCreationTokens:   preview.Usage.CacheCreationInputTokens,
		CacheCreation5mTokens: preview.Usage.CacheCreation5mTokens,
		CacheCreation1hTokens: preview.Usage.CacheCreation1hTokens,
		CacheReadTokens:       preview.Usage.CacheReadInputTokens,
		ImageOutputTokens:     preview.Usage.ImageOutputTokens,
	}

	multiplier := 1.0
	if s.cfg != nil {
		multiplier = s.cfg.Default.RateMultiplier
	}
	if apiKey.GroupID != nil {
		resolver := s.userGroupRateResolver
		if resolver == nil {
			resolver = newUserGroupRateResolver(nil, nil, resolveUserGroupRateCacheTTL(s.cfg), nil, "service.openai_gateway")
		}
		multiplier = resolver.Resolve(ctx, apiKey.User.ID, *apiKey.GroupID, apiKey.Group.RateMultiplier)
	}
	baseMultiplier := multiplier
	multiplier, imageMultiplier := computePeakAwareMultipliers(apiKey, baseMultiplier, pricingAt)
	videoMultiplier := resolveVideoRateMultiplier(apiKey, baseMultiplier)

	billingModel := forwardResultBillingModel(preview.Model, preview.UpstreamModel)
	if preview.BillingModel != "" {
		billingModel = strings.TrimSpace(preview.BillingModel)
	}
	if snap.BillingModelSource == BillingModelSourceChannelMapped && snap.ChannelMappedModel != "" && snap.ChannelMappedModel != snap.OriginalModel {
		billingModel = snap.ChannelMappedModel
	}
	if snap.BillingModelSource == BillingModelSourceRequested && snap.OriginalModel != "" {
		billingModel = snap.OriginalModel
	}
	requestedForBilling := snap.OriginalModel
	if requestedForBilling == "" {
		requestedForBilling = preview.Model
	}
	billingModel = settleBillingOnAccountServedModel(billingAccount, requestedForBilling, billingModel)
	billingModels := usageBillingModelCandidates(
		billingModel,
		preview.BillingModel,
		snap.ChannelMappedModel,
		snap.OriginalModel,
		preview.UpstreamModel,
		preview.Model,
	)
	billingModels = s.filterCNProviderBillingModelCandidates(ctx, account, apiKey, billingModels)
	serviceTier := ""
	if preview.ServiceTier != nil {
		serviceTier = strings.TrimSpace(*preview.ServiceTier)
	}
	longContextBillingGate := openAILongContextBillingGate(billingAccount)
	cost, err := s.calculateOpenAIRecordUsageCost(
		ctx, &preview, apiKey, billingModels, multiplier, imageMultiplier, videoMultiplier, baseMultiplier,
		tokens, serviceTier, longContextBillingGate, pricingAt,
	)
	if err != nil || cost == nil {
		return nil
	}

	baselineBillingModel := firstUsageBillingModel(billingModels)
	if responseModel := responseModelBillingDeclaration(
		snap.BillingModelSource,
		preview.UpstreamResponseModel,
		preview.UpstreamResponseModelConflict,
		false,
	); responseModel != "" && !strings.EqualFold(responseModel, baselineBillingModel) {
		if identified, responseChannelPriced := s.hasIdentifiedOpenAIResponsePricing(ctx, responseModel, apiKey); identified {
			responseModels := s.filterCNProviderBillingModelCandidates(ctx, account, apiKey, usageBillingModelCandidates(responseModel))
			responseCost, responseErr := s.calculateOpenAIRecordUsageCost(
				ctx, &preview, apiKey, responseModels, multiplier, imageMultiplier,
				videoMultiplier, baseMultiplier, tokens, serviceTier, longContextBillingGate, pricingAt,
			)
			baselineChannelPriced := s.resolveOpenAIChannelPricing(ctx, baselineBillingModel, apiKey) != nil
			if responseErr == nil && responseModelBillingAdoptable(cost, responseCost, baselineChannelPriced, responseChannelPriced) {
				billingModels = responseModels
				cost = responseCost
			}
		}
	}

	if groupBillsOpenAIFastAtStandard(apiKey, billingAccount, serviceTier) {
		standardCost, standardErr := s.calculateOpenAIRecordUsageCost(
			ctx, &preview, apiKey, billingModels, multiplier, imageMultiplier, videoMultiplier, baseMultiplier,
			tokens, "", longContextBillingGate, pricingAt,
		)
		if standardErr == nil && cost != nil && standardCost != nil {
			cost.ActualCost = standardCost.ActualCost
		}
	}
	return cost
}
