//go:build unit

package kiro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestKiroCLIIdentityMatchesObservedCapture(t *testing.T) {
	// Compare with the real-client capture owner instead of duplicating its UA here.
	raw, err := os.ReadFile(canonicalKiroProfilePath(t))
	if err != nil {
		t.Fatal(err)
	}
	var profile struct {
		Observed struct {
			UserAgent     string `json:"user_agent"`
			XAmzUserAgent string `json:"x_amz_user_agent"`
		} `json:"observed"`
	}
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Observed.UserAgent != KiroCLIUserAgent {
		t.Fatalf("canonical profile user_agent drifted from Go owner:\n got: %s\nwant: %s", profile.Observed.UserAgent, KiroCLIUserAgent)
	}
	if profile.Observed.XAmzUserAgent != KiroCLIAmzUserAgent {
		t.Fatalf("canonical profile x_amz_user_agent drifted from Go owner:\n got: %s\nwant: %s", profile.Observed.XAmzUserAgent, KiroCLIAmzUserAgent)
	}
}

func canonicalKiroProfilePath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(dir, "deploy", "stage0", "tk_canonical_kiro_cli.json")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("tk_canonical_kiro_cli.json not found from the test working directory")
		}
		dir = parent
	}
}
