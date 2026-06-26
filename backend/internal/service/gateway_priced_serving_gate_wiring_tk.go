package service

// TokenKey: post-construction setters + per-service one-liner wrappers for the
// runtime priced-serving gate (docs/approved/priced-or-it-doesnt-ship.md).
//
// model mapping happens in 4+ service-layer call sites (no single gateway
// middleware choke point), and PricingCatalogService is not held by these
// upstream-shaped structs. Rather than touch each upstream constructor
// signature, we attach the catalog (and, for the gemini compat service, also
// settingService + notifier it lacks) via Set* setters during Wire DI — mirrors
// SetPricingAvailabilityService / SetPricingMissingNotifier. Each service then
// exposes a tkPricedServingGate(...) one-liner so the upstream-file injection
// stays a single guarded call + early return.
//
// All setters/wrappers are nil-safe; an un-wired service simply lets every
// request through (the gate is an additive subtraction — it must never reject
// traffic because of a wiring gap).

import (
	"context"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// GatewayService (native anthropic /v1/messages, responses-bridge,
// chat-completions-bridge all hang off this struct).
// ---------------------------------------------------------------------------

// SetPricingCatalogService wires the catalog membership predicate onto
// GatewayService. Called during Wire DI; absent call = gate disabled.
func (s *GatewayService) SetPricingCatalogService(catalog *PricingCatalogService) {
	if s != nil {
		s.tkPricingCatalog = catalog
	}
}

// HasPricingCatalogService proves the post-construction setter actually ran
// (consumed by wire_assertion smoke tests, mirroring HasPricingAvailabilityService).
func (s *GatewayService) HasPricingCatalogService() bool {
	return s != nil && s.tkPricingCatalog != nil
}

// tkPricedServingGate runs the gate for a GatewayService call site. Returns
// true = proceed (forward); false = already rejected (404 written + alert
// fired), caller MUST return without forwarding. Safe with a nil receiver.
func (s *GatewayService) tkPricedServingGate(ctx context.Context, c *gin.Context, platform, billingModel, requestedModel string) bool {
	if s == nil {
		return true
	}
	return tkCheckPricedServingGate(ctx, s.tkPricingCatalog, s.settingService, s.tkPricingMissingNotifier, c, platform, billingModel, requestedModel)
}

// ---------------------------------------------------------------------------
// OpenAIGatewayService (native openai /v1/responses + chat).
// ---------------------------------------------------------------------------

// SetPricingCatalogService wires the catalog membership predicate onto
// OpenAIGatewayService. Called during Wire DI; absent call = gate disabled.
func (s *OpenAIGatewayService) SetPricingCatalogService(catalog *PricingCatalogService) {
	if s != nil {
		s.tkPricingCatalog = catalog
	}
}

// HasPricingCatalogService proves the setter ran (wire_assertion smoke test).
func (s *OpenAIGatewayService) HasPricingCatalogService() bool {
	return s != nil && s.tkPricingCatalog != nil
}

// tkPricedServingGate runs the gate for an OpenAIGatewayService call site.
// Contract identical to GatewayService.tkPricedServingGate.
func (s *OpenAIGatewayService) tkPricedServingGate(ctx context.Context, c *gin.Context, platform, billingModel, requestedModel string) bool {
	if s == nil {
		return true
	}
	return tkCheckPricedServingGate(ctx, s.tkPricingCatalog, s.settingService, s.tkPricingMissingNotifier, c, platform, billingModel, requestedModel)
}

// ---------------------------------------------------------------------------
// GeminiMessagesCompatService (native gemini Forward + ForwardNative).
// Holds NONE of the deps on the upstream constructor, so the setter injects all
// three at once.
// ---------------------------------------------------------------------------

// SetPricedServingGateDeps wires the catalog predicate, settingService, and
// pricing-missing notifier onto the gemini compat service in one call (it holds
// none of them natively). Called during Wire DI; absent call = gate disabled.
func (s *GeminiMessagesCompatService) SetPricedServingGateDeps(catalog *PricingCatalogService, setting *SettingService, notifier PricingMissingNotifier) {
	if s != nil {
		s.tkPricingCatalog = catalog
		s.tkSettingService = setting
		s.tkPricingMissingNotifier = notifier
	}
}

// HasPricedServingGateDeps proves the setter ran (wire_assertion smoke test).
// Requires BOTH catalog and setting (either nil means the gate can never fire).
func (s *GeminiMessagesCompatService) HasPricedServingGateDeps() bool {
	return s != nil && s.tkPricingCatalog != nil && s.tkSettingService != nil
}

// tkPricedServingGate runs the gate for a gemini compat call site. Contract
// identical to GatewayService.tkPricedServingGate.
func (s *GeminiMessagesCompatService) tkPricedServingGate(ctx context.Context, c *gin.Context, platform, billingModel, requestedModel string) bool {
	if s == nil {
		return true
	}
	return tkCheckPricedServingGate(ctx, s.tkPricingCatalog, s.tkSettingService, s.tkPricingMissingNotifier, c, platform, billingModel, requestedModel)
}
