//go:build unit

package kiro

import (
	"strings"
	"testing"
)

func TestStreamingUserAgentShape(t *testing.T) {
	ua := StreamingUserAgent(ClientIdentity{}, "")
	// Defaults applied, aws-sdk-js streaming shape, KiroIDE-<ver> tail, no machineID suffix.
	for _, want := range []string{
		"aws-sdk-js/" + StreamingSDKVersion,
		"api/codewhispererstreaming#" + StreamingSDKVersion,
		"m/E",
		"KiroIDE-" + DefaultKiroIDEVersion,
		"os/" + DefaultSystemVersion,
		"md/nodejs#" + DefaultNodeVersion,
	} {
		if !strings.Contains(ua, want) {
			t.Fatalf("UA missing %q: %s", want, ua)
		}
	}
	if strings.HasSuffix(ua, "-") {
		t.Fatalf("UA must not end with a dangling machineID separator: %s", ua)
	}
}

func TestUserAgentMachineIDSuffix(t *testing.T) {
	ua := StreamingUserAgent(ClientIdentity{KiroIDEVersion: "9.9.9"}, "fp123")
	if !strings.HasSuffix(ua, "KiroIDE-9.9.9-fp123") {
		t.Fatalf("expected KiroIDE-<ver>-<machineID> tail, got %s", ua)
	}
}

func TestResolveClientIdentityEnvOverride(t *testing.T) {
	t.Setenv(UserAgentVersionEnv, "9.9.9-test")
	id := ResolveClientIdentity()
	if id.KiroIDEVersion != "9.9.9-test" {
		t.Fatalf("env override not honored: got %q", id.KiroIDEVersion)
	}
	// Non-overridden fields still fall back to compile-time defaults.
	if id.NodeVersion != DefaultNodeVersion {
		t.Fatalf("NodeVersion default not applied: %q", id.NodeVersion)
	}
}

func TestResolveClientIdentityDefault(t *testing.T) {
	t.Setenv(UserAgentVersionEnv, "")
	id := ResolveClientIdentity()
	if id.KiroIDEVersion != DefaultKiroIDEVersion {
		t.Fatalf("default version not applied: got %q want %q", id.KiroIDEVersion, DefaultKiroIDEVersion)
	}
}

func TestAmzUserAgentShape(t *testing.T) {
	amz := BuildAmzUserAgent(ClientIdentity{}, StreamingSDKVersion, "")
	if amz != "aws-sdk-js/"+StreamingSDKVersion+" KiroIDE-"+DefaultKiroIDEVersion {
		t.Fatalf("unexpected amz UA: %s", amz)
	}
	amz2 := BuildAmzUserAgent(ClientIdentity{}, StreamingSDKVersion, "m9")
	if !strings.HasSuffix(amz2, "-m9") {
		t.Fatalf("expected machineID suffix on amz UA: %s", amz2)
	}
}
