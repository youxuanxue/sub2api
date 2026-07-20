package kiro

// shim.go provides the local replacements for the external Kiro-Go packages
// (config / logger / auth) that this vendored protocol layer originally depended
// on. TokenKey does not embed those packages, so their data, settings, logging,
// and auth symbols are defined here. Fingerprint headers deliberately delegate
// to the TokenKey-owned internal/pkg/kiro identity builders in headers.go.
//
// Design intent:
//   - Account / AccountInfo / PromptFilterRule are pure data carriers. The
//     TokenKey integration layer (a later PR) is responsible for filling Account
//     from an ent account row and consuming the returned AccountInfo. This vendor
//     package never reads or writes a database.
//   - The remaining config.GetXxx() configuration knobs become package-level
//     functions with TokenKey defaults so the protocol layer compiles standalone.
//   - logger.* becomes thin log/slog wrappers.
//   - auth.RefreshToken / GetAuthClientForProxy live in-package (refresh.go +
//     GetAuthClientForProxy below).

import (
	"fmt"
	"log/slog"
	"net/http"
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

// KiroBonusInfo is one promotional/bonus credits bucket from getUsageLimits.
type KiroBonusInfo struct {
	Code      string  `json:"code,omitempty"`
	Label     string  `json:"label,omitempty"`
	Current   float64 `json:"current,omitempty"`
	Limit     float64 `json:"limit,omitempty"`
	Percent   float64 `json:"percent,omitempty"` // 0-100
	Status    string  `json:"status,omitempty"`
	ExpiresAt int64   `json:"expiresAt,omitempty"` // unix seconds
}

// AccountInfo carries the account metadata returned by RefreshAccountInfo.
// Fields cover exactly what rest.go populates.
type AccountInfo struct {
	Email             string          `json:"email,omitempty"`
	UserId            string          `json:"userId,omitempty"`
	SubscriptionType  string          `json:"subscriptionType,omitempty"`
	SubscriptionTitle string          `json:"subscriptionTitle,omitempty"`
	UsageCurrent      float64         `json:"usageCurrent,omitempty"`
	UsageLimit        float64         `json:"usageLimit,omitempty"`
	UsagePercent      float64         `json:"usagePercent,omitempty"`
	NextResetDate     string          `json:"nextResetDate,omitempty"`
	LastRefresh       int64           `json:"lastRefresh,omitempty"`
	TrialUsageCurrent float64         `json:"trialUsageCurrent,omitempty"`
	TrialUsageLimit   float64         `json:"trialUsageLimit,omitempty"`
	TrialUsagePercent float64         `json:"trialUsagePercent,omitempty"`
	TrialStatus       string          `json:"trialStatus,omitempty"`
	TrialExpiresAt    int64           `json:"trialExpiresAt,omitempty"`
	Bonuses           []KiroBonusInfo `json:"bonuses,omitempty"`
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

// GetEndpointFallback reports whether to try the alternate supported Kiro
// endpoint after a retryable upstream failure.
func GetEndpointFallback() bool { return true }

// GetPreferredEndpoint returns the preferred endpoint selector. "auto" keeps the
// supported ordering (current Kiro runtime, then transitional legacy q).
func GetPreferredEndpoint() string { return "auto" }

// Prompt filtering: Claude Code system prompts are preserved (Anthropic OAuth parity)
// with env/boundary noise stripped. Other filters stay off until wired to settings.
func GetFilterClaudeCode() bool      { return true }
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
