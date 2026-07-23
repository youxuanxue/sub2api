package service

import (
	"context"
	"os"
	"regexp"
	"strings"
	"sync"
)

// TokenKey canonical Claude Code OAuth HTTP fingerprint.
//
// Anthropic's cohort risk-control inspects multiple layers per request: TLS
// ClientHello, HTTP User-Agent / x-stainless-* identity headers, billing
// header text block, and the system-prompt shape. The TLS ClientHello is
// stable across Claude Code releases (ja3_hash unchanged 2026-05-15 ~
// 2026-05-25 covering the 2.1.142 → 2.1.150 client bump), so the DB
// profile row that pins it carries a *stable* identifier
// `tk_canonical_cc_oauth` — decoupled from any cc CLI patch version that
// happens to be captured.
//
// The User-Agent string, in contrast, changes every 2–4 weeks as new cc CLI
// patch releases ship. Hard-coding it in a Go constant means each upstream
// bump triggers rebuild + release + SQL apply + Redis clear. That cost is
// the regression PR #408 was about to permanently enshrine; instead this
// module follows the existing `internal/pkg/antigravity` UserAgent pattern:
//
//   - **compile-time default**  (DefaultClaudeCodeUserAgentVersion)
//   - **env override**          (CLAUDE_CODE_USER_AGENT_VERSION)
//   - **runtime resolver**      (ClaudeCodeUserAgentResolver) — injected at
//     bootstrap from SettingService.GetClaudeCodeUserAgentVersion, so an
//     admin UI / API change takes effect on the next request without a
//     redeploy / SQL apply / Redis clear cycle.
//
// The non-UA observed.* fields (StainlessLang, StainlessOS, StainlessArch,
// StainlessRuntime, StainlessRuntimeVersion, StainlessPackageVersion) are
// mid-frequency — they change when the cc binary upgrades Node runtime or
// the upstream Stainless SDK version. Those still live as compile-time
// defaults and a one-line code bump + redeploy is acceptable for them.

const (
	// canonicalTLSFingerprintProfileName is the stable id of the canonical
	// Anthropic OAuth TLS profile in the database. Deliberately omits any
	// cc CLI version / runtime / capture-date string so future cc releases
	// never trigger a rename + DB migration cycle. Adopting a new TLS
	// fingerprint capture (genuine ClientHello change) would either UPDATE
	// the same row's parameter fields in place or rename to a v2 id with
	// an explicit migration plan.
	canonicalTLSFingerprintProfileName = "tk_canonical_cc_oauth"

	// ClaudeCodeUserAgentVersionEnv is the environment variable name that
	// overrides the compile-time default cc CLI UA version.
	ClaudeCodeUserAgentVersionEnv = "CLAUDE_CODE_USER_AGENT_VERSION"

	// DefaultClaudeCodeUserAgentVersion is the compile-time fallback when
	// neither env nor runtime resolver provides a value. Keep in sync with
	// the most recent cc CLI release this build was validated against; the
	// admin UI / runtime resolver is the normal update path going forward.
	DefaultClaudeCodeUserAgentVersion = "2.1.218"

	// canonicalUAPrefix / canonicalUASuffix wrap the version-only field.
	// Matches interactive Claude Code REPL ingress (`claude-cli/<version> (external, cli)`).
	canonicalUAPrefix = "claude-cli/"
	canonicalUASuffix = " (external, cli)"
)

// claudeCodeUserAgentVersionPattern validates the semver shape we accept
// from env / resolver. Mirrors `antigravity.userAgentVersionPattern`.
var claudeCodeUserAgentVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// ClaudeCodeUserAgentResolver returns the runtime-overridden cc CLI UA
// version (semver string without prefix/suffix). Empty return falls back
// to env / compile-time default.
type ClaudeCodeUserAgentResolver func(ctx context.Context) string

var (
	defaultClaudeCodeUserAgentVersion = DefaultClaudeCodeUserAgentVersion
	claudeCodeUserAgentMu             sync.RWMutex
	claudeCodeUserAgentResolver       ClaudeCodeUserAgentResolver
)

func init() {
	if v := NormalizeClaudeCodeUserAgentVersion(os.Getenv(ClaudeCodeUserAgentVersionEnv)); v != "" {
		defaultClaudeCodeUserAgentVersion = v
	}
}

// NormalizeClaudeCodeUserAgentVersion validates and normalizes a candidate
// semver string. Returns "" for any invalid input so callers can fall
// through to the next layer in the resolution chain.
func NormalizeClaudeCodeUserAgentVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || !claudeCodeUserAgentVersionPattern.MatchString(version) {
		return ""
	}
	return version
}

// GetDefaultClaudeCodeUserAgentVersion returns the env-overridden compile
// default — exposed for observability / admin-UI display.
func GetDefaultClaudeCodeUserAgentVersion() string {
	return defaultClaudeCodeUserAgentVersion
}

