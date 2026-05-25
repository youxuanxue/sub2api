package service

import "strings"

// TokenKey: when an Anthropic OAuth account binds the canonical TLS fingerprint
// profile, HTTP User-Agent and x-stainless-* must match the captured observed
// block — not ingress clients (prod relay may forward mixed CLI/SDK UAs).
//
// TLS profiles only affect ClientHello; this companion pins the HTTP layer to
// deploy/aws/stage0/claude_cli_2_1_150_node24_20260525.json observed.* values.

const canonicalTLSFingerprintProfileName = "claude_cli_2_1_150_node24_20260525"

// canonicalHTTPObserved matches deploy/aws/stage0/claude_cli_2_1_150_node24_20260525.json observed.
var canonicalHTTPObserved = Fingerprint{
	UserAgent:               "claude-cli/2.1.150 (external, sdk-cli)",
	StainlessLang:           "js",
	StainlessPackageVersion: "0.94.0",
	StainlessOS:             "MacOS",
	StainlessArch:           "arm64",
	StainlessRuntime:        "node",
	StainlessRuntimeVersion: "v24.3.0",
}

// IsCanonicalTLSProfileName reports whether name is the TokenKey canonical TLS profile.
func IsCanonicalTLSProfileName(name string) bool {
	return strings.TrimSpace(name) == canonicalTLSFingerprintProfileName
}

// applyCanonicalHTTPObserved overwrites HTTP fingerprint fields from the canonical
// observed block. ClientID and UpdatedAt are preserved. Returns true when any
// field changed (caller may persist).
func applyCanonicalHTTPObserved(fp *Fingerprint) bool {
	if fp == nil {
		return false
	}
	changed := false
	if fp.UserAgent != canonicalHTTPObserved.UserAgent {
		fp.UserAgent = canonicalHTTPObserved.UserAgent
		changed = true
	}
	if fp.StainlessLang != canonicalHTTPObserved.StainlessLang {
		fp.StainlessLang = canonicalHTTPObserved.StainlessLang
		changed = true
	}
	if fp.StainlessPackageVersion != canonicalHTTPObserved.StainlessPackageVersion {
		fp.StainlessPackageVersion = canonicalHTTPObserved.StainlessPackageVersion
		changed = true
	}
	if fp.StainlessOS != canonicalHTTPObserved.StainlessOS {
		fp.StainlessOS = canonicalHTTPObserved.StainlessOS
		changed = true
	}
	if fp.StainlessArch != canonicalHTTPObserved.StainlessArch {
		fp.StainlessArch = canonicalHTTPObserved.StainlessArch
		changed = true
	}
	if fp.StainlessRuntime != canonicalHTTPObserved.StainlessRuntime {
		fp.StainlessRuntime = canonicalHTTPObserved.StainlessRuntime
		changed = true
	}
	if fp.StainlessRuntimeVersion != canonicalHTTPObserved.StainlessRuntimeVersion {
		fp.StainlessRuntimeVersion = canonicalHTTPObserved.StainlessRuntimeVersion
		changed = true
	}
	return changed
}
