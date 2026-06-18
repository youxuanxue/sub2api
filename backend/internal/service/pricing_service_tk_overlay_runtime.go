package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TK pricing overlay — runtime hot-push wiring.
//
// The embedded tk_pricing_overlay.json (pricing_service_tk_overlay.go) is the
// compile floor. This file makes the overlay HOT: the live effective map =
// embedded ∪ a runtime blob stored in settings (SettingKeyTKPricingOverlayRuntime),
// so a model can be priced + surfaced in /pricing WITHOUT a release. Mirrors the
// claude_code_http_mimicry_manifest pattern (setting_service_tk_mimicry_selfheal.go):
// git stays the single source of truth, ops `sync-runtime` UPSERTs the settings
// blob on prod, and the next routine release folds it into the embed (the floor
// catches up — see ops/pricing/manage-overlay-runtime.py and its `check`).
//
// Two triggers keep the running process current after a hot push:
//   - Pub/sub (immediate): the settings_updated channel — see SubscribeOverlayRuntime.
//   - Poll (fallback): syncWithRemote's tick re-reads the blob, hash-gated.
//
// Both funnel through reloadTKOverlayRuntime, which validate-before-swaps and
// NEVER blanks the effective map (a corrupt blob keeps the embedded floor — no $0).

// SetOverlayRuntimeDeps wires the post-construction dependencies for the hot
// overlay: a getter for the runtime settings blob and a callback to bust the
// public-catalog mtime cache after a swap. Both are nil-safe; with neither set
// the service serves the embedded floor exactly as before. Called from the wire
// sentinel ProvideTKPricingOverlayRuntime.
func (s *PricingService) SetOverlayRuntimeDeps(
	getter func(ctx context.Context) (string, bool),
	cacheInvalidator func(),
) {
	if s == nil {
		return
	}
	s.overlayMu.Lock()
	s.overlayRuntimeGetter = getter
	s.overlayCacheInvalidator = cacheInvalidator
	s.overlayMu.Unlock()
}

// SubscribeOverlayRuntime starts a cross-replica listener so a settings hot-push
// reloads the overlay immediately (within seconds) instead of waiting out the
// poll tick. Uses the same "settings_updated" bus SettingService publishes on, so
// any settings write — including the ops sync-runtime's redis PUBLISH — fans out.
// Nil-safe: a nil bus (single-replica dev / no redis) leaves only the poll path.
func (s *PricingService) SubscribeOverlayRuntime(ctx context.Context, bus SettingPubSub) {
	if s == nil || bus == nil {
		return
	}
	bus.Subscribe(ctx, func() {
		if _, err := s.reloadTKOverlayRuntime(ctx); err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] runtime overlay pubsub reload failed: %v", err)
		}
	})
}

// reloadTKOverlayRuntime re-reads the runtime overlay settings blob and, if it
// changed, rebuilds the effective union (embedded ∪ runtime), rebuilds the merged
// billing price map, and busts the public-catalog cache. Hash-gated (idempotent
// across poll ticks + pub/sub fan-out). Returns whether anything changed.
//
// Safety: a corrupt runtime blob is rejected BEFORE the swap (the prior good
// effective map is kept) and returns an error; the effective map is never blanked,
// so billing never falls back to $0. An empty/absent blob is valid — it means
// "use the embedded floor" (the GC-after-release state).
func (s *PricingService) reloadTKOverlayRuntime(ctx context.Context) (bool, error) {
	if s == nil {
		return false, nil
	}
	s.overlayMu.Lock()
	getter := s.overlayRuntimeGetter
	prevHash := s.overlayRuntimeHash
	s.overlayMu.Unlock()

	var blob string
	if getter != nil {
		if v, ok := getter(ctx); ok {
			blob = v
		}
	}

	newHash := ""
	if blob != "" {
		sum := sha256.Sum256([]byte(blob))
		newHash = hex.EncodeToString(sum[:])
	}
	if newHash == prevHash {
		return false, nil // unchanged (covers both "empty stays empty" and "same blob")
	}

	// Validate before swapping: a corrupt blob must not disturb the live map.
	if blob != "" {
		if _, err := parseTKOverlayBytes([]byte(blob)); err != nil {
			// Keep prevHash so a later corrected blob still triggers a reload.
			return false, err
		}
	}

	if blob == "" {
		rebuildTKOverlayUnion(nil) // operator cleared the key → fall back to embedded floor
	} else {
		rebuildTKOverlayUnion([]byte(blob))
	}

	// Rebuild the merged billing price map so /v1/messages billing sees the new
	// overlay. Reuse loadPricingData (it re-parses the source file → re-applies
	// the now-updated overlay → swaps s.pricingData under s.mu). Best-effort: if
	// the source file is absent the catalog path already reflects the new union
	// and billing will re-merge on the next remote sync.
	if path := s.getPricingFilePath(); path != "" {
		if err := s.loadPricingData(path); err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] overlay reload: rebuild price map skipped: %v", err)
		}
	}

	// Bust the public-catalog mtime cache (it keys on model_pricing.json mtime and
	// would otherwise serve stale prices after an overlay-only change).
	s.overlayMu.Lock()
	invalidator := s.overlayCacheInvalidator
	s.overlayRuntimeHash = newHash
	s.overlayMu.Unlock()
	if invalidator != nil {
		invalidator()
	}

	slog.Info("tk pricing overlay runtime reloaded", "models", len(loadTKPricingOverlay()), "empty_blob", blob == "")
	return true, nil
}
