package middleware

import (
	"errors"
	"io/fs"
	"os"
	"strings"
)

// StartDrainingMarker is a one-shot file. When it exists at boot the process
// starts with /health -> 503, so the entry load balancer never routes to a
// fresh container before the rolling deploy has smoke-tested it. SIGUSR2
// (ReleaseDrain) clears the flag and deletes the file, so later crash restarts
// or host reboots come up serving instead of staying drained.
// Contract: docs/approved/design-cn-prod-rolling-deploy.md §2.
const defaultStartDrainingMarker = "/app/data/start-draining"

// StartDrainingMarkerEnv overrides the marker path (tests, non-container runs).
const StartDrainingMarkerEnv = "GATEWAY_START_DRAINING_MARKER"

var startDrainingMarker = defaultStartDrainingMarker

func resolveMarker(lookup func(string) string) string {
	if lookup == nil {
		lookup = os.Getenv
	}
	if v := strings.TrimSpace(lookup(StartDrainingMarkerEnv)); v != "" {
		return v
	}
	return defaultStartDrainingMarker
}

// ApplyStartDraining sets the drain flag when the marker exists and reports it.
func ApplyStartDraining(lookup func(string) string) bool {
	startDrainingMarker = resolveMarker(lookup)
	if _, err := os.Stat(startDrainingMarker); err == nil {
		SetDrain(true)
		return true
	}
	return false
}

// ReleaseDrain returns /health to 200 and removes the marker. It reports
// whether the flag changed; a marker that cannot be removed is returned as err
// so the caller can log it (the flag is released either way).
func ReleaseDrain() (bool, error) {
	changed := drainFlag.CompareAndSwap(true, false)
	if err := os.Remove(startDrainingMarker); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return changed, err
	}
	return changed, nil
}

// StartDrainingMarkerPath reports the resolved marker path (for logs).
func StartDrainingMarkerPath() string { return startDrainingMarker }
