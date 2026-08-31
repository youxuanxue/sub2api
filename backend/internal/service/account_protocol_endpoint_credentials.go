package service

import (
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

// apiBaseURLsCredentialKey is the credentials map key for per-protocol base URLs.
// Kept as a local alias so identity-key handling stays next to the exclusive flag.
const apiBaseURLsCredentialKey = "api_base_urls"

// CredentialMergeMode selects how non-identity credential keys are merged before
// FinalizeAccountCredentials runs.
type CredentialMergeMode int

const (
	// CredentialMergeAdmin mirrors MergePreservingSensitiveCreds: non-sensitive
	// keys are wholly decided by incoming (omit = delete); sensitive keys are
	// preserved when omitted.
	CredentialMergeAdmin CredentialMergeMode = iota
	// CredentialMergePreserveAll mirrors CRS mergeMap: existing keys survive
	// unless incoming overwrites them. Protocol identity keys are still never
	// inherited implicitly (see MergeAccountCredentials).
	CredentialMergePreserveAll
)

// MergeAccountCredentials is the SSOT credential merge for every persistence
// write that combines an existing map with an incoming patch/payload.
//
// Protocol identity keys (api_base_urls, protocol_endpoints_exclusive) are NEVER
// inherited from existing unless incoming explicitly sets them. That stops
// base_url / api_key rotations (CRS, admin edit, bulk patch) from keeping a
// stale exclusive routing identity that would send new secrets to an old
// upstream. Callers must then persist the returned map (or run Finalize alone
// after a full replace).
func MergeAccountCredentials(existing, incoming map[string]any, channelType int, mode CredentialMergeMode) map[string]any {
	var merged map[string]any
	switch mode {
	case CredentialMergePreserveAll:
		merged = mergeMap(existing, incoming)
	default:
		merged = MergePreservingSensitiveCreds(existing, incoming)
	}
	if incoming == nil || !credentialMapHasKey(incoming, apiBaseURLsCredentialKey) {
		delete(merged, apiBaseURLsCredentialKey)
	}
	if incoming == nil || !credentialMapHasKey(incoming, ProtocolEndpointsExclusiveCredentialKey) {
		delete(merged, ProtocolEndpointsExclusiveCredentialKey)
	}
	return FinalizeAccountCredentials(merged, channelType)
}

// FinalizeAccountCredentials is the SSOT post-write normalizer for account
// credentials. Every create / full-replace / merge path must run it before
// persistence so exclusive chat identity cannot diverge from base_url.
func FinalizeAccountCredentials(credentials map[string]any, channelType int) map[string]any {
	return reconcileExclusiveProtocolEndpointCredentials(credentials, channelType)
}

// PrepareBulkCredentialPatch ensures a JSONB-merge bulk credentials patch cannot
// leave stale exclusive identity behind when it rotates base_url or api_key
// without explicitly declaring protocol endpoints.
func PrepareBulkCredentialPatch(patch map[string]any) map[string]any {
	if len(patch) == 0 {
		return patch
	}
	_, hasBaseURL := patch["base_url"]
	_, hasAPIKey := patch["api_key"]
	if !hasBaseURL && !hasAPIKey {
		return patch
	}
	_, hasBaseURLs := patch[apiBaseURLsCredentialKey]
	_, hasExclusive := patch[ProtocolEndpointsExclusiveCredentialKey]
	if hasBaseURLs || hasExclusive {
		return FinalizeAccountCredentials(patch, 0)
	}
	out := make(map[string]any, len(patch)+2)
	for key, value := range patch {
		out[key] = value
	}
	// JSON null clears the effective identity on JSONB || merge (nil does not
	// type-assert to bool/map, so exclusive routing falls back to base_url).
	out[apiBaseURLsCredentialKey] = nil
	out[ProtocolEndpointsExclusiveCredentialKey] = nil
	return out
}

// deriveExclusiveChatProtocolURL builds the chat_completions URL declared under
// protocol_endpoints_exclusive. Shared by supplier projection and reconcile so
// Qianfan / ordinary OpenAI chat shapes cannot drift.
func deriveExclusiveChatProtocolURL(baseURL string, channelType int) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if channelType == newapiconstant.ChannelTypeBaiduV2 {
		return strings.TrimRight(newapiintegration.QianfanBaseURL, "/") + "/v2/chat/completions"
	}
	return trimmed
}

// applyExclusiveChatProtocolEndpoints writes the chat-only exclusive identity
// block used by supplier-managed NewAPI accounts.
func applyExclusiveChatProtocolEndpoints(credentials map[string]any, baseURL string, channelType int) {
	if credentials == nil {
		return
	}
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	credentials["base_url"] = trimmed
	credentials[apiBaseURLsCredentialKey] = map[string]any{
		APIProtocolChatCompletions: deriveExclusiveChatProtocolURL(trimmed, channelType),
	}
	credentials[ProtocolEndpointsExclusiveCredentialKey] = true
}

func reconcileExclusiveProtocolEndpointCredentials(credentials map[string]any, channelType int) map[string]any {
	if credentials == nil {
		return nil
	}
	if !credentialsDeclareExclusiveProtocolEndpoints(credentials) {
		return credentials
	}
	baseURL := strings.TrimSpace(credentialString(credentials["base_url"]))
	if baseURL == "" {
		delete(credentials, ProtocolEndpointsExclusiveCredentialKey)
		delete(credentials, apiBaseURLsCredentialKey)
		return credentials
	}
	wantChat := deriveExclusiveChatProtocolURL(baseURL, channelType)
	raw, _ := credentials[apiBaseURLsCredentialKey].(map[string]any)
	out := make(map[string]any, len(raw)+1)
	for key, value := range raw {
		out[key] = value
	}
	gotChat := strings.TrimSpace(credentialString(out[APIProtocolChatCompletions]))
	if !exclusiveChatURLsAligned(gotChat, wantChat) {
		out[APIProtocolChatCompletions] = wantChat
	}
	credentials[apiBaseURLsCredentialKey] = out
	credentials[ProtocolEndpointsExclusiveCredentialKey] = true
	return credentials
}

func credentialsDeclareExclusiveProtocolEndpoints(credentials map[string]any) bool {
	return accountDeclaresExclusiveProtocolEndpoints(&Account{Credentials: credentials})
}

func exclusiveChatURLsAligned(got, want string) bool {
	return strings.TrimRight(strings.TrimSpace(got), "/") == strings.TrimRight(strings.TrimSpace(want), "/")
}

func credentialMapHasKey(credentials map[string]any, key string) bool {
	if credentials == nil {
		return false
	}
	_, ok := credentials[key]
	return ok
}

func credentialString(value any) string {
	text, _ := value.(string)
	return text
}
