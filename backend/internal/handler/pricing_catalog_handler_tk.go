package handler

// TokenKey: GET /api/v1/public/pricing handler — public model + pricing catalog.
// Spec: docs/approved/user-cold-start.md §2 v1, US-028.
//
// Behaviors:
//   - When setting `pricing_catalog_public` is false, respond 404 (route exists
//     but is intentionally hidden — does NOT use 200 + empty body, which would
//     leak the route's existence and confuse client-side handling).
//   - When the catalog source has no usable data, respond 200 with
//     {object: "list", data: [], updated_at: ...} (US-028 AC-005 — never 500
//     because of degraded source).
//   - The response shape intentionally does NOT use the {code,message,data}
//     envelope used elsewhere in this codebase; it mirrors the OpenAI
//     `/v1/models` shape so external tools (e.g. All API Hub) can map it with
//     minimal field translation.

import (
	"context"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// PricingCatalogSource is the read-side seam the handler depends on. The
// production wiring uses *service.PricingCatalogService; tests pass a fake.
type PricingCatalogSource interface {
	BuildPublicCatalog(ctx context.Context) *service.PublicCatalogResponse
}

// PricingCatalogGate gates the public route on the admin setting. The
// production wiring uses *service.SettingService; tests pass a fake.
type PricingCatalogGate interface {
	IsPricingCatalogPublic(ctx context.Context) bool
}

// PricingCatalogHandler exposes the public pricing catalog endpoint.
type PricingCatalogHandler struct {
	catalog      PricingCatalogSource
	gate         PricingCatalogGate
	availability *service.PricingAvailabilityService // optional; nil = feature flag off
}

// NewPricingCatalogHandler is the production constructor. Either dependency
// being nil collapses to a sensible degraded behavior (404 / empty list)
// rather than panicking, because this is a public endpoint reachable during
// startup races.
func NewPricingCatalogHandler(catalog *service.PricingCatalogService, gate *service.SettingService) *PricingCatalogHandler {
	return &PricingCatalogHandler{catalog: catalog, gate: gate}
}

// SetAvailabilityService wires the optional pricing-availability observability
// service. When nil (default / Phase 1), GetPublicCatalog returns the base
// catalog without `availability` field. In Phase 2+ wire this to enable badges.
func (h *PricingCatalogHandler) SetAvailabilityService(svc *service.PricingAvailabilityService) {
	if h != nil {
		h.availability = svc
	}
}

// HasAvailabilityService returns true once the availability service is wired.
// Used by wire_assertion_tk_test.go to prove production DI executed the setter.
func (h *PricingCatalogHandler) HasAvailabilityService() bool {
	return h != nil && h.availability != nil
}

// GetPublicCatalog serves GET /api/v1/public/pricing.
func (h *PricingCatalogHandler) GetPublicCatalog(c *gin.Context) {
	ctx := c.Request.Context()

	if h == nil || h.gate == nil || !h.gate.IsPricingCatalogPublic(ctx) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}

	if h.catalog == nil {
		c.JSON(http.StatusOK, service.PublicCatalogResponse{
			Object: "list",
			Data:   []service.PublicCatalogModel{},
		})
		return
	}

	resp := h.catalog.BuildPublicCatalog(ctx)
	if resp == nil {
		c.JSON(http.StatusOK, service.PublicCatalogResponse{
			Object: "list",
			Data:   []service.PublicCatalogModel{},
		})
		return
	}
	// DecorateWithAvailability is nil-safe; when availability == nil (Phase 1
	// with flag off) returns resp unchanged.
	resp = service.DecorateWithAvailability(ctx, resp, h.availability)
	c.JSON(http.StatusOK, resp)
}
