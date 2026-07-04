package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TKAuthServiceColdStartReady is a wire sentinel: holding it proves that
// AuthService has had its trial-key issuer wired. provideCleanup takes this
// type as a parameter purely to force wire to evaluate the side-effect; the
// value itself carries no runtime data.
type TKAuthServiceColdStartReady struct{}

// ProvideTKAuthServiceColdStart wires the trial-key issuer onto AuthService
// after both services are constructed, then returns the sentinel.
//
// This wrapper-style provider mirrors the pattern used by
// ProvideTokenRefreshService / ProvideClaudeTokenProvider in wire.go —
// build the underlying service, attach setter-injected deps, return.
func ProvideTKAuthServiceColdStart(
	auth *AuthService,
	api *APIKeyService,
	settings *SettingService,
) TKAuthServiceColdStartReady {
	if auth != nil {
		auth.SetTrialKeyIssuer(NewTrialKeyIssuer(api, settings))
	}
	return TKAuthServiceColdStartReady{}
}

// TKGatewayPricingAvailabilityReady is a wire sentinel: holding it proves
// GatewayService.SetPricingAvailabilityService has been called with the
// production availability service. provideCleanup (cmd/server/wire.go) takes
// this type as an unused parameter to force wire to evaluate the side-effect.
type TKGatewayPricingAvailabilityReady struct{}

// ProvideTKGatewayPricingAvailability wires the availability service onto
// GatewayService post-construction. Mirrors ProvideTKAuthServiceColdStart in
// shape — keep upstream NewGatewayService signature stable, attach setter-only
// dependencies in TK companion glue.
//
// Setter is nil-safe; if avail is nil (e.g. degraded test wiring) the service
// remains in feature-flag-off state.
func ProvideTKGatewayPricingAvailability(
	gw *GatewayService,
	avail *PricingAvailabilityService,
) TKGatewayPricingAvailabilityReady {
	if gw != nil {
		gw.SetPricingAvailabilityService(avail)
	}
	return TKGatewayPricingAvailabilityReady{}
}

// TKUniversalModelsProviderReady is a wire sentinel: holding it proves
// APIKeyService's universal-key resolver has been wired to GatewayService's
// direct-scheduler model+protocol support predicate, with GetAvailableModels
// retained as a degraded fallback. provideCleanup (cmd/server/wire.go) consumes
// this type as an unused parameter to force wire to evaluate the side-effect.
type TKUniversalModelsProviderReady struct{}

// ProvideTKUniversalModelsProvider wires the universal-key resolver's model
// support truth source post-construction. APIKeyService constructs the resolver
// before GatewayService exists, so this late binding avoids the construction
// cycle. Mirrors ProvideTKGatewayPricingAvailability in shape.
//
// Setter is nil-safe; if either dep is nil the resolver keeps its safe
// platform-level fallback. See docs/approved/universal-key-routing.md.
func ProvideTKUniversalModelsProvider(
	api *APIKeyService,
	gw *GatewayService,
) TKUniversalModelsProviderReady {
	if api != nil && gw != nil {
		api.SetUniversalModelSupportProvider(gw.UniversalGroupSupportsRequest)
		api.SetUniversalAvailableModelsProvider(gw.GetAvailableModels)
	}
	return TKUniversalModelsProviderReady{}
}

// TKPricingOverlayRuntimeReady is a wire sentinel: holding it proves the runtime
// hot-pushable TK pricing overlay has been wired onto PricingService
// (SetOverlayRuntimeDeps + initial reload + pub/sub subscribe). provideCleanup
// (cmd/server/wire.go) consumes this type as an unused parameter to force wire to
// evaluate the side-effect.
type TKPricingOverlayRuntimeReady struct{}

// ProvideTKPricingOverlayRuntime wires the runtime overlay (settings-blob getter
// + public-catalog cache invalidator) onto PricingService post-construction, does
// the initial load so an already-present runtime blob is honored at boot, and
// subscribes to the settings pub/sub so a hot-push reloads immediately across
// replicas. Mirrors ProvideTKGatewayPricingAvailability in shape — keep upstream
// NewPricingService signature stable, attach setter-only deps in TK companion glue.
//
// All setters are nil-safe: with a nil settingService/catalog/pubsub the service
// serves the embedded overlay floor exactly as before.
func ProvideTKPricingOverlayRuntime(
	ps *PricingService,
	settingService *SettingService,
	catalog *PricingCatalogService,
	pubsub SettingPubSub,
) TKPricingOverlayRuntimeReady {
	if ps != nil {
		var invalidator func()
		if catalog != nil {
			invalidator = catalog.InvalidateCache
		}
		var getter func(ctx context.Context) (string, bool)
		if settingService != nil {
			getter = func(ctx context.Context) (string, bool) {
				return settingService.GetRawSettingValue(ctx, SettingKeyTKPricingOverlayRuntime)
			}
		}
		ps.SetOverlayRuntimeDeps(getter, invalidator)
		if _, err := ps.reloadTKOverlayRuntime(context.Background()); err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] runtime overlay initial load failed: %v", err)
		}
		ps.SubscribeOverlayRuntime(context.Background(), pubsub)
	}
	return TKPricingOverlayRuntimeReady{}
}

