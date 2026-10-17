package service

import (
	"regexp"
	"strings"
)

// edgeIDPattern identifies the historical TokenKey edge relay host shape.
// It remains a small identity predicate for routing and model eligibility
// compatibility; retired edge account inventory and management endpoints do
// not depend on it.
var edgeIDPattern = regexp.MustCompile(`^https?://api-([a-z0-9]+)\.tokenkey\.dev/?$`)

// edgeStubAllPoolsPlatform is the historical sentinel used by the capacity
// reconciler when a relay's transport platform does not identify its pool.
const edgeStubAllPoolsPlatform = "all"

// isEdgeMirrorStub reports whether an account is an API-key relay stub whose
// base URL points at a TokenKey edge host.
func isEdgeMirrorStub(a *Account, re *regexp.Regexp) bool {
	if a == nil || re == nil || a.Type != AccountTypeAPIKey {
		return false
	}
	baseURL := strings.TrimSpace(a.GetCredential("base_url"))
	return baseURL != "" && re.MatchString(baseURL)
}

// edgeIDFromBaseURL derives the historical edge id from a relay base URL.
func edgeIDFromBaseURL(baseURL string) string {
	if m := edgeIDPattern.FindStringSubmatch(baseURL); len(m) == 2 {
		return m[1]
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(baseURL, "https://"), "http://")
	if host, _, ok := strings.Cut(trimmed, "/"); ok {
		return host
	}
	return trimmed
}

// MirrorStubEdgeID returns the edge id a prod relay stub points at, or an
// empty string for ordinary accounts. This identity is still used by a few
// protocol compatibility guards while the edge account console is retired.
func MirrorStubEdgeID(acc *Account) string {
	if !isEdgeMirrorStub(acc, edgeIDPattern) {
		return ""
	}
	return edgeIDFromBaseURL(strings.TrimSpace(acc.GetCredential("base_url")))
}

// edgeStubPoolPlatform resolves the pool represented by a relay stub. It is
// retained for the capacity reconciliation path; the retired account console
// no longer uses it for inventory fan-out.
func edgeStubPoolPlatform(acct *Account) string {
	if acct == nil {
		return ""
	}
	if acct.IsRetiredSupplier() {
		return PlatformNewAPI
	}
	if mp := strings.ToLower(strings.TrimSpace(acct.GetCredential("mirror_platform"))); mp != "" {
		return mp
	}
	if acct.Platform == PlatformNewAPI {
		return edgeStubAllPoolsPlatform
	}
	return acct.Platform
}
