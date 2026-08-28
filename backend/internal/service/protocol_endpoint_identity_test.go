package service

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

func TestBuildProtocolEndpointIdentityNormalizesEquivalentConfigToOneKey(t *testing.T) {
	first := &Account{
		ID:          71,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		ChannelType: 0,
		Credentials: map[string]any{
			"api_key":  "first-secret",
			"base_url": "HTTPS://Relay.Example.Test:443/v1/",
			"model_mapping": map[string]any{
				"client-model": "upstream-model",
			},
			"header_override_enabled": true,
			"header_overrides": map[string]any{
				"X-Tenant": "alpha",
			},
		},
	}
	second := &Account{
		ID:          72,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		ChannelType: 0,
		Priority:    999,
		Credentials: map[string]any{
			"api_key":  "rotated-secret",
			"base_url": "https://relay.example.test/v1",
			"model_mapping": map[string]any{
				"different-client-model": "different-upstream-model",
			},
			"header_override_enabled": true,
			"header_overrides": map[string]any{
				"x-tenant": "alpha",
			},
		},
	}

	firstIdentity, governed, err := BuildProtocolEndpointIdentity(first)
	if err != nil {
		t.Fatalf("BuildProtocolEndpointIdentity(first): %v", err)
	}
	if !governed {
		t.Fatal("first account was not governed")
	}
	secondIdentity, governed, err := BuildProtocolEndpointIdentity(second)
	if err != nil {
		t.Fatalf("BuildProtocolEndpointIdentity(second): %v", err)
	}
	if !governed {
		t.Fatal("second account was not governed")
	}

	if got, want := firstIdentity.Key(), secondIdentity.Key(); got != want {
		t.Fatalf("equivalent endpoint keys differ: %q != %q", got, want)
	}
	canonical, err := firstIdentity.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	want := `{"key_schema_version":1,"platform":"openai","endpoint_profile":"custom_api_key","channel_type":"0","protocol_endpoints":{"chat_completions":{"url":"https://relay.example.test/v1/chat/completions","api_version":""},"messages":{"url":"https://relay.example.test/v1/messages","api_version":""},"responses":{"url":"https://relay.example.test/v1/responses","api_version":""}},"upstream_request_profile":"openai_json_v1","routing_headers":{"x-tenant":"alpha"}}`
	if string(canonical) != want {
		t.Fatalf("canonical identity = %s, want %s", canonical, want)
	}
	if strings.Contains(string(canonical), "secret") || strings.Contains(string(canonical), "model") {
		t.Fatalf("credential or model data leaked into identity: %s", canonical)
	}
}

func TestBuildProtocolEndpointIdentityExcludesQueryCredentialsButKeepsSemanticRouting(t *testing.T) {
	first := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "header-secret",
			"base_url": "https://relay.example.test/v1?deployment=blue&api_key=first-secret&" +
				"api-version=2026-08-27&access_token=first-token&key=first-key&sig=first-signature",
		},
	}
	second := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "rotated-header-secret",
			"base_url": "https://relay.example.test/v1?sig=rotated-signature&key=rotated-key&" +
				"access_token=rotated-token&api-version=2026-08-27&api_key=rotated-secret&deployment=blue",
		},
	}

	firstIdentity, governed, err := BuildProtocolEndpointIdentity(first)
	if err != nil || !governed {
		t.Fatalf("BuildProtocolEndpointIdentity(first) = governed %t, err %v", governed, err)
	}
	secondIdentity, governed, err := BuildProtocolEndpointIdentity(second)
	if err != nil || !governed {
		t.Fatalf("BuildProtocolEndpointIdentity(second) = governed %t, err %v", governed, err)
	}
	if firstIdentity.Key() != secondIdentity.Key() {
		t.Fatalf("query credential rotation changed capability key: %q != %q", firstIdentity.Key(), secondIdentity.Key())
	}
	for protocol, endpoint := range firstIdentity.ProtocolEndpoints {
		if strings.Contains(endpoint.URL, "secret") || strings.Contains(endpoint.URL, "token") || strings.Contains(endpoint.URL, "signature") || strings.Contains(endpoint.URL, "first-key") {
			t.Fatalf("%s endpoint leaked query credential: %s", protocol, endpoint.URL)
		}
		if !strings.Contains(endpoint.URL, "api-version=2026-08-27&deployment=blue") {
			t.Fatalf("%s endpoint dropped semantic query routing: %s", protocol, endpoint.URL)
		}
	}
}

