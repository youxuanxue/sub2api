package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

func endpointIdentityAccountFixture(kind string) *Account {
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"base_url":      "HTTPS://Relay.Example.Test:443/v1/?deployment=blue&api_key=fixture&api-version=2026-08-27",
		"model_mapping": map[string]any{"client-model": "upstream-model"},
	}}
	switch kind {
	case "distinct":
		account.Credentials["api_base_urls"] = map[string]any{
			APIProtocolAnthropic:       "https://messages.example.test/v1",
			APIProtocolChatCompletions: "https://chat.example.test/v1",
			APIProtocolResponses:       "https://responses.example.test/v1",
		}
	case "official":
		account.Type = AccountTypeOAuth
	}
	return account
}

func BenchmarkProtocolEndpointIdentity(b *testing.B) {
	for _, kind := range []string{"shared", "distinct", "official"} {
		b.Run(kind, func(b *testing.B) {
			account := endpointIdentityAccountFixture(kind)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				identity, governed, err := BuildProtocolEndpointIdentity(account)
				if err != nil || !governed || identity.Key() == "" {
					b.Fatalf("invalid identity: %v", err)
				}
			}
		})
	}
}

func BenchmarkProtocolEndpointAccountSnapshot(b *testing.B) {
	for _, kind := range []string{"shared", "distinct"} {
		b.Run(kind, func(b *testing.B) {
			account := endpointIdentityAccountFixture(kind)
			attachTestProtocolCapability(account, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				snapshot, err := ProtocolAccountSnapshot(account, "client-model")
				if err != nil || snapshot.ResolvedModel() != "upstream-model" {
					b.Fatalf("invalid snapshot: %v", err)
				}
			}
		})
	}
}
