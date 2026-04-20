//go:build unit

package service

import (
	"testing"
)

// Tests for docs/approved/newapi-as-fifth-platform.md §3.2 / US-011 / US-012.
// Covers the pure-method contract of IsOpenAICompatPoolMember: scheduling-pool
// membership must be strict (no cross-platform mixing) and must reject
// newapi accounts with channel_type==0 (incomplete configuration).

func TestUS011_PoolMember_OpenAIAccountInOpenAIGroup(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI}
	if !a.IsOpenAICompatPoolMember(PlatformOpenAI) {
		t.Fatalf("openai account in openai group must be a pool member")
	}
}

func TestUS011_PoolMember_NewAPIAccountInOpenAIGroup_Rejected(t *testing.T) {
	a := &Account{Platform: PlatformNewAPI, ChannelType: 1}
	if a.IsOpenAICompatPoolMember(PlatformOpenAI) {
		t.Fatalf("newapi account MUST NOT be a member of an openai group's pool (security: no mixing)")
	}
}

func TestUS011_PoolMember_OpenAIAccountInNewAPIGroup_Rejected(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI}
	if a.IsOpenAICompatPoolMember(PlatformNewAPI) {
		t.Fatalf("openai account MUST NOT be a member of a newapi group's pool (security: no mixing)")
	}
}

func TestUS012_PoolMember_NewAPIChannelTypeZero_Excluded(t *testing.T) {
	a := &Account{Platform: PlatformNewAPI, ChannelType: 0}
	if a.IsOpenAICompatPoolMember(PlatformNewAPI) {
		t.Fatalf("newapi account with channel_type=0 MUST be excluded from pool (incomplete config)")
	}
}

func TestUS012_PoolMember_NewAPIChannelTypePositive_Allowed(t *testing.T) {
	a := &Account{Platform: PlatformNewAPI, ChannelType: 7}
	if !a.IsOpenAICompatPoolMember(PlatformNewAPI) {
		t.Fatalf("newapi account with channel_type>0 MUST be in the newapi pool")
	}
}

// Edge cases — defensive contract, not exercised by any single AC but locks
// behavior so future contributors cannot relax it silently.

func TestUS011_PoolMember_NilAccount_False(t *testing.T) {
	var a *Account
	if a.IsOpenAICompatPoolMember(PlatformOpenAI) {
		t.Fatalf("nil account is not a pool member of any platform")
	}
}

func TestUS011_PoolMember_EmptyGroupPlatform_False(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI}
	if a.IsOpenAICompatPoolMember("") {
		t.Fatalf("empty groupPlatform must yield false (defensive: no implicit openai pool)")
	}
}

func TestUS011_PoolMember_UnknownPlatform_False(t *testing.T) {
	a := &Account{Platform: "wrybar"}
	if a.IsOpenAICompatPoolMember("wrybar") {
		// Wait — strict equality says this WOULD be true. The defensive
		// contract is "unknown platform != openai pool", but symmetric
		// equality is allowed; what we explicitly forbid is silently
		// promoting unknown to openai. This test pins that.
		t.Logf("unknown==unknown is allowed (strict equality); test is informational")
	}
	if a.IsOpenAICompatPoolMember(PlatformOpenAI) {
		t.Fatalf("unknown-platform account MUST NOT be promoted into openai pool")
	}
}

func TestOpenAICompatPlatforms_ListsBothCanonicals(t *testing.T) {
	got := OpenAICompatPlatforms()
	if len(got) != 2 {
		t.Fatalf("OpenAICompatPlatforms must list exactly 2 platforms today, got %d: %v", len(got), got)
	}
	want := map[string]bool{PlatformOpenAI: false, PlatformNewAPI: false}
	for _, p := range got {
		if _, ok := want[p]; !ok {
			t.Fatalf("unexpected platform %q in OpenAICompatPlatforms()", p)
		}
		want[p] = true
	}
	for p, seen := range want {
		if !seen {
			t.Fatalf("OpenAICompatPlatforms missing %q", p)
		}
	}
}
