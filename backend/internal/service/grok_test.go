//go:build unit

package service

import (
	"strconv"
	"strings"
	"testing"
	"time"
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
		{"no active grok subscription", 403, `{"error":{"message":"You do not have an active Grok subscription"}}`, true},
		{"out of available resources", 403, `{"error":{"message":"You have run out of available resources"}}`, true},
		{"supergrok heavy required", 403, `{"error":{"message":"This requires SuperGrok Heavy"}}`, true},
		{"generic WAF 403 not matched", 403, `{"error":{"message":"Forbidden"}}`, false},
		{"401 with grok body is not 403", 401, `{"error":{"message":"active Grok subscription"}}`, false},
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
// (default api.x.ai/v1, per-account override with trailing-slash trim).
func TestAccountGrokHelpers(t *testing.T) {
	a := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{}}
	if !a.IsGrok() {
		t.Fatal("IsGrok should be true for platform=grok")
	}
	if got := a.GetGrokBaseURL(); got != "https://api.x.ai/v1" {
		t.Errorf("default GetGrokBaseURL = %q, want https://api.x.ai/v1", got)
	}
	a.Credentials["base_url"] = "https://proxy.example.com/v1/"
	if got := a.GetGrokBaseURL(); got != "https://proxy.example.com/v1" {
		t.Errorf("override GetGrokBaseURL = %q, want trailing-slash trimmed", got)
	}
	if (&Account{Platform: PlatformOpenAI}).IsGrok() {
		t.Fatal("IsGrok should be false for a non-grok platform")
	}
}

// TestGrokTokenRefresher_CanRefreshNeedsRefresh covers the refresher gating
// (grok+oauth only) and the expiry-window logic (no expiry -> refresh to prime;
// far future -> skip; within window -> refresh).
func TestGrokTokenRefresher_CanRefreshNeedsRefresh(t *testing.T) {
	r := NewGrokTokenRefresher()
	grok := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{}}

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
	grok.Credentials["expires_at"] = strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	if r.NeedsRefresh(grok, 5*time.Minute) {
		t.Fatal("NeedsRefresh should be false when expiry is far in the future")
	}
	grok.Credentials["expires_at"] = strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)
	if !r.NeedsRefresh(grok, 5*time.Minute) {
		t.Fatal("NeedsRefresh should be true when within the refresh window")
	}
}
