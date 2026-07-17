//go:build unit

package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// NewMediaStore returns nil (offload disabled) unless BOTH driver and bucket are
// set — so an unconfigured deployment (local dev / edge) passes media through as
// inline base64 instead of crashing or silently pointing at an empty bucket.
func TestNewMediaStore_DisabledWhenUnconfigured(t *testing.T) {
	cases := []config.MediaStorageConfig{
		{},                                  // nothing set
		{Driver: "s3"},                      // bucket missing
		{Bucket: "b"},                       // driver missing
		{Driver: "", Bucket: "b", Region: "us-east-1"}, // driver empty
	}
	for i, mc := range cases {
		if got := NewMediaStore(&config.Config{MediaStorage: mc}); got != nil {
			t.Errorf("case %d %+v: expected nil store, got non-nil", i, mc)
		}
	}
}

// With driver+bucket set the store constructs (no network: LoadDefaultConfig +
// client creation are lazy). Empty creds is the prod path (instance role).
func TestNewMediaStore_EnabledConstructsStore(t *testing.T) {
	got := NewMediaStore(&config.Config{MediaStorage: config.MediaStorageConfig{
		Driver: "s3", Bucket: "tk-test-media", Region: "us-east-1",
	}})
	if got == nil {
		t.Fatal("expected a non-nil store when driver+bucket are set")
	}
}
