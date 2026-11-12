//go:build unit

package kiro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestKiroCLIIdentityMatchesObservedCapture(t *testing.T) {
	const wantUA = "aws-sdk-rust/1.6.0 ua/2.1 api/codewhispererstreaming/0.1.19636 os/macos lang/rust/1.94.1 md/appVersion-3.2.1 app/AmazonQ-For-CLI"
	const wantAmzUA = "aws-sdk-rust/1.6.0 ua/2.1 api/codewhispererstreaming/0.1.19636 os/macos lang/rust/1.94.1 m/F app/AmazonQ-For-CLI"
	if KiroCLIUserAgent != wantUA {
		t.Fatalf("unexpected Kiro CLI User-Agent:\n got: %s\nwant: %s", KiroCLIUserAgent, wantUA)
	}
	if KiroCLIAmzUserAgent != wantAmzUA {
		t.Fatalf("unexpected Kiro CLI x-amz-user-agent:\n got: %s\nwant: %s", KiroCLIAmzUserAgent, wantAmzUA)
	}

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
		candidate := filepath.Join(dir, "deploy", "aws", "stage0", "tk_canonical_kiro_cli.json")
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
