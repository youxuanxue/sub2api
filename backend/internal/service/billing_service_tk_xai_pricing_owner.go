package service

// TK xAI pricing-owner resolver.
//
// Single source of truth: backend/internal/pkg/xai/models.go
// (IsGrokTextResponsesModelID / ResolveGrokTextResponsesModelID).
//
// This helper maps a known Grok text alias → its canonical routing target →
// the canonical pricing owner. Direct registry rows still take precedence; this
// resolver is only consulted when no direct row exists.
//
// Design invariants:
//   - Composer models have no standalone public pricing; they bill at grok-build-0.1.
//   - grok-3-mini/fast have no public pricing; known aliases → "" (fail closed).
//   - Truly unknown future Grok text IDs are NOT handled here; they fall through
//     to grokUnknownTextFamilyFallback which uses the 4.6 floor.

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// grokCanonicalPricingOwner maps each routing-canonical model ID to its billing
// owner. Canonical models keep their own registry row; Composer is the sole
// cross-owner policy exception because it has no standalone public price card.
// Absent entries signal "known model, no public pricing" and must fail closed.
var grokCanonicalPricingOwner = map[string]string{
	"grok-4.6":                     "grok-4.6",
	"grok-4.5":                     "grok-4.5",
	"grok-4.3":                     "grok-4.3",
	"grok-build-0.1":               "grok-build-0.1",
	"grok-composer-2.5-fast":       "grok-build-0.1", // no standalone price card; Composer bills at the coding model rate
	"grok-4.20-0309-reasoning":     "grok-4.20-0309-reasoning",
	"grok-4.20-0309-non-reasoning": "grok-4.20-0309-non-reasoning",
	"grok-4.20-multi-agent-0309":   "grok-4.20-multi-agent-0309",
	// grok-3-mini, grok-3-mini-fast: deliberately absent = fail closed.
}

// resolveGrokTextPricingOwner returns the canonical pricing-registry owner and
// whether model is a known Grok text alias. A known alias with an empty owner
// must fail closed; the boolean keeps it distinct from a truly unknown future
// Grok ID, which may use the family floor.
//
// The resolver always uses the compile-time DefaultTextModel for resolution so
// pricing is deterministic regardless of runtime settings.
func resolveGrokTextPricingOwner(model string) (owner string, known bool) {
	if !xai.IsGrokTextResponsesModelID(model) {
		return "", false
	}
	canonical := xai.ResolveGrokTextResponsesModelID(model, xai.DefaultTextModel)
	return grokCanonicalPricingOwner[canonical], true
}
