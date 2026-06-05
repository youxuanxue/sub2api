package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/baseline"
)

// EnsureClaudeCodeMimicryBaseline self-heals the two deployment-level Claude Code
// HTTP mimicry runtime knobs — settings.claude_code_user_agent_version and
// settings.claude_code_http_mimicry_manifest — toward the embedded
// anthropic-http-mimicry-baselines.json. It is the in-process equivalent of the
// ops pipeline's `sync-runtime`, so a freshly-deployed node acquires the canonical
// UA + betas WITHOUT an operator round-trip (the check's http_ua_drift gate).
//
// Healing is a MONOTONIC RATCHET, not a directionless overwrite: each key heals
// only when its current value is unset/invalid or carries a cc_version OLDER
// than the embedded baseline. A NEWER value is left untouched — it means the ops
// sync-runtime hot-updated the fleet ahead of this binary's release train, and
// clobbering it would roll the live UA back on every tick until the next deploy
// (observed in production on the 2.1.163→2.1.165 bump: 8/9 nodes reverted within
// hours). Deliberate downgrades go through deleting the settings keys (unset →
// reseeded from baseline), not through writing an older value.
//
// Comparison is SEMANTIC (parse + field compare), not byte-exact, so it never
// fights the Python sync-runtime when both write equivalent JSON. Returns whether
// it wrote anything. Caches are invalidated on write so the change takes effect
// immediately (the 60s TTL would otherwise lag).
func (s *SettingService) EnsureClaudeCodeMimicryBaseline(ctx context.Context) (bool, error) {
	if s == nil || s.settingRepo == nil {
		return false, nil
	}
	doc, err := baseline.LoadHTTPMimicryBaseline()
	if err != nil {
		return false, fmt.Errorf("load http mimicry baseline: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	wantUA := NormalizeClaudeCodeUserAgentVersion(doc.CCVersion)
	if wantUA == "" {
		return false, fmt.Errorf("http mimicry baseline cc_version %q is not a valid UA version", doc.CCVersion)
	}
	// Build the desired manifest and run it THROUGH the parser so the stored value
	// is byte-stable across ticks (ParseClaudeCodeHTTPMimicryManifest dedups /
	// normalizes beta tokens — comparing raw-vs-normalized would loop forever).
	wantBytes, err := json.Marshal(&ClaudeCodeHTTPMimicryManifest{
		SchemaVersion: doc.SchemaVersion,
		CCVersion:     wantUA,
		SonnetOpus:    doc.SonnetOpus,
		Haiku:         doc.Haiku,
	})
	if err != nil {
		return false, fmt.Errorf("marshal baseline mimicry manifest: %w", err)
	}
	wantManifest := ParseClaudeCodeHTTPMimicryManifest(string(wantBytes))
	if wantManifest == nil {
		return false, fmt.Errorf("embedded http mimicry baseline is not a valid manifest")
	}

	changed := false

	// --- UA version (monotonic: heal only unset/invalid/older) ---
	curUARaw, err := s.settingRepo.GetValue(ctx, SettingKeyClaudeCodeUserAgentVersion)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return false, fmt.Errorf("read %s: %w", SettingKeyClaudeCodeUserAgentVersion, err)
	}
	curUA := NormalizeClaudeCodeUserAgentVersion(curUARaw)
	effectiveUA := wantUA // what the DB carries after this tick (for cache refresh)
	switch {
	case curUA == "" || CompareVersions(curUA, wantUA) < 0:
		if err := s.settingRepo.Set(ctx, SettingKeyClaudeCodeUserAgentVersion, wantUA); err != nil {
			return changed, fmt.Errorf("write %s: %w", SettingKeyClaudeCodeUserAgentVersion, err)
		}
		changed = true
	case CompareVersions(curUA, wantUA) > 0:
		effectiveUA = curUA
		// Newer than this binary's baseline: hot-updated via sync-runtime ahead
		// of the release train. Leave it — see the ratchet contract above.
		slog.Debug("claude code mimicry UA setting newer than embedded baseline; preserving",
			"current", curUA, "baseline", wantUA)
	}

	// --- mimicry manifest (monotonic on its own cc_version; semantic compare at par) ---
	curManifestRaw, err := s.settingRepo.GetValue(ctx, SettingKeyClaudeCodeHTTPMimicryManifest)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return changed, fmt.Errorf("read %s: %w", SettingKeyClaudeCodeHTTPMimicryManifest, err)
	}
	curManifest := ParseClaudeCodeHTTPMimicryManifest(curManifestRaw)
	manifestNewer := curManifest != nil &&
		CompareVersions(NormalizeClaudeCodeUserAgentVersion(curManifest.CCVersion), wantManifest.CCVersion) > 0
	if !manifestNewer && !mimicryManifestEquivalent(curManifest, wantManifest) {
		encoded, err := json.Marshal(wantManifest)
		if err != nil {
			return changed, fmt.Errorf("marshal desired mimicry manifest: %w", err)
		}
		if err := s.settingRepo.Set(ctx, SettingKeyClaudeCodeHTTPMimicryManifest, string(encoded)); err != nil {
			return changed, fmt.Errorf("write %s: %w", SettingKeyClaudeCodeHTTPMimicryManifest, err)
		}
		changed = true
	}

	if changed {
		s.invalidateClaudeCodeMimicryCaches(effectiveUA)
		slog.Info("claude code mimicry runtime self-healed to baseline (local deployment only)",
			"cc_version", effectiveUA)
	}
	return changed, nil
}

// invalidateClaudeCodeMimicryCaches drops the in-process UA + manifest caches so
// the next read reflects the just-written values immediately (mirrors the
// UpdateSettings writeback path).
func (s *SettingService) invalidateClaudeCodeMimicryCaches(wantUA string) {
	s.claudeCodeUAVersionSF.Forget("claude_code_user_agent_version")
	s.claudeCodeUAVersionCache.Store(&cachedClaudeCodeUserAgentVersion{
		version:   wantUA,
		expiresAt: time.Now().Add(claudeCodeUserAgentVersionCacheTTL).UnixNano(),
	})
	s.claudeCodeMimicryManifestSF.Forget("claude_code_http_mimicry_manifest")
	// Clear the manifest cache so the next getter re-parses from DB (avoids
	// re-deriving the parsed struct here).
	s.claudeCodeMimicryManifestCache.Store((*cachedClaudeCodeHTTPMimicryManifest)(nil))
}

// mimicryManifestEquivalent reports whether two parsed manifests carry the same
// canonical wire values (cc_version + the two beta lists). nil current → not equal.
func mimicryManifestEquivalent(cur, want *ClaudeCodeHTTPMimicryManifest) bool {
	if cur == nil || want == nil {
		return false
	}
	return cur.CCVersion == want.CCVersion &&
		reflect.DeepEqual(cur.SonnetOpus, want.SonnetOpus) &&
		reflect.DeepEqual(cur.Haiku, want.Haiku)
}