func TestBuildProtocolEndpointIdentityChangesForRoutingFacts(t *testing.T) {
	base := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "secret",
			"base_url": "https://relay.example.test/v1",
		},
	}
	baseIdentity, governed, err := BuildProtocolEndpointIdentity(base)
	if err != nil || !governed {
		t.Fatalf("BuildProtocolEndpointIdentity(base) = governed %t, err %v", governed, err)
	}

	tests := []struct {
		name   string
		mutate func(*Account)
	}{
		{name: "base URL", mutate: func(account *Account) { account.Credentials["base_url"] = "https://other.example.test/v1" }},
		{name: "channel type", mutate: func(account *Account) { account.ChannelType = 17 }},
		{name: "routing header", mutate: func(account *Account) {
			account.Credentials["header_override_enabled"] = true
			account.Credentials["header_overrides"] = map[string]any{"x-tenant": "beta"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := *base
			changed.Credentials = cloneMap(base.Credentials)
			tt.mutate(&changed)
			identity, governed, err := BuildProtocolEndpointIdentity(&changed)
			if err != nil || !governed {
				t.Fatalf("BuildProtocolEndpointIdentity(changed) = governed %t, err %v", governed, err)
			}
			if identity.Key() == baseIdentity.Key() {
				t.Fatalf("%s change did not change capability key", tt.name)
			}
		})
	}
}

func TestBuildProtocolEndpointIdentityRejectsCredentialedURLAndSkipsUngovernedShape(t *testing.T) {
	credentialed := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://user:password@relay.example.test/v1",
		},
	}
	if _, _, err := BuildProtocolEndpointIdentity(credentialed); err == nil {
		t.Fatal("expected credentialed endpoint URL to be rejected")
	}

	ungoverned := &Account{
		Platform: PlatformAntigravity,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://arbitrary.example.test/v1",
		},
	}
	if _, governed, err := BuildProtocolEndpointIdentity(ungoverned); err != nil || governed {
		t.Fatalf("ungoverned shape = governed %t, err %v", governed, err)
	}
}

func TestAccountSupportedProtocolsReadsLinkedCapabilityOnly(t *testing.T) {
	account := &Account{
		Extra: map[string]any{
			SupportedProtocolsExtraKey: []any{"messages"},
		},
		ProtocolEndpointCapability: &ProtocolEndpointCapability{
			SupportedProtocols: []protocolrouter.Protocol{
				protocolrouter.ProtocolResponses,
				protocolrouter.ProtocolChatCompletions,
			},
		},
	}
	want := []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses}
	if got := account.SupportedProtocols(); !protocolListsEqual(got, want) {
		t.Fatalf("SupportedProtocols() = %v, want %v", got, want)
	}

	legacyOnly := &Account{Extra: map[string]any{SupportedProtocolsExtraKey: []any{"messages"}}}
	if got := legacyOnly.SupportedProtocols(); len(got) != 0 {
		t.Fatalf("legacy rollback projection became a runtime input: %v", got)
	}
}

func TestBuildProtocolEndpointCapabilityLinkInputKeepsFactsOnlyForSameIdentity(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "rotated-secret",
			"base_url": "https://relay.example.test/v1",
		},
	}
	identity, governed, err := BuildProtocolEndpointIdentity(account)
	if err != nil || !governed {
		t.Fatalf("BuildProtocolEndpointIdentity = governed %t, err %v", governed, err)
	}
	capabilityID := int64(9)
	account.ProtocolEndpointCapabilityID = &capabilityID
	account.ProtocolEndpointCapability = &ProtocolEndpointCapability{
		ID:                 capabilityID,
		CapabilityKey:      identity.Key(),
		SupportedProtocols: []protocolrouter.Protocol{protocolrouter.ProtocolResponses},
		Revision:           7,
	}

	input, err := BuildProtocolEndpointCapabilityLinkInput(account)
	if err != nil {
		t.Fatalf("BuildProtocolEndpointCapabilityLinkInput(same identity): %v", err)
	}
	if !input.Governed || input.Identity.Key() != identity.Key() {
		t.Fatalf("same identity input = %+v", input)
	}
	if got, want := input.SeedProtocols, []protocolrouter.Protocol{protocolrouter.ProtocolResponses}; !protocolListsEqual(got, want) {
		t.Fatalf("same identity seed protocols = %v, want %v", got, want)
	}

	account.Credentials["base_url"] = "https://other.example.test/v1"
	input, err = BuildProtocolEndpointCapabilityLinkInput(account)
	if err != nil {
		t.Fatalf("BuildProtocolEndpointCapabilityLinkInput(new identity): %v", err)
	}
	if input.Identity.Key() == identity.Key() {
		t.Fatal("identity-affecting edit reused the old capability key")
	}
	if len(input.SeedProtocols) != 0 {
		t.Fatalf("identity-affecting edit copied old endpoint facts: %v", input.SeedProtocols)
	}
}

func TestBuildProtocolEndpointCapabilityLinkInputSeedsOnlyOfficialProfile(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "secret",
		},
	}
	input, err := BuildProtocolEndpointCapabilityLinkInput(account)
	if err != nil {
		t.Fatalf("BuildProtocolEndpointCapabilityLinkInput: %v", err)
	}
	if !input.Governed || !input.OfficialSeed {
		t.Fatalf("official input = %+v", input)
	}
	if got, want := input.SeedProtocols, []protocolrouter.Protocol{protocolrouter.ProtocolResponses}; !protocolListsEqual(got, want) {
		t.Fatalf("official seed protocols = %v, want %v", got, want)
	}
}

func cloneMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
