package service

// TokenKey: post-construction setters + per-service one-liner wrappers for the
// runtime priced-serving gate (docs/approved/priced-or-it-doesnt-ship.md).
//
// model mapping happens in 4+ service-layer call sites (no single gateway
// middleware choke point), and the gate's billing oracle (BillingService) is
// not held by every upstream-shaped struct. Rather than touch each upstream
// constructor signature, we attach the catalog + billing resolver (and, for the
// gemini compat service, also settingService + notifier it lacks) via Set*
// setters during Wire DI — mirrors SetPricingAvailabilityService /
// SetPricingMissingNotifier. Each service then exposes a tkPricedServingGate(...)
// one-liner so the upstream-file injection stays a single guarded call + early
// return.
//
// Judgment source (evaluation root-cause refactor): the gate calls the SAME
// BillingService.GetModelPricing oracle billing uses to decide whether to record
// $0 — NOT a catalog-membership shadow predicate. So the catalog is wired only
// for legacy uses; the pass/reject decision goes through tkBillingPricingResolver
// (a thin func over billingService.GetModelPricing). This makes gate ⟺ billing
// constructive (it inherits getFallbackPricing family coverage + every priced
// field) and judges the exact key billing will charge per route.
//
// All setters/wrappers are nil-safe; an un-wired service simply lets every
// request through (the gate is an additive subtraction — it must never reject
// traffic because of a wiring gap).

import (
	"context"

	"github.com/gin-gonic/gin"
)

// tkBillingResolverFromService adapts a *BillingService into the gate's
// tkBillingPricingResolver (func(model) (*ModelPricing, error)). Returns nil for
// a nil billing service so the gate's nil-resolver fail-open kicks in.
func tkBillingResolverFromService(b *BillingService) tkBillingPricingResolver {
	if b == nil {
		return nil
	}
	return b.GetModelPricing
}

// ---------------------------------------------------------------------------
// GatewayService (native anthropic /v1/messages, responses-bridge,
// chat-completions-bridge all hang off this struct).
// ---------------------------------------------------------------------------

// SetPricingCatalogService wires the catalog membership predicate onto
// GatewayService. Called during Wire DI; absent call = gate disabled.
// The catalog is retained for legacy/list uses; the gate's pass/reject judgment
// goes through the billing oracle (s.billingService.GetModelPricing).
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
//
// wireProtocol is the CLIENT-facing protocol of the call site (not
// account.Platform): native /v1/messages = anthropic; the responses /
// chat-completions bridges = openai. billingModel must be the exact key billing
// will charge (native anthropic = originalModel).
func (s *GatewayService) tkPricedServingGate(ctx context.Context, c *gin.Context, wireProtocol tkGateWireProtocol, platform, billingModel, requestedModel string) bool {
	if s == nil {
		return true
	}
	return tkCheckPricedServingGate(ctx, tkBillingResolverFromService(s.billingService), s.settingService, s.tkPricingMissingNotifier, c, wireProtocol, platform, billingModel, requestedModel)
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
// Contract identical to GatewayService.tkPricedServingGate. Native openai bills
// on the mapped billingModel, which is the same key passed here.
func (s *OpenAIGatewayService) tkPricedServingGate(ctx context.Context, c *gin.Context, wireProtocol tkGateWireProtocol, platform, billingModel, requestedModel string) bool {
	if s == nil {
		return true
	}
	return tkCheckPricedServingGate(ctx, tkBillingResolverFromService(s.billingService), s.settingService, s.tkPricingMissingNotifier, c, wireProtocol, platform, billingModel, requestedModel)
}

// ---------------------------------------------------------------------------
// GeminiMessagesCompatService (native gemini Forward + ForwardNative +
// ForwardAsChatCompletions). Holds NONE of the deps on the upstream
// constructor, so the setter injects all of them at once.
// ---------------------------------------------------------------------------

// SetPricedServingGateDeps wires the catalog predicate, billing oracle,
// settingService, and pricing-missing notifier onto the gemini compat service in
// one call (it holds none of them natively). Called during Wire DI; absent call
// = gate disabled.
func (s *GeminiMessagesCompatService) SetPricedServingGateDeps(catalog *PricingCatalogService, billing *BillingService, setting *SettingService, notifier PricingMissingNotifier) {
	if s != nil {
		s.tkPricingCatalog = catalog
		s.tkBillingService = billing
		s.tkSettingService = setting
		s.tkPricingMissingNotifier = notifier
	}
}

// HasPricedServingGateDeps proves the setter ran (wire_assertion smoke test).
// Requires billing AND setting (either nil means the gate can never fire / would
// fail-open silently).
func (s *GeminiMessagesCompatService) HasPricedServingGateDeps() bool {
	return s != nil && s.tkBillingService != nil && s.tkSettingService != nil
}

// tkPricedServingGate runs the gate for a gemini compat call site. Contract
// identical to GatewayService.tkPricedServingGate. The gemini compat service
// serves THREE client protocols off one account platform, so the caller passes
// the right wireProtocol: Forward(Anthropic /v1/messages)=anthropic,
// ForwardNative=gemini, ForwardAsChatCompletions=openai. billingModel is the key
// billing will charge (gemini native = originalModel on all three paths).
func (s *GeminiMessagesCompatService) tkPricedServingGate(ctx context.Context, c *gin.Context, wireProtocol tkGateWireProtocol, platform, billingModel, requestedModel string) bool {
	if s == nil {
		return true
	}
	return tkCheckPricedServingGate(ctx, tkBillingResolverFromService(s.tkBillingService), s.tkSettingService, s.tkPricingMissingNotifier, c, wireProtocol, platform, billingModel, requestedModel)
}
