package main

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// TestProvideTKGatewayPricingAvailability_WiresGatewayService ensures the
// post-construction setter actually attaches the availability service to
// GatewayService. The sentinel-style provider would silently no-op if its
// implementation regressed; this test pins the behavior.
//
// R-001 of docs/approved/pricing-availability-source-of-truth.md mandates
// production DI evidence.
func TestProvideTKGatewayPricingAvailability_WiresGatewayService(t *testing.T) {
	// Build a minimal GatewayService — only the availability slot is asserted.
	gw := &service.GatewayService{}
	require.False(t, gw.HasPricingAvailabilityService(),
		"baseline: bare GatewayService must not have availability wired")

	avail := service.NewPricingAvailabilityService(nil, time.Now)

	ready := service.ProvideTKGatewayPricingAvailability(gw, avail)
	_ = ready // sentinel value carries no runtime data; the side effect is the assertion

	require.True(t, gw.HasPricingAvailabilityService(),
		"after Provide: GatewayService must have availability wired")
}

// TestProvideTKGatewayPricingAvailability_NilGatewayServiceIsNoOp guards the
// nil-safety contract: degraded test wiring (gw == nil) must not panic.
func TestProvideTKGatewayPricingAvailability_NilGatewayServiceIsNoOp(t *testing.T) {
	require.NotPanics(t, func() {
		_ = service.ProvideTKGatewayPricingAvailability(nil, nil)
	})
}

// TestProvideTKPricingCatalogHandler_WiresAvailability proves the handler-side
// post-construction wiring runs. ProvideTKPricingCatalogHandler is the
// constructor used by handler ProviderSet (replacing NewPricingCatalogHandler);
// without this wrapper the catalog endpoint would silently never decorate
// responses with availability data even when the service is wired.
func TestProvideTKPricingCatalogHandler_WiresAvailability(t *testing.T) {
	avail := service.NewPricingAvailabilityService(nil, time.Now)

	// catalog and gate may be nil in this test; only the availability wiring
	// is under inspection. NewPricingCatalogHandler tolerates both being nil
	// (degraded behavior) — see pricing_catalog_handler_tk.go.
	h := handler.ProvideTKPricingCatalogHandler(nil, nil, avail)

	require.NotNil(t, h)
	require.True(t, h.HasAvailabilityService(),
		"after Provide: PricingCatalogHandler must have availability wired")
}

// TestProvideTKPricingCatalogHandler_NilAvailabilityIsAllowed pins the
// degraded path: feature-flag-off (avail == nil) must produce a usable handler
// that simply does not decorate responses.
func TestProvideTKPricingCatalogHandler_NilAvailabilityIsAllowed(t *testing.T) {
	h := handler.ProvideTKPricingCatalogHandler(nil, nil, nil)

	require.NotNil(t, h)
	require.False(t, h.HasAvailabilityService())
}

// TestProvideTKGatewayHandlerModelList_WiresModelListFilter proves the
// sentinel-style post-construction setter wires the filter onto GatewayHandler.
// Without this the client model-list endpoints (/v1/models, /antigravity/models)
// would silently never filter unreachable + unpriced models (Goal 2, R-003).
func TestProvideTKGatewayHandlerModelList_WiresModelListFilter(t *testing.T) {
	gw := &handler.GatewayHandler{}
	require.False(t, gw.HasModelListFilter(), "baseline: no filter before wiring")

	f := service.NewModelListFilter(nil, nil)
	ready := handler.ProvideTKGatewayHandlerModelList(gw, f)
	_ = ready

	require.True(t, gw.HasModelListFilter(), "after wiring: filter must be set")
}

// TestProvideTKGatewayHandlerModelList_NilHandlerIsNoOp verifies nil-safety.
func TestProvideTKGatewayHandlerModelList_NilHandlerIsNoOp(t *testing.T) {
	require.NotPanics(t, func() {
		_ = handler.ProvideTKGatewayHandlerModelList(nil, nil)
	})
}
