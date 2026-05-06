package service

// TokenKey: read-side predicate used by the upstream-discovery filter (R-002,
// Goal 1) and client model-list filter (R-003, Goal 2). Keeps the predicate
// in a TK companion file so the core RecordOutcome / classifier code in
// pricing_availability_service_tk.go stays focused on the write path.

import (
	"context"
	"strings"
)

// IsUnreachable reports whether the (platform, modelID) cell is currently in
// the 'unreachable' state. Used to drop models from upstream-discovery and
// client model-list responses.
//
// Behavior:
//   - nil receiver / nil repo → false (feature-flag-off; never filter when
//     the service was not wired). This matches the design's nil-safe stance.
//   - empty platform OR empty modelID → false (defensive; callers may pass
//     unset values during early init).
//   - repo error (e.g. PG transient) → false (fail-open; an SDK seeing a
//     model that turns out unreachable is recoverable, but a blank model
//     list is not).
func (s *PricingAvailabilityService) IsUnreachable(ctx context.Context, platform, modelID string) bool {
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
	return state.Status == AvailabilityStatusUnreachable
}
