//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

// attachTestNativeDeclaredCapability simulates the persisted declaration seed
// from account creation/startup. Runtime snapshots never reconstruct this fact.
func attachTestNativeDeclaredCapability(account *Account) {
	if account == nil || account.ProtocolEndpointCapability != nil || account.ProtocolEndpointCapabilityID != nil {
		return
	}
	identity, governed, err := BuildProtocolEndpointIdentity(account)
	if err != nil || !governed {
		return
	}
	declared := NativeDeclaredProtocolContract(identity)
	if len(declared) == 0 {
		return
	}
	id := account.ID + 100000
	account.ProtocolEndpointCapabilityID = &id
	account.ProtocolEndpointCapability = &ProtocolEndpointCapability{ID: id, CapabilityKey: identity.Key(), Identity: identity, SupportedProtocols: declared, Revision: 1, ProbeEvidence: ProtocolProbeEvidence{NativeDeclaration: true}}
}

func TestGeminiNativeDeclaredContractDoesNotInventProbeEvidence(t *testing.T) {
	account := webCandidateAccount(true)
	attachTestNativeDeclaredCapability(&account)
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolChatCompletions, "", "gemini-3-flash", false, []byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	snapshot, err := protocolAccountSnapshotForRequest(&account, request)
	require.NoError(t, err)
	plan, err := NewProtocolRouter().Plan(request, snapshot)
	require.NoError(t, err)
	require.Equal(t, protocolrouter.ProtocolGeminiGenerateContent, plan.TargetProtocol())
	require.Equal(t, "https://example.invalid/v1beta/models/gemini-3.8-flash:generateContent", plan.Endpoint())
	require.True(t, account.ProtocolEndpointCapability.ProbeEvidence.NativeDeclaration)
	require.False(t, account.ProtocolEndpointCapability.ProbeEvidence.InitialProbeCompleted)
	require.False(t, account.ProtocolEndpointCapability.ProbeEvidence.OfficialSeed)
	// Explicit linked conflicts and denials remain authoritative.
	attachTestProtocolCapability(&account, protocolrouter.ProtocolGeminiGenerateContent)
	account.ProtocolEndpointCapability.IdentityConflict = true
	_, err = protocolAccountSnapshotForRequest(&account, request)
	require.ErrorIs(t, err, ErrProtocolCapabilityUnknown)
	account.ProtocolEndpointCapability.IdentityConflict = false
	account.ProtocolEndpointCapability.SupportedProtocols = []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions}
	snapshot, err = protocolAccountSnapshotForRequest(&account, request)
	require.NoError(t, err)
	_, err = NewProtocolRouter().Plan(request, snapshot)
	require.ErrorIs(t, err, protocolrouter.ErrNoLegalRoute)
}

func TestGeminiWebProviderIdentityInvalidatesLinkedGeneralCapability(t *testing.T) {
	account := webCandidateAccount(true)
	delete(account.Credentials, GeminiWebRelayCredentialKey)
	attachTestProtocolCapability(&account, protocolrouter.ProtocolGeminiGenerateContent)
	generalKey := account.ProtocolEndpointCapability.CapabilityKey
	account.Credentials[GeminiWebRelayCredentialKey] = true
	identity, governed, err := BuildProtocolEndpointIdentity(&account)
	require.NoError(t, err)
	require.True(t, governed)
	require.NotEqual(t, generalKey, identity.Key())
	_, err = ProtocolAccountSnapshot(&account, "gemini-3-flash")
	require.ErrorIs(t, err, ErrProtocolCapabilityUnknown)
}

