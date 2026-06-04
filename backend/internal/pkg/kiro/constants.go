// Package kiro holds TokenKey-side runtime constants and fingerprint helpers for
// the sixth platform (AWS Kiro / CodeWhisperer).
//
// The wire-level protocol (request/response translation, EventStream decoding,
// REST + token refresh) is vendored from Quorinex/Kiro-Go under
// internal/integration/kiro. This package owns the *TokenKey-controlled* knobs:
// the KiroIDE client-identity strings (SDK versions, IDE version, OS/Node tags)
// and the User-Agent builder used to mimic the real Kiro IDE on the wire. Keeping
// these here (rather than hard-coded in the vendored layer) lets the canonical
// fingerprint be bumped via setting/redeploy when Kiro IDE updates — the same
// posture as the Claude Code canonical UA (see identity_service_tk_canonical_http).
package kiro

import (
	"fmt"
	"os"
	"strings"
)

// UserAgentVersionEnv overrides the on-wire KiroIDE version at runtime without a
// code change, so the fingerprint can track upstream Kiro client releases by a
// deploy-env edit alone. Kept identical to the env key read by the vendored
// internal/integration/kiro layer so both UA-building paths stay consistent.
const UserAgentVersionEnv = "KIRO_IDE_USER_AGENT_VERSION"

// DefaultKiroAccountPriority is the HARD-ENFORCED scheduler priority baseline for
// every active kiro-platform account. The anthropic config reconciler value-syncs
// each kiro account's priority column to this constant on every tick (skip-if-aligned),
// so after a DB rebuild or for a newly-created kiro account, priority deterministically
// returns to this baseline. Smaller priority wins in the scheduler; kiro rides its own
// isolated pool and is NOT part of the anthropic window-rebalance pipeline, so this
// kiro-scoped value-sync does not conflict with that pipeline's priority ownership.
const DefaultKiroAccountPriority = 10

// SDK version strings carried in the aws-sdk-js style User-Agent. These mirror
// the values the real Kiro IDE emits; bump together with KiroIDEVersion when a
// new Kiro client ships and the fingerprint is re-aligned.
const (
	StreamingSDKVersion = "1.0.34"
	RuntimeSDKVersion   = "1.0.0"
)

// Compile-time defaults for the KiroIDE client identity. These are overridable at
// runtime (env / setting) in PR6; the constants are the canonical baseline.
// These mirror the real KiroIDE client identity observed via the vendored
// Kiro-Go GetKiroClientConfig defaults; keep in lockstep with
// internal/integration/kiro so the on-wire fingerprint stays consistent
// regardless of which layer builds the User-Agent.
const (
	DefaultKiroIDEVersion = "0.11.107"
	DefaultSystemVersion  = "darwin#24.0.0"
	DefaultNodeVersion    = "22.22.0"
)

// Streaming endpoints, in auto-fallback order. The vendored CallKiroAPI uses the
// same set; these constants are the TokenKey-side source of truth for the hosts
// the gateway egresses to (used by fingerprint/TLS profile selection in PR6).
const (
	EndpointKiroIDE        = "https://q.us-east-1.amazonaws.com/generateAssistantResponse"
	EndpointCodeWhisperer  = "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse"
	RESTAPIBase            = "https://codewhisperer.us-east-1.amazonaws.com"
	DefaultRegion          = "us-east-1"
	AmzTargetCodeWhisperer = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
)

// ClientIdentity carries the mutable client-fingerprint fields woven into the
// User-Agent. A zero value falls back to the compile-time defaults.
type ClientIdentity struct {
	KiroIDEVersion string
	SystemVersion  string
	NodeVersion    string
}

func (c ClientIdentity) withDefaults() ClientIdentity {
	if c.KiroIDEVersion == "" {
		c.KiroIDEVersion = DefaultKiroIDEVersion
	}
	if c.SystemVersion == "" {
		c.SystemVersion = DefaultSystemVersion
	}
	if c.NodeVersion == "" {
		c.NodeVersion = DefaultNodeVersion
	}
	return c
}

// BuildUserAgent renders the aws-sdk-js style User-Agent the Kiro IDE sends, e.g.
//
//	aws-sdk-js/1.0.34 ua/2.1 os/darwin#24.0.0 lang/js md/nodejs#20.18.1 api/codewhispererstreaming#1.0.34 m/E KiroIDE-0.7.45[-<machineID>]
//
// apiName/sdkVersion/mode select the streaming vs runtime variant. A non-empty
// machineID is appended to the KiroIDE-<ver> segment as the per-account
// fingerprint suffix.
func BuildUserAgent(id ClientIdentity, apiName, sdkVersion, mode, machineID string) string {
	id = id.withDefaults()
	ua := fmt.Sprintf(
		"aws-sdk-js/%s ua/2.1 os/%s lang/js md/nodejs#%s api/%s#%s %s KiroIDE-%s",
		sdkVersion, id.SystemVersion, id.NodeVersion, apiName, sdkVersion, mode, id.KiroIDEVersion,
	)
	if machineID != "" {
		ua += "-" + machineID
	}
	return ua
}

// BuildAmzUserAgent renders the shorter x-amz-user-agent header value:
//
//	aws-sdk-js/<sdkVersion> KiroIDE-<ver>[-<machineID>]
func BuildAmzUserAgent(id ClientIdentity, sdkVersion, machineID string) string {
	id = id.withDefaults()
	ua := fmt.Sprintf("aws-sdk-js/%s KiroIDE-%s", sdkVersion, id.KiroIDEVersion)
	if machineID != "" {
		ua += "-" + machineID
	}
	return ua
}

// StreamingUserAgent is the convenience builder for the streaming API surface.
func StreamingUserAgent(id ClientIdentity, machineID string) string {
	return BuildUserAgent(id, "codewhispererstreaming", StreamingSDKVersion, "m/E", machineID)
}

// ResolveClientIdentity returns the active client identity, applying the
// KIRO_IDE_USER_AGENT_VERSION env override over the compile-time defaults. This
// is the entry point callers should use so the runtime version knob is honored.
func ResolveClientIdentity() ClientIdentity {
	id := ClientIdentity{}
	if v := strings.TrimSpace(os.Getenv(UserAgentVersionEnv)); v != "" {
		id.KiroIDEVersion = v
	}
	return id.withDefaults()
}
