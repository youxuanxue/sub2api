package kiro

// shim.go provides the local replacements for the external Kiro-Go packages
// (config / logger / auth) that this vendored protocol layer originally depended
// on. TokenKey does not embed those packages, so every cross-package symbol the
// vendored files reference is defined here against stdlib + google/uuid only.
//
// Design intent:
//   - Account / AccountInfo / PromptFilterRule are pure data carriers. The
//     TokenKey integration layer (a later PR) is responsible for filling Account
//     from an ent account row and consuming the returned AccountInfo. This vendor
//     package never reads or writes a database.
//   - The config.GetXxx() configuration knobs become package-level functions with
//     conservative defaults (no proxy, endpoint fallback on, prompt filtering off)
//     so the protocol layer compiles and runs standalone. A later PR can wire them
//     to TokenKey settings.
//   - logger.* becomes thin log/slog wrappers.
//   - auth.RefreshToken / GetAuthClientForProxy live in-package (refresh.go +
//     GetAuthClientForProxy below).

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ==================== Credential / metadata carriers ====================

// Account is the credential carrier for the vendored Kiro protocol layer.
// Field names match the access points in the vendored files verbatim so no
// access site needed to change. The TokenKey layer fills this from an ent
// account; this package treats it as read-mostly (ProfileArn may be populated
// in-memory by ResolveProfileArn, but nothing here persists it).
type Account struct {
	ID           string `json:"id"`
	Email        string `json:"email,omitempty"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ProfileArn   string `json:"profileArn,omitempty"`
	Region       string `json:"region,omitempty"`
	MachineId    string `json:"machineId,omitempty"`
	AuthMethod   string `json:"authMethod,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	ProxyURL     string `json:"proxyURL,omitempty"`
	Enabled      bool   `json:"enabled"`
	BanStatus    string `json:"banStatus,omitempty"`
	BanReason    string `json:"banReason,omitempty"`
	BanTime      int64  `json:"banTime,omitempty"`
}

// AccountInfo carries the account metadata returned by RefreshAccountInfo.
// Fields cover exactly what rest.go populates.
type AccountInfo struct {
	Email             string  `json:"email,omitempty"`
	UserId            string  `json:"userId,omitempty"`
	SubscriptionType  string  `json:"subscriptionType,omitempty"`
	SubscriptionTitle string  `json:"subscriptionTitle,omitempty"`
	UsageCurrent      float64 `json:"usageCurrent,omitempty"`
	UsageLimit        float64 `json:"usageLimit,omitempty"`
	UsagePercent      float64 `json:"usagePercent,omitempty"`
	NextResetDate     string  `json:"nextResetDate,omitempty"`
	LastRefresh       int64   `json:"lastRefresh,omitempty"`
	TrialUsageCurrent float64 `json:"trialUsageCurrent,omitempty"`
	TrialUsageLimit   float64 `json:"trialUsageLimit,omitempty"`
	TrialUsagePercent float64 `json:"trialUsagePercent,omitempty"`
	TrialStatus       string  `json:"trialStatus,omitempty"`
	TrialExpiresAt    int64   `json:"trialExpiresAt,omitempty"`
}

// PromptFilterRule defines a single custom prompt sanitization rule used by
// translator.go applyFilterRule. Type can be "regex" or "lines-containing".
type PromptFilterRule struct {
	Type    string `json:"type"`
	Match   string `json:"match"`
	Replace string `json:"replace,omitempty"`
	Enabled bool   `json:"enabled"`
}

// ==================== Configuration knobs (defaults) ====================

// GetProxyURL returns the global outbound proxy. The TokenKey layer handles
// egress proxying, so the vendored default is empty (no proxy).
func GetProxyURL() string { return "" }

// GetEndpointFallback reports whether to try alternate Kiro endpoints on quota
// exhaustion. Default true preserves the upstream resilient behavior.
func GetEndpointFallback() bool { return true }

