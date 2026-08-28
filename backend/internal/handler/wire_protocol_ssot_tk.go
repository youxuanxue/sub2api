package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ProvideProtocolRoutedOpenAIGatewayHandler keeps protocol-only dependencies
// outside the upstream-shaped provider while still making the Gemini executor
// mandatory in the generated Wire graph.
func ProvideProtocolRoutedOpenAIGatewayHandler(
	gatewayService *service.OpenAIGatewayService,
	geminiCompatService *service.GeminiMessagesCompatService,
	antigravityGatewayService *service.AntigravityGatewayService,
	concurrencyService *service.ConcurrencyService,
	billingCacheService *service.BillingCacheService,
	apiKeyService *service.APIKeyService,
	usageRecordWorkerPool *service.UsageRecordWorkerPool,
	errorPassthroughService *service.ErrorPassthroughService,
	contentModerationService *service.ContentModerationService,
	opsService *service.OpsService,
	grokQuotaService *service.GrokQuotaService,
	cfg *config.Config,
	coordinator *securityaudit.Coordinator,
	videoTaskCache service.VideoTaskCache,
	mediaStore service.MediaStore,
	protocolRoutingReady service.ProtocolRoutingSSOTReady,
) *OpenAIGatewayHandler {
	h := ProvideOpenAIGatewayHandler(
		gatewayService,
		concurrencyService,
		billingCacheService,
		apiKeyService,
		usageRecordWorkerPool,
		errorPassthroughService,
		contentModerationService,
		opsService,
		grokQuotaService,
		cfg,
		coordinator,
		videoTaskCache,
		mediaStore,
		protocolRoutingReady,
	)
	h.SetGeminiProtocolServices(geminiCompatService, antigravityGatewayService)
	return h
}