// TKGatewayAnthropicSigPreemptReady is a wire sentinel: holding it proves that
// GatewayService.SetAnthropicSigPreemptCache has been called. provideCleanup
// (cmd/server/wire.go) consumes this type as an unused parameter so wire forces
// evaluation of the side-effect.
type TKGatewayAnthropicSigPreemptReady struct{}

// ProvideTKGatewayAnthropicSigPreempt wires the Anthropic signature_error
// preempt cache onto GatewayService post-construction. Mirrors
// ProvideTKGatewayPricingAvailability in shape — keep upstream
// NewGatewayService signature stable, attach setter-only dependencies in TK
// companion glue.
//
// Setter is nil-safe; if cache is nil (e.g. degraded test wiring) the gateway
// remains in feature-disabled state and applySigPreemptIfArmed / armSigPreemptOnError
// become no-ops.
func ProvideTKGatewayAnthropicSigPreempt(
	gw *GatewayService,
	cache AnthropicSignaturePreemptCache,
) TKGatewayAnthropicSigPreemptReady {
	if gw != nil {
		gw.SetAnthropicSigPreemptCache(cache)
	}
	return TKGatewayAnthropicSigPreemptReady{}
}

// TKAnthropicSaturationReady is a wire sentinel proving that the anthropic
// saturation counter has been wired onto BOTH GatewayService (read side, the
// scheduler penalty) and RateLimitService (write side, the skip-penalty
// increments). provideCleanup (cmd/server/wire.go) consumes it as an unused
// parameter so wire forces evaluation of the side-effect.
type TKAnthropicSaturationReady struct{}

// ProvideTKAnthropicSaturation wires the Redis-backed anthropic saturation
// counter into the gateway scheduler (read) and the rate-limit skip-penalty
// path (write). One provider, two setters — both nil-safe; if cache is nil the
// feature is inert (no penalty, no increments). See
// gateway_service_tk_saturation_penalty.go / ratelimit_service_tk_saturation.go.
func ProvideTKAnthropicSaturation(
	gw *GatewayService,
	rl *RateLimitService,
	cache AnthropicSaturationCounterCache,
) TKAnthropicSaturationReady {
	if gw != nil {
		gw.SetAnthropicSaturationCounter(cache)
	}
	if rl != nil {
		rl.SetAnthropicSaturationCounter(cache)
	}
	return TKAnthropicSaturationReady{}
}

// ProvideTKAccountIncidentNotifier builds the account-incident Feishu notifier,
// starts its background digest ticker, and wires it onto RateLimitService
// post-construction. It returns the concrete instance (not a sentinel) so
// provideCleanup can Stop() the ticker at shutdown — mirroring the
// ChannelMonitorRunner lifecycle shape rather than the setter-only sentinel
// pattern.
//
// Node identity (prod / edge-<id>) is derived from server.frontend_url so no new
// env / deploy-template change is needed. Setter is nil-safe; if rl is nil the
// notifier is still returned (and Stopped) without being attached.
func ProvideTKAccountIncidentNotifier(
	rl *RateLimitService,
	ops *OpsService,
	cfg *config.Config,
) *TKAccountIncidentNotifier {
	site := "unknown"
	if cfg != nil {
		site = siteFromFrontendURL(cfg.Server.FrontendURL)
	}
	// Pass a nil interface (not a typed-nil *OpsService) when ops is absent so the
	// notifier's `cfgProvider != nil` guards short-circuit cleanly.
	var provider opsFeishuConfigProvider
	if ops != nil {
		provider = ops
	}
	n := newTKAccountIncidentNotifier(provider, site)
	// 注入可调度账号计数,让 notifier 的池恢复轮询能把空池火警闭环成「池已恢复」绿卡。
	// 必须在 Start() 前设好(虽然轮询每拍重读,设早一拍更稳)。rl 为 nil 时轮询自动 no-op。
	if rl != nil {
		n.SetPoolSchedulableCounter(rl.countSchedulableByPlatform)
	}
	n.Start()
	if rl != nil {
		rl.SetAccountIncidentNotifier(n)
	}
	if isEdgeSiteID(site) {
		SetClaudeAPIStatusNotifier(nil)
	} else {
		SetClaudeAPIStatusNotifier(n)
	}
	return n
}

