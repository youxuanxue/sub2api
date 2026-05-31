package service

import (
	"context"
	"log/slog"
	"sync/atomic"
)

// SettingPubSub is the narrow cross-replica fan-out dependency for SystemSettings
// writes. Implemented in the repository layer (Redis pub/sub on the
// "settings_updated" channel) so the service layer stays redis-free
// (depguard service-no-repository), mirroring the TierCache / *_cache.go pattern.
// All call sites are nil-safe: a nil bus disables fan-out and the existing 60s
// per-cache TTL remains the fallback.
type SettingPubSub interface {
	// Publish signals peer replicas to reload settings.
	Publish(ctx context.Context) error
	// Subscribe invokes handler on every refresh signal until ctx is cancelled.
	Subscribe(ctx context.Context, handler func())
}

// settingsPubSubHub wires a SettingPubSub bus into SettingService so a
// SystemSettings write (e.g. the Claude Code / Antigravity HTTP User-Agent
// version) invalidates every other replica's in-process atomic caches within
// seconds instead of waiting out the per-cache 60s TTL.
//
// TokenKey-only (CLAUDE.md §5): kept in a companion so setting_service.go stays
// close to upstream shape — the only upstream-file deltas are one struct field
// (settingsPubSub) and one notifySettingsPubSub() line at the tail of
// refreshCachedSettings.
type settingsPubSubHub struct {
	bus SettingPubSub
	// suppress is set while applying a remote refresh so the refreshCachedSettings
	// call that repopulates caches does not re-publish — breaking the
	// publish → subscribe → refresh → publish loop.
	suppress atomic.Bool
}

// EnableSettingsPubSub wires a cross-replica fan-out bus and starts the
// subscriber. Safe with a nil bus or service (no-op).
func (s *SettingService) EnableSettingsPubSub(ctx context.Context, bus SettingPubSub) {
	if s == nil || bus == nil {
		return
	}
	hub := &settingsPubSubHub{bus: bus}
	s.settingsPubSub = hub
	bus.Subscribe(ctx, func() { s.applyRemoteSettingsRefresh(ctx) })
}

// notifySettingsPubSub publishes a refresh so peer replicas reload settings.
// Invoked at the tail of refreshCachedSettings. No-op when pub/sub is disabled
// or while this replica is applying a remote refresh (loop guard).
func (s *SettingService) notifySettingsPubSub() {
	hub := s.settingsPubSub
	if hub == nil || hub.bus == nil || hub.suppress.Load() {
		return
	}
	if err := hub.bus.Publish(context.Background()); err != nil {
		slog.Warn("settings_pubsub_publish_failed", "error", err)
	}
}

// applyRemoteSettingsRefresh reloads SystemSettings from the DB and repopulates
// the in-process atomic caches WITHOUT re-publishing (suppress guards the loop).
func (s *SettingService) applyRemoteSettingsRefresh(ctx context.Context) {
	hub := s.settingsPubSub
	if hub != nil {
		hub.suppress.Store(true)
		defer hub.suppress.Store(false)
	}
	settings, err := s.GetAllSettings(ctx)
	if err != nil {
		slog.Warn("settings_pubsub_reload_failed", "error", err)
		return
	}
	s.refreshCachedSettings(settings)
}
