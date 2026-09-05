//go:build unit

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProvideCleanup_TKStopOrderInSource pins the relative Stop order of TK
// cleanup hooks inside provideCleanup after companion extraction.
func TestProvideCleanup_TKStopOrderInSource(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "wire.go"))
	require.NoError(t, err)
	body := string(src)

	fnStart := strings.Index(body, "func provideCleanup(")
	require.Greater(t, fnStart, 0)
	fn := body[fnStart:]

	type step struct {
		name   string
		needle string
	}
	steps := []step{
		{"SchedulerSnapshot before reaper", `"SchedulerSnapshotService"`},
		{"SchedulerRateLimitReaper", `tkHooks.stopSchedulerRateLimitReaper()`},
		{"AnthropicConfigReconciler", `tkHooks.stopAnthropicConfigReconciler()`},
		{"UpstreamBalanceSentinel after reconciler", `"UpstreamBalanceSentinel"`},
		{"IdempotencyCleanup before hold", `"IdempotencyCleanupService"`},
		{"HoldReconciler", `tkHooks.stopHoldReconciler()`},
		{"BatchImageCleanup after hold", `"BatchImageCleanupService"`},
		{"ChannelMonitor before incident", `"ChannelMonitorRunner"`},
		{"AccountIncidentNotifier", `tkHooks.stopAccountIncidentNotifier()`},
		{"PricingMissingNotifier", `tkHooks.stopPricingMissingNotifier()`},
	}

	prev := -1
	for _, s := range steps {
		idx := strings.Index(fn, s.needle)
		require.Greater(t, idx, 0, "missing %s (%q)", s.name, s.needle)
		require.Greater(t, idx, prev, "order broken at %s", s.name)
		prev = idx
	}
}
