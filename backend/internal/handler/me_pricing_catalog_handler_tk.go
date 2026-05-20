package handler

// TokenKey: GET /api/v1/me/pricing-catalog — per-user pricing menu.
//
// JWT-only (lives inside the `authenticated` group registered by
// RegisterUserRoutes via registerTKUserRoutes). The publicly-gated
// pricing_catalog_public admin flag does NOT apply here: this surface
// is identity-scoped and exposes only data the user already has
// implicit access to via their own keys/groups.
//
// docs/approved/user-cold-start.md §2 follow-up (per-key catalog view).

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// MePricingCatalogSource is the read-side seam the handler depends on.
// Production wiring uses *service.MePricingCatalogService; tests inject
// a fake without standing up the full DI graph.
type MePricingCatalogSource interface {
	BuildForUser(
		ctx context.Context,
		userID int64,
		opts service.MePricingCatalogOptions,
	) (*service.MePricingCatalogResponse, error)
}

// MePricingCatalogHandler exposes the per-user catalog endpoint.
type MePricingCatalogHandler struct {
	svc MePricingCatalogSource
}

// NewMePricingCatalogHandler is the production constructor. A nil svc
// degrades to 500 rather than panicking — matches the rest of the
// pricing handlers' defensive shape.
func NewMePricingCatalogHandler(svc *service.MePricingCatalogService) *MePricingCatalogHandler {
	var s MePricingCatalogSource
	if svc != nil {
		s = svc
	}
	return &MePricingCatalogHandler{svc: s}
}

// Get serves GET /api/v1/me/pricing-catalog.
//
// Query params (both optional, mutually exclusive when referring to
// different groups):
//   - api_key_id — show menu for the group of this key
//   - group_id   — show menu for this group ("explore other group" mode)
//
// When both are absent, default to the user's first active key's group;
// when the user has no key, default to their first accessible group.
func (h *MePricingCatalogHandler) Get(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	opts, ok := parseMePricingOpts(c)
	if !ok {
		return
	}

	if h == nil || h.svc == nil {
		response.InternalError(c, "pricing service unavailable")
		return
	}

	resp, err := h.svc.BuildForUser(c.Request.Context(), subject.UserID, opts)
	if err != nil {
		writeMePricingError(c, err)
		return
	}
	response.Success(c, resp)
}

// parseMePricingOpts reads and validates the query parameters. On bad
// input it writes the HTTP error and returns ok=false.
func parseMePricingOpts(c *gin.Context) (service.MePricingCatalogOptions, bool) {
	var opts service.MePricingCatalogOptions

	if raw := c.Query("api_key_id"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			response.BadRequest(c, "invalid api_key_id")
			return opts, false
		}
		opts.APIKeyID = &v
	}
	if raw := c.Query("group_id"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			response.BadRequest(c, "invalid group_id")
			return opts, false
		}
		opts.GroupID = &v
	}
	return opts, true
}

// writeMePricingError maps service-level errors to HTTP codes.
func writeMePricingError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrMePricingNoAccessibleGroups):
		// 200 + empty payload so the UI can render the "create a key"
		// banner without the API client treating it like an error.
		c.JSON(http.StatusOK, gin.H{
			"code":    0,
			"message": "no accessible groups",
			"data": service.MePricingCatalogResponse{
				Models:           []service.MePricingModel{},
				MyKeys:           []service.MePricingKeyRef{},
				AccessibleGroups: []service.MePricingGroupRef{},
			},
		})
	case errors.Is(err, service.ErrMePricingAPIKeyNotFound):
		response.NotFound(c, "api key not found")
	case errors.Is(err, service.ErrMePricingGroupForbidden):
		response.Forbidden(c, "group not accessible")
	case errors.Is(err, service.ErrMePricingConflictingTargets):
		response.BadRequest(c, "api_key_id and group_id refer to different groups")
	default:
		response.InternalError(c, err.Error())
	}
}