// GetPreferredEndpoint returns the preferred endpoint selector. "auto" keeps the
// upstream default ordering (Kiro IDE → CodeWhisperer → AmazonQ).
func GetPreferredEndpoint() string { return "auto" }

// kiroClientConfig mirrors the upstream config.KiroClientConfig shape used by
// the aws-sdk-js style User-Agent builder in headers.go.
type kiroClientConfig struct {
	KiroVersion   string
	SystemVersion string
	NodeVersion   string
}

// kiroDefaultClientVersion is the canonical KiroIDE version baked into the
// User-Agent. Keep in lockstep with internal/pkg/kiro.DefaultKiroIDEVersion
// (that package is the TK-side authority + sentinel/test-guarded source).
const kiroDefaultClientVersion = "0.12.333"

// kiroUserAgentVersionEnv lets operators bump the on-wire KiroIDE version
// without a code change / image rebuild when the upstream Kiro client ships a
// new release (TK reliability/anti-fragility knob, mirrors the Claude Code
// canonical UA version override). Set it in the Stage0 deploy env.
const kiroUserAgentVersionEnv = "KIRO_IDE_USER_AGENT_VERSION"

// GetKiroClientConfig returns the client version triple used to build the
// aws-sdk-js style User-Agent. KiroVersion defaults to kiroDefaultClientVersion
// but is overridable at runtime via the KIRO_IDE_USER_AGENT_VERSION env var so
// the fingerprint can track upstream Kiro client releases without a redeploy of
// new code. NodeVersion mirrors the upstream default; SystemVersion is
// OS-derived.
func GetKiroClientConfig() kiroClientConfig {
	kiroVersion := kiroDefaultClientVersion
	if v := strings.TrimSpace(os.Getenv(kiroUserAgentVersionEnv)); v != "" {
		kiroVersion = v
	}
	return kiroClientConfig{
		KiroVersion:   kiroVersion,
		SystemVersion: defaultSystemVersion(),
		NodeVersion:   "22.22.0",
	}
}

func defaultSystemVersion() string {
	switch runtime.GOOS {
	case "windows":
		return "win32#10.0.22631"
	case "darwin":
		return "darwin#24.6.0"
	default:
		return "linux#6.6.87"
	}
}

// Prompt filtering is disabled in the vendored default to avoid depending on
// TokenKey settings; a later PR can wire these to admin-configurable knobs.
func GetFilterClaudeCode() bool      { return false }
func GetFilterStripBoundaries() bool { return false }
func GetFilterEnvNoise() bool        { return false }

// GetPromptFilterRules returns the user-defined prompt filter rules. None by
// default.
func GetPromptFilterRules() []PromptFilterRule { return nil }

// ==================== Logging shims ====================

func logDebugf(format string, args ...any) { slog.Debug(fmt.Sprintf(format, args...)) }
func logInfof(format string, args ...any)  { slog.Info(fmt.Sprintf(format, args...)) }
func logWarnf(format string, args ...any)  { slog.Warn(fmt.Sprintf(format, args...)) }
func logErrorf(format string, args ...any) { slog.Error(fmt.Sprintf(format, args...)) }

// ==================== Auth HTTP client shim ====================

// authProxyClientCache caches per-proxy auth HTTP clients (mirrors upstream auth).
var authProxyClientCache sync.Map

// GetAuthClientForProxy returns an HTTP client (30s timeout) configured for the
// given proxy URL, used by RefreshToken. If proxyURL is empty, a process-wide
// default client (ProxyFromEnvironment) is returned. Reuses buildKiroTransport
// from client.go so transport tuning stays in one place.
func GetAuthClientForProxy(proxyURL string) *http.Client {
	if proxyURL == "" {
		if c, ok := authProxyClientCache.Load(""); ok {
			return c.(*http.Client)
		}
	} else if c, ok := authProxyClientCache.Load(proxyURL); ok {
		return c.(*http.Client)
	}
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: buildKiroTransport(proxyURL),
	}
	authProxyClientCache.Store(proxyURL, client)
	return client
}
