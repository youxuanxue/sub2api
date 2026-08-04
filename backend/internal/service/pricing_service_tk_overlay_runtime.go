package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TK complete pricing registry runtime hot-push wiring.
//
// The embedded tk_pricing_overlay.json (pricing_service_tk_overlay.go) is the
// embedded last-known-good fallback. A valid protected-main envelope replaces
// that artifact exactly, so a price can change without an application release.
//
// Two triggers keep the running process current after a hot push:
//   - Pub/sub (immediate): the settings_updated channel — see SubscribeOverlayRuntime.
//   - Poll (fallback): syncWithRemote's tick re-reads the blob, hash-gated.
//
// Both funnel through reloadTKOverlayRuntime, which validates before swapping and
// never blanks the effective map (a corrupt blob keeps the last-known-good snapshot).

// SetOverlayRuntimeDeps wires the post-construction dependencies for the hot
// registry: a getter for the runtime settings envelope and a callback to bust the
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
	s.overlayReloadMu.Lock()
	defer s.overlayReloadMu.Unlock()
	s.overlayMu.Lock()
	s.overlayRuntimeGetter = getter
	s.overlayCacheInvalidator = cacheInvalidator
	s.overlayMu.Unlock()
}

// SubscribeOverlayRuntime starts a cross-replica listener so a settings hot-push
// reloads the registry immediately (within seconds) instead of waiting out the
// poll tick. Uses the same "settings_updated" bus SettingService publishes on, so
// the protected publisher's settings write fans out to all replicas.
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

// reloadTKOverlayRuntime re-reads the runtime registry envelope and, if it
// changed, validates and atomically replaces the complete snapshot. Hash-gated
// across poll ticks + pub/sub fan-out). Returns whether anything changed.
//
// Safety: a corrupt runtime blob is rejected BEFORE the swap (the prior good
// effective map is kept) and returns an error; the effective map is never blanked,
// so billing never falls back to $0. An empty/absent blob selects the embedded
// registry only at startup. Once a runtime snapshot has loaded, a transient miss
// keeps that last-known-good snapshot instead of silently reverting prices.
func (s *PricingService) reloadTKOverlayRuntime(ctx context.Context) (bool, error) {
	if s == nil {
		return false, nil
	}
	s.overlayReloadMu.Lock()
	defer s.overlayReloadMu.Unlock()

	// Establish the embedded floor before consulting runtime state. A corrupt
	// setting on the process's first reload must not leave the global snapshot nil.
	loadTKPricingOverlaySnapshot()
	s.overlayMu.Lock()
	getter := s.overlayRuntimeGetter
	prevHash := s.overlayRuntimeHash
	s.overlayMu.Unlock()

	var blob string
	present := false
	if getter != nil {
		if v, ok := getter(ctx); ok {
			blob = v
			present = v != ""
		}
	}
	if !present && prevHash != "" {
		return false, fmt.Errorf("runtime pricing registry missing after a snapshot was applied; keeping last-known-good")
	}

	newHash := ""
	if present {
		sum := sha256.Sum256([]byte(blob))
		newHash = hex.EncodeToString(sum[:])
	}
	if newHash == prevHash {
		return false, nil // unchanged (covers both "empty stays empty" and "same blob")
	}

	registryBytes := []byte(blob)
	if !present {
		registryBytes = nil
	}
	candidate, err := buildTKPricingOverlaySnapshot(registryBytes)
	if err != nil {
		// Keep prevHash so a corrected envelope always retriggers validation.
		return false, err
	}
	tkOverlayMu.Lock()
	tkOverlayEffective = candidate
	tkOverlayMu.Unlock()
	s.mu.Lock()
	s.pricingData = candidate.Models
	s.lastUpdated = time.Now()
	s.mu.Unlock()

	// Bust the public-catalog mtime cache (it keys on model_pricing.json mtime and
	// would otherwise serve stale prices after a registry-only change).
	s.overlayMu.Lock()
	invalidator := s.overlayCacheInvalidator
	s.overlayRuntimeHash = newHash
	s.overlayMu.Unlock()
	if invalidator != nil {
		invalidator()
	}

	slog.Info("tk pricing registry runtime reloaded",
		"models", len(candidate.Models),
		"source_commit", candidate.Metadata.SourceCommit,
		"registry_sha256", candidate.Metadata.RegistrySHA256,
		"embedded", !present)
	return true, nil
}
