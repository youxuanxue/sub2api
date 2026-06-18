//go:build unit

package engine

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// TrajProjectablePlatforms is the single source for "which platforms the traj v2
// projector can faithfully reconstruct" (feeds the /auth/me export-chip
// allowlist). This pins its membership and that the OpenAI-compat members are
// derived from OpenAICompatPlatforms() rather than re-listed.
func TestTrajProjectablePlatforms(t *testing.T) {
	got := TrajProjectablePlatforms()
	set := map[string]bool{}
	for _, p := range got {
		if set[p] {
			t.Fatalf("duplicate platform %q in TrajProjectablePlatforms", p)
		}
		set[p] = true
	}

	wantPresent := []string{
		domain.PlatformAnthropic,
		domain.PlatformKiro,
		domain.PlatformGemini,
		domain.PlatformAntigravity,
		domain.PlatformOpenAI,
		domain.PlatformNewAPI,
		domain.PlatformGrok,
	}
	for _, p := range wantPresent {
		if !set[p] {
			t.Errorf("TrajProjectablePlatforms missing %q", p)
		}
	}

	// The OpenAI-compat members must flow through from OpenAICompatPlatforms() so
	// a future compat platform is picked up automatically (no second list).
	for _, p := range OpenAICompatPlatforms() {
		if !set[p] {
			t.Errorf("TrajProjectablePlatforms must include OpenAICompatPlatforms() member %q", p)
		}
	}
}
