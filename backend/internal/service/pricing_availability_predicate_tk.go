package service

// TokenKey: read-side predicate used by the upstream-discovery projection.
// Keeps the predicate in a TK companion file so the core RecordOutcome /
// classifier code in pricing_availability_service_tk.go stays focused on the
// write path.

import (
	"context"
	"strings"
)

// IsStructurallyGone reports whether the (platform, modelID) evidence says the
// model no longer exists upstream. A transient 5xx/network failure may also
// derive status=unreachable, but it remains evidence and must not hide the model.
//
// Behavior:
//   - nil receiver / nil repo → false (feature-flag-off; never filter when
//     the service was not wired). This matches the design's nil-safe stance.
//   - empty platform OR empty modelID → false (defensive; callers may pass
//     unset values during early init).
//   - repo error (e.g. PG transient) → false (fail-open; an SDK seeing a
//     model that turns out unreachable is recoverable, but a blank model
//     list is not).
func (s *PricingAvailabilityService) IsStructurallyGone(ctx context.Context, platform, modelID string) bool {
	if s == nil || s.repo == nil {
		return false
	}
	platform = strings.TrimSpace(platform)
	modelID = strings.TrimSpace(modelID)
	if platform == "" || modelID == "" {
		return false
	}
	state, err := s.repo.Get(ctx, platform, modelID)
	if err != nil {
		return false
	}
	return tkAvailabilityStructurallyGone(state)
}
