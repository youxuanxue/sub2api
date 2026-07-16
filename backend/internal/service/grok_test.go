//go:build unit

package service

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// TestTkIsGrokEntitlement403 is the load-bearing regression guard for the
// Heavy-only entitlement classifier: a real grok entitlement-403 must be
// recognized (so it is NOT failed-over pool-wide / masked as 502), while a
// generic 403 and a non-403 must not trip it.
func TestTkIsGrokEntitlement403(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		// xAI's real envelope (confirmed live against api.x.ai): {"code":"...","error":"<string>"}
		{"no active grok subscription (xAI string shape)", 403, `{"code":"forbidden","error":"You do not have an active Grok subscription"}`, true},
		{"out of available resources (xAI string shape)", 403, `{"code":"forbidden","error":"You have run out of available resources"}`, true},
		// OpenAI object shape must still match (defense in depth)
		{"supergrok heavy required (object shape)", 403, `{"error":{"message":"This requires SuperGrok Heavy"}}`, true},
		{"generic WAF 403 not matched", 403, `{"code":"forbidden","error":"Forbidden"}`, false},
		{"401 with grok body is not 403", 401, `{"code":"unauthenticated","error":"active Grok subscription"}`, false},
		{"empty body", 403, ``, false},
	}
	for _, c := range cases {
		if got := tkIsGrokEntitlement403(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%s: tkIsGrokEntitlement403(%d) = %v, want %v", c.name, c.status, got, c.want)
		}
	}
	if msg := tkGrokEntitlement403ClientMessage("upstream detail"); msg == "" || !strings.Contains(msg, "Heavy") {
		t.Errorf("client message must be non-empty and mention Heavy, got %q", msg)
	}
}

// TestAccountGrokHelpers covers the grok account predicate + base-URL resolution
// (default CLI gateway, per-account override with trailing-slash trim).
func TestAccountGrokHelpers(t *testing.T) {
	a := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{}}
	if !a.IsGrok() {
		t.Fatal("IsGrok should be true for platform=grok")
	}
	if got := a.GetGrokBaseURL(); got != xai.DefaultCLIBaseURL {
		t.Errorf("default GetGrokBaseURL = %q, want %s", got, xai.DefaultCLIBaseURL)
	}
	a.Credentials["base_url"] = "https://proxy.example.com/v1/"
	if got := a.GetGrokBaseURL(); got != "https://proxy.example.com/v1" {
		t.Errorf("override GetGrokBaseURL = %q, want trailing-slash trimmed", got)
	}
	if (&Account{Platform: PlatformOpenAI}).IsGrok() {
		t.Fatal("IsGrok should be false for a non-grok platform")
	}

	relay := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "edge-key",
			"base_url": "https://api-us4.tokenkey.dev",
		},
	}
	if !relay.IsGrokAPIKey() {
		t.Fatal("IsGrokAPIKey should be true for platform=grok,type=apikey")
	}
	if got := relay.GetOpenAIApiKey(); got != "edge-key" {
		t.Fatalf("grok apikey relay GetOpenAIApiKey = %q, want edge-key", got)
	}
	if got := relay.GetOpenAIBaseURL(); got != "https://api-us4.tokenkey.dev" {
		t.Fatalf("grok apikey relay GetOpenAIBaseURL = %q, want edge base URL", got)
	}
	if relay.IsGrokOAuth() {
		t.Fatal("IsGrokOAuth should be false for grok apikey relay")
	}

	newapi := &Account{
		Platform: PlatformNewAPI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "newapi-key",
		},
	}
	if got := newapi.GetOpenAIApiKey(); got != "" {
		t.Fatalf("newapi must not use the OpenAI native API-key getter, got %q", got)
	}
}

// TestGrokTokenRefresher_CanRefreshNeedsRefresh covers the refresher gating
// (grok+oauth only) and the expiry-window logic (no expiry -> refresh to prime;
// far future -> skip; within window -> refresh).
func TestGrokTokenRefresher_CanRefreshNeedsRefresh(t *testing.T) {
	r := NewGrokTokenRefresher(nil)
	grok := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "at", "refresh_token": "rt"}}

	if !r.CanRefresh(grok) {
		t.Fatal("CanRefresh should be true for grok+oauth")
	}
	if r.CanRefresh(&Account{Platform: PlatformGrok, Type: AccountTypeAPIKey}) {
		t.Fatal("CanRefresh should be false for grok+apikey")
	}
	if r.CanRefresh(&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}) {
		t.Fatal("CanRefresh should be false for a non-grok platform")
	}

	if !r.NeedsRefresh(grok, 5*time.Minute) {
		t.Fatal("NeedsRefresh should be true when no expiry is recorded (prime path)")
	}
	// Far future must exceed the refresher's minimum skew (grokTokenRefreshSkew = 1h).
	grok.Credentials["expires_at"] = strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10)
	if r.NeedsRefresh(grok, 5*time.Minute) {
		t.Fatal("NeedsRefresh should be false when expiry is far in the future")
	}
	grok.Credentials["expires_at"] = strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)
	if !r.NeedsRefresh(grok, 5*time.Minute) {
		t.Fatal("NeedsRefresh should be true when within the refresh window")
	}
}

// TestTkInputHasNonEmptyCredential guards the UpdateAccount gate: a grok edit
// only fires the live xAI re-validate when the refresh_token was actually
// (re)pasted — a blank/absent field must NOT trigger a network call (else an
// unrelated edit would be blocked by a transient xAI outage).
func TestTkInputHasNonEmptyCredential(t *testing.T) {
	if !tkInputHasNonEmptyCredential(map[string]any{"refresh_token": "rt"}, "refresh_token") {
		t.Fatal("re-pasted refresh_token must be detected as provided")
	}
	if tkInputHasNonEmptyCredential(map[string]any{"refresh_token": "   "}, "refresh_token") {
		t.Fatal("whitespace-only refresh_token must count as not provided")
	}
	if tkInputHasNonEmptyCredential(map[string]any{}, "refresh_token") {
		t.Fatal("absent refresh_token must count as not provided")
	}
	if tkInputHasNonEmptyCredential(nil, "refresh_token") {
		t.Fatal("nil credentials must count as not provided")
	}
	if tkInputHasNonEmptyCredential(map[string]any{"refresh_token": 123}, "refresh_token") {
		t.Fatal("non-string refresh_token must count as not provided")
	}
}
