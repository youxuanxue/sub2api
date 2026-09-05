//go:build unit

package main

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProvideTKCleanupHooks_StopNilSafe(t *testing.T) {
	hooks := provideTKCleanupHooks(
		nil, nil, nil, nil, nil,
		service.TKAuthServiceColdStartReady{},
		service.TKGatewayPricingAvailabilityReady{},
		service.TKPricingOverlayRuntimeReady{},
		service.TKAccountModelMappingRuntimeServingReady{},
		service.TKGatewayAnthropicSigPreemptReady{},
		service.TKAnthropicSaturationReady{},
		service.TKOpenAISaturationReady{},
		service.TKAntigravitySaturationReady{},
		handler.TKGatewayHandlerModelListReady{},
		service.TKUniversalModelsProviderReady{},
		service.TKGroupUnsupportedModelCacheReady{},
		service.ProtocolRoutingSSOTReady{},
	)
	require.NotPanics(t, func() {
		hooks.stopSchedulerRateLimitReaper()
		hooks.stopAnthropicConfigReconciler()
		hooks.stopHoldReconciler()
		hooks.stopAccountIncidentNotifier()
		hooks.stopPricingMissingNotifier()
	})
}
