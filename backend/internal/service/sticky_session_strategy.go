// Package service / sticky_session_strategy.go
//
// Resolve the per-request StickyStrategy from the global setting + the
// per-group enum field. Kept separate from the injector so the injector
// itself stays a pure stateless library and resolution can be mocked or
// short-circuited per call site.
package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/group"
)

// StickyStrategyResolver bundles the inputs needed to produce a
// StickyStrategy. The resolver is intentionally minimal:
//   - GlobalEnabledFn is typically (*SettingService).IsStickyRoutingEnabled
//   - GroupModeFn returns the group's stored mode (or empty when unknown);
//     callers that already have an *ent.Group in hand can use
//     ResolveStickyStrategyFromGroup() instead and skip the function.
type StickyStrategyResolver struct {
	GlobalEnabledFn func(ctx context.Context) bool
	GroupModeFn     func(ctx context.Context, groupID int64) StickyMode
}

// Resolve produces a StickyStrategy for the given group. Defensive defaults:
//   - If GlobalEnabledFn is nil, treat global as enabled.
//   - If GroupModeFn returns "", treat as StickyModeAuto (the schema default).
func (r StickyStrategyResolver) Resolve(ctx context.Context, groupID int64) StickyStrategy {
	enabled := true
	if r.GlobalEnabledFn != nil {
		enabled = r.GlobalEnabledFn(ctx)
	}
	mode := StickyModeAuto
	if r.GroupModeFn != nil {
		if m := r.GroupModeFn(ctx, groupID); m != "" {
			mode = m
		}
	}
	return StickyStrategy{GlobalEnabled: enabled, Mode: mode}
}

// ResolveStickyStrategyFromGroup builds a strategy when the caller already
// has the *ent.Group loaded. The global flag is read via globalEnabledFn
// (usually the SettingService). When grp is nil, the group mode defaults
// to "auto" — preferring opt-out semantics over hard failure if the group
// row is missing for any reason.
func ResolveStickyStrategyFromGroup(ctx context.Context, grp *ent.Group, globalEnabledFn func(ctx context.Context) bool) StickyStrategy {
	enabled := true
	if globalEnabledFn != nil {
		enabled = globalEnabledFn(ctx)
	}
	mode := StickyModeAuto
	if grp != nil {
		if m := strings.TrimSpace(string(grp.StickyRoutingMode)); m != "" {
			mode = group.StickyRoutingMode(m)
		}
	}
	return StickyStrategy{GlobalEnabled: enabled, Mode: mode}
}