func TestGeminiWebNativeImageRejectsHistoryBeforeSelection(t *testing.T) {
	web := webCandidateAccount(true)
	peer := webCandidateAccount(false)
	peer.ID = 201
	delete(peer.Credentials, "gemini_web")
	group := grp(740, PlatformGemini, 1, false)
	group.AllowImageGeneration = true
	resolver, _, key := globalCandidateFixture([]Group{group}, []Account{web, peer})
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"first"}]},{"role":"model","parts":[{"text":"earlier output"}]},{"role":"user","parts":[{"text":"Draw a cube"}]}]}`)
	_, state, err := resolver.PrepareCandidateRequest(context.Background(), key, ShapeGemini, "/v1beta/models/nano-2:generateContent", "nano-2", body, "", "")
	require.NoError(t, err)
	require.Equal(t, peer.ID, state.current.account.ID)
	require.Equal(t, protocolrouter.ProtocolGeminiGenerateContent, state.current.plan.TargetProtocol())
}

func TestGeminiWebOriginalIntentFallsBackToGeneralGemini(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		shape            UniversalShape
	}{
		{"chat", "/v1/chat/completions", `{"model":"gemini-3-flash","messages":[{"role":"system","content":"instruction"},{"role":"user","content":"hello"}]}`, ShapeOpenAIChat},
		{"messages", "/v1/messages", `{"model":"gemini-3-flash","max_tokens":64,"system":"instruction","messages":[{"role":"user","content":"hello"}]}`, ShapeAnthropicMessages},
		{"responses", "/v1/responses", `{"model":"gemini-3-flash","instructions":"instruction","input":"hello"}`, ShapeOpenAIChat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			web := webCandidateAccount(true)
			peer := webCandidateAccount(false)
			peer.ID = 201
			delete(peer.Credentials, "gemini_web")
			group := grp(740, PlatformGemini, 1, false)
			group.AllowMessagesDispatch = true
			resolver, _, key := globalCandidateFixture([]Group{group}, []Account{web, peer})
			_, state, err := resolver.PrepareCandidateRequest(context.Background(), key, tc.shape, tc.path, "gemini-3-flash", []byte(tc.body), "", "")
			require.NoError(t, err)
			require.Equal(t, peer.ID, state.current.account.ID)
			require.Equal(t, protocolrouter.ProtocolGeminiGenerateContent, state.current.plan.TargetProtocol())
			if tc.shape == ShapeAnthropicMessages {
				group.AllowMessagesDispatch = false
				resolver, _, key = globalCandidateFixture([]Group{group}, []Account{web, peer})
				_, state, err = resolver.PrepareCandidateRequest(context.Background(), key, tc.shape, tc.path, "gemini-3-flash", []byte(tc.body), "", "")
				require.Error(t, err)
				require.Nil(t, state)
			}
		})
	}
}

func TestGeminiNativeContractDoesNotFanOutExclusiveCompatibilityEndpoints(t *testing.T) {
	account := webCandidateAccount(true)
	account.Credentials[ProtocolEndpointsExclusiveCredentialKey] = true
	account.Credentials["api_base_urls"] = map[string]any{APIProtocolChatCompletions: "https://compat.example.invalid"}
	attachTestNativeDeclaredCapability(&account)
	identity, governed, err := BuildProtocolEndpointIdentity(&account)
	require.NoError(t, err)
	require.True(t, governed)
	require.Equal(t, map[protocolrouter.Protocol]ProtocolEndpoint{
		protocolrouter.ProtocolGeminiGenerateContent: {URL: "https://example.invalid/v1beta/models/{model}:{action}", APIVersion: "v1beta"},
	}, identity.ProtocolEndpoints)
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolChatCompletions, "", "gemini-3-flash", false, []byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	snapshot, err := protocolAccountSnapshotForRequest(&account, request)
	require.NoError(t, err)
	plan, err := NewProtocolRouter().Plan(request, snapshot)
	require.NoError(t, err)
	require.Equal(t, "https://example.invalid/v1beta/models/gemini-3.8-flash:generateContent", plan.Endpoint())
}

func TestGeminiWebSessionRemovalCannotReuseNativeDeclaration(t *testing.T) {
	account := webCandidateAccount(false)
	attachTestNativeDeclaredCapability(&account)
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolGeminiGenerateContent, "", "gemini-3-flash", false, []byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`))
	require.NoError(t, err)
	snapshot, err := protocolAccountSnapshotForRequest(&account, request)
	require.NoError(t, err)
	_, err = NewProtocolRouter().Plan(request, snapshot)
	require.NoError(t, err)
	account.Credentials["gemini_web"] = map[string]any{}
	snapshot, err = protocolAccountSnapshotForRequest(&account, request)
	require.NoError(t, err)
	_, err = NewProtocolRouter().Plan(request, snapshot)
	require.ErrorIs(t, err, protocolrouter.ErrNoLegalRoute)
}

func TestGeminiNativeMissingPersistedDeclarationFailsClosed(t *testing.T) {
	account := webCandidateAccount(true)
	_, err := ProtocolAccountSnapshot(&account, "gemini-3-flash")
	require.ErrorIs(t, err, ErrProtocolCapabilityUnknown)
}

func TestGeminiWebBestEffortPrefersExactGenericPeer(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		shape            UniversalShape
	}{
		{"messages", "/v1/messages", `{"model":"gemini-3-flash","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`, ShapeAnthropicMessages},
		{"chat", "/v1/chat/completions", `{"model":"gemini-3-flash","max_completion_tokens":128,"messages":[{"role":"user","content":"hello"}]}`, ShapeOpenAIChat},
		{"responses", "/v1/responses", `{"model":"gemini-3-flash","max_output_tokens":128,"input":"hello"}`, ShapeOpenAIChat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			web := webCandidateAccount(true)
			web.Priority = 1
			peer := webCandidateAccount(false)
			peer.ID, peer.Priority = 201, 10
			delete(peer.Credentials, "gemini_web")
			group := grp(740, PlatformGemini, 1, false)
			group.AllowMessagesDispatch = true
			resolver, _, key := globalCandidateFixture([]Group{group}, []Account{web, peer})
			_, state, err := resolver.PrepareCandidateRequest(context.Background(), key, tc.shape, tc.path, "gemini-3-flash", []byte(tc.body), "", "")
			require.NoError(t, err)
			require.Equal(t, peer.ID, state.current.account.ID, "exact capability outranks Web account priority")
			require.Empty(t, state.current.plan.Adjustment())
			resolver, _, key = globalCandidateFixture([]Group{group}, []Account{web})
			_, state, err = resolver.PrepareCandidateRequest(context.Background(), key, tc.shape, tc.path, "gemini-3-flash", []byte(tc.body), "", "")
			require.NoError(t, err)
			require.Equal(t, web.ID, state.current.account.ID)
			require.Equal(t, protocolrouter.GeminiWebBestEffortTokenLimit, state.current.plan.Adjustment())
		})
	}
}
