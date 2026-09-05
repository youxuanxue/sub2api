package main

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// tkCleanupHooks aggregates TokenKey-only provideCleanup dependencies so
// upstream-shaped cmd/server/wire.go only takes one TK parameter. Holding the
// wire sentinels forces evaluation of post-construction setter providers;
// holding the concrete notifiers/reapers lets shutdown Stop() their tickers.
type tkCleanupHooks struct {
	schedulerRateLimitReaper  *service.SchedulerRateLimitReaper
	anthropicConfigReconciler *service.AnthropicConfigReconciler
	holdReconciler            *service.HoldReconcilerService
	accountIncidentNotifier   *service.TKAccountIncidentNotifier
	pricingMissingNotifier    *service.TKPricingMissingNotifier
}

// provideTKCleanupHooks is the Wire provider that collapses TK cleanup /
// force-eval edges into a single injectable value for provideCleanup.
func provideTKCleanupHooks(
	schedulerRateLimitReaper *service.SchedulerRateLimitReaper,
	anthropicConfigReconciler *service.AnthropicConfigReconciler,
	holdReconciler *service.HoldReconcilerService,
	accountIncidentNotifier *service.TKAccountIncidentNotifier,
	pricingMissingNotifier *service.TKPricingMissingNotifier,
	_ service.TKAuthServiceColdStartReady,
	_ service.TKGatewayPricingAvailabilityReady,
	_ service.TKPricingOverlayRuntimeReady,
	_ service.TKAccountModelMappingRuntimeServingReady,
	_ service.TKGatewayAnthropicSigPreemptReady,
	_ service.TKAnthropicSaturationReady,
	_ service.TKOpenAISaturationReady,
	_ service.TKAntigravitySaturationReady,
	_ handler.TKGatewayHandlerModelListReady,
	_ service.TKUniversalModelsProviderReady,
	_ service.TKGroupUnsupportedModelCacheReady,
	_ service.ProtocolRoutingSSOTReady,
) tkCleanupHooks {
	return tkCleanupHooks{
		schedulerRateLimitReaper:  schedulerRateLimitReaper,
		anthropicConfigReconciler: anthropicConfigReconciler,
		holdReconciler:            holdReconciler,
		accountIncidentNotifier:   accountIncidentNotifier,
		pricingMissingNotifier:    pricingMissingNotifier,
	}
}

func (h tkCleanupHooks) stopSchedulerRateLimitReaper() {
	if h.schedulerRateLimitReaper != nil {
		h.schedulerRateLimitReaper.Stop()
	}
}

func (h tkCleanupHooks) stopAnthropicConfigReconciler() {
	if h.anthropicConfigReconciler != nil {
		h.anthropicConfigReconciler.Stop()
	}
}

func (h tkCleanupHooks) stopHoldReconciler() {
	if h.holdReconciler != nil {
		h.holdReconciler.Stop()
	}
}

func (h tkCleanupHooks) stopAccountIncidentNotifier() {
	if h.accountIncidentNotifier != nil {
		h.accountIncidentNotifier.Stop()
	}
}

func (h tkCleanupHooks) stopPricingMissingNotifier() {
	if h.pricingMissingNotifier != nil {
		h.pricingMissingNotifier.Stop()
	}
}
