package kiro

import "testing"

func TestKiroDataRegion_ProfileArnOverridesOAuthRegion(t *testing.T) {
	account := &Account{AuthMethod: "idc", Region: "eu-west-1", ProfileArn: "arn:aws:codewhisperer:eu-central-1:1:profile/demo"}
	if got := kiroDataRegion(account, ""); got != "eu-central-1" {
		t.Fatalf("kiroDataRegion = %q, want profile region", got)
	}
	got := regionalizeKiroURL("https://runtime.us-east-1.kiro.dev/generateAssistantResponse", account, "")
	want := "https://q.eu-central-1.amazonaws.com/generateAssistantResponse"
	if got != want {
		t.Fatalf("regionalizeKiroURL = %q, want %q", got, want)
	}
}

func TestKiroDataRegion_OAuthRegionDoesNotRewriteDataPlane(t *testing.T) {
	account := &Account{AuthMethod: "idc", Region: "eu-west-1"}
	got := regionalizeKiroURL("https://runtime.us-east-1.kiro.dev/generateAssistantResponse", account, "")
	want := "https://runtime.us-east-1.kiro.dev/generateAssistantResponse"
	if got != want {
		t.Fatalf("regionalizeKiroURL = %q, want %q", got, want)
	}
}

func TestKiroRestTargets_RegionalProfileUsesQSurface(t *testing.T) {
	account := &Account{AuthMethod: "social", ProfileArn: "arn:aws:codewhisperer:eu-central-1:1:profile/demo"}
	targets := kiroRestTargets(account, "/Get-Usage-Limits?origin=AI_EDITOR")
	if len(targets) != 1 || targets[0].base != "https://q.eu-central-1.amazonaws.com" || targets[0].path != "/getUsageLimits?origin=AI_EDITOR" {
		t.Fatalf("regional REST targets = %#v", targets)
	}
}

func TestKiroDataRegion_APIKeyUsesAccountRegion(t *testing.T) {
	account := &Account{AuthMethod: "api_key", Region: "eu-central-1"}
	if got := kiroDataRegion(account, ""); got != "eu-central-1" {
		t.Fatalf("kiroDataRegion = %q, want API-key region", got)
	}
}