// SetClaudeCodeUserAgentResolver registers a runtime resolver, normally
// injected at server bootstrap by SettingService so admin settings drive
// the active UA version without redeploy.
func SetClaudeCodeUserAgentResolver(r ClaudeCodeUserAgentResolver) {
	claudeCodeUserAgentMu.Lock()
	defer claudeCodeUserAgentMu.Unlock()
	claudeCodeUserAgentResolver = r
}

// GetClaudeCodeUserAgentVersionForContext returns the active cc CLI UA
// version for the request context: resolver → env (already merged into
// default at init) → compile default. Always returns a non-empty semver.
func GetClaudeCodeUserAgentVersionForContext(ctx context.Context) string {
	if ctx == nil {
		ctx = context.Background()
	}
	claudeCodeUserAgentMu.RLock()
	r := claudeCodeUserAgentResolver
	claudeCodeUserAgentMu.RUnlock()
	if r != nil {
		if v := NormalizeClaudeCodeUserAgentVersion(r(ctx)); v != "" {
			return v
		}
	}
	return defaultClaudeCodeUserAgentVersion
}

// BuildCanonicalUserAgent wraps a (validated or fallback) version into the
// full `claude-cli/<v> (external, cli)` string.
func BuildCanonicalUserAgent(version string) string {
	v := NormalizeClaudeCodeUserAgentVersion(version)
	if v == "" {
		v = defaultClaudeCodeUserAgentVersion
	}
	return canonicalUAPrefix + v + canonicalUASuffix
}

// GetCanonicalUserAgentForContext returns the full UA wire string for the
// request context; this is what the gateway pins on Redis fingerprint:{id}.
func GetCanonicalUserAgentForContext(ctx context.Context) string {
	return BuildCanonicalUserAgent(GetClaudeCodeUserAgentVersionForContext(ctx))
}

// canonicalHTTPObservedStatic holds the mid-frequency `observed.*` fields
// (everything except UserAgent). These change when the cc binary upgrades
// its Node runtime or the upstream Stainless SDK; bumping them is a code
// change + redeploy, intentionally not runtime config — a redeploy is
// cheap and there is no operational pressure to flip these between
// releases.
var canonicalHTTPObservedStatic = Fingerprint{
	StainlessLang:           "js",
	StainlessPackageVersion: "0.94.0",
	StainlessOS:             "MacOS",
	StainlessArch:           "arm64",
	StainlessRuntime:        "node",
	StainlessRuntimeVersion: "v26.3.0",
}

// IsCanonicalTLSProfileName reports whether name is the TokenKey canonical
// TLS profile id.
func IsCanonicalTLSProfileName(name string) bool {
	return strings.TrimSpace(name) == canonicalTLSFingerprintProfileName
}

// applyCanonicalHTTPObserved overwrites HTTP fingerprint fields with the
// canonical observed block. UserAgent comes from the caller-supplied
// (typically resolver-derived) string so the same helper can serve cache
// repair, cache seed, and unit tests without a global lookup. ClientID
// and UpdatedAt are preserved. Returns true when any field changed (caller
// may persist).
func applyCanonicalHTTPObserved(fp *Fingerprint, userAgent string) bool {
	if fp == nil {
		return false
	}
	if strings.TrimSpace(userAgent) == "" {
		userAgent = BuildCanonicalUserAgent("")
	}
	changed := false
	if fp.UserAgent != userAgent {
		fp.UserAgent = userAgent
		changed = true
	}
	if fp.StainlessLang != canonicalHTTPObservedStatic.StainlessLang {
		fp.StainlessLang = canonicalHTTPObservedStatic.StainlessLang
		changed = true
	}
	if fp.StainlessPackageVersion != canonicalHTTPObservedStatic.StainlessPackageVersion {
		fp.StainlessPackageVersion = canonicalHTTPObservedStatic.StainlessPackageVersion
		changed = true
	}
	if fp.StainlessOS != canonicalHTTPObservedStatic.StainlessOS {
		fp.StainlessOS = canonicalHTTPObservedStatic.StainlessOS
		changed = true
	}
	if fp.StainlessArch != canonicalHTTPObservedStatic.StainlessArch {
		fp.StainlessArch = canonicalHTTPObservedStatic.StainlessArch
		changed = true
	}
	if fp.StainlessRuntime != canonicalHTTPObservedStatic.StainlessRuntime {
		fp.StainlessRuntime = canonicalHTTPObservedStatic.StainlessRuntime
		changed = true
	}
	if fp.StainlessRuntimeVersion != canonicalHTTPObservedStatic.StainlessRuntimeVersion {
		fp.StainlessRuntimeVersion = canonicalHTTPObservedStatic.StainlessRuntimeVersion
		changed = true
	}
	return changed
}