// ProvideTKPricingMissingNotifier builds the pricing-missing Feishu notifier,
// starts its background digest ticker, and wires it onto both billing funnels
// (GatewayService + OpenAIGatewayService) post-construction. Same lifecycle
// shape as ProvideTKAccountIncidentNotifier: returns the concrete instance so
// provideCleanup can Stop() the ticker at shutdown. Setters are nil-safe.
//
// It ALSO wires the runtime priced-serving gate deps
// (docs/approved/priced-or-it-doesnt-ship.md) in the same pass, since the gate
// reuses this same notifier as its reject-time alert channel and the catalog
// predicate must reach the same three forwarders:
//   - GatewayService / OpenAIGatewayService already hold settingService +
//     notifier + billingService; we add the catalog via SetPricingCatalogService.
//   - GeminiMessagesCompatService holds none of them; SetPricedServingGateDeps
//     injects catalog + billing + setting + notifier at once.
//
// The gate's pass/reject judgment goes through BillingService.GetModelPricing
// (the same oracle billing uses to decide $0), so the billing service must reach
// the gemini compat forwarder too (it holds none of the deps natively).
//
// Piggybacking on this already-evaluated provider (consumed by provideCleanup
// via the *TKPricingMissingNotifier edge) avoids a fresh wire sentinel for the
// gate. All setters are nil-safe; an absent dep simply leaves the gate off.
func ProvideTKPricingMissingNotifier(
	gw *GatewayService,
	openaiGw *OpenAIGatewayService,
	geminiCompat *GeminiMessagesCompatService,
	catalog *PricingCatalogService,
	billing *BillingService,
	setting *SettingService,
	ops *OpsService,
	cfg *config.Config,
) *TKPricingMissingNotifier {
	site := "unknown"
	if cfg != nil {
		site = siteFromFrontendURL(cfg.Server.FrontendURL)
	}
	// Pass a nil interface (not a typed-nil *OpsService) when ops is absent so the
	// notifier's `cfgProvider != nil` guards short-circuit cleanly.
	var provider opsFeishuConfigProvider
	if ops != nil {
		provider = ops
	}
	n := newTKPricingMissingNotifier(provider, site)
	n.Start()
	if gw != nil {
		gw.SetPricingMissingNotifier(n)
		gw.SetPricingCatalogService(catalog)
	}
	if openaiGw != nil {
		openaiGw.SetPricingMissingNotifier(n)
		openaiGw.SetPricingCatalogService(catalog)
	}
	if geminiCompat != nil {
		// gemini compat delegates billing to GatewayService.recordUsage (which uses
		// gw.resolver for channel pricing), so feed the gate the SAME resolver so its
		// channel-price probe matches billing exactly (B1). Same package → private
		// field access, no extra Wire provider / wire_gen regen.
		var resolver *ModelPricingResolver
		if gw != nil {
			resolver = gw.resolver
		}
		geminiCompat.SetPricedServingGateDeps(catalog, billing, setting, n, resolver)
	}
	return n
}

// TKGroupUnsupportedModelCacheReady is a wire sentinel: holding it proves the
// shared group×model unsupported negative cache is wired onto both gateways and
// channel invalidation flush.
type TKGroupUnsupportedModelCacheReady struct{}

// ProvideTKGroupUnsupportedModelCache wires a shared per-replica negative cache
// onto GatewayService and OpenAIGatewayService and registers channel flush.
func ProvideTKGroupUnsupportedModelCache(
	gw *GatewayService,
	openaiGw *OpenAIGatewayService,
	ch *ChannelService,
) TKGroupUnsupportedModelCacheReady {
	cache := newTkGroupUnsupportedModelNegativeCache()
	if gw != nil {
		gw.SetTkGroupUnsupportedModelCache(cache)
	}
	if openaiGw != nil {
		openaiGw.SetTkGroupUnsupportedModelCache(cache)
	}
	registerTkGroupUnsupportedModelCacheFlusher(ch, cache)
	return TKGroupUnsupportedModelCacheReady{}
}
