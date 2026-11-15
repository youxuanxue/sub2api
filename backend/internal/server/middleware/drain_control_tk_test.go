//go:build unit

package middleware

import (
	"os"
	"path/filepath"
	"testing"
)

func markerLookup(path string) func(string) string {
	return func(key string) string {
		if key == StartDrainingMarkerEnv {
			return path
		}
		return ""
	}
}

func TestApplyStartDrainingWithoutMarkerServes(t *testing.T) {
	resetInflightForTest(t)
	path := filepath.Join(t.TempDir(), "start-draining")
	if ApplyStartDraining(markerLookup(path)) || IsDraining() {
		t.Fatal("no marker must start serving")
	}
}

func TestStartDrainingMarkerIsOneShot(t *testing.T) {
	resetInflightForTest(t)
	path := filepath.Join(t.TempDir(), "start-draining")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !ApplyStartDraining(markerLookup(path)) || !IsDraining() {
		t.Fatal("marker must start drained")
	}
	changed, err := ReleaseDrain()
	if err != nil || !changed || IsDraining() {
		t.Fatalf("release: changed=%v err=%v draining=%v", changed, err, IsDraining())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("release must delete the marker so a restart serves")
	}
	if ApplyStartDraining(markerLookup(path)) || IsDraining() {
		t.Fatal("restart after release must serve")
	}
}

func TestReleaseDrainWhenServingIsNoop(t *testing.T) {
	resetInflightForTest(t)
	startDrainingMarker = filepath.Join(t.TempDir(), "absent")
	changed, err := ReleaseDrain()
	if changed || err != nil {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}
