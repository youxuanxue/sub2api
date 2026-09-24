package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestProtocolEndpointIdentityURLReusePreservesPathsAndFreshness(t *testing.T) {
	protocols := []protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses}
	keys := []string{APIProtocolAnthropic, APIProtocolChatCompletions, APIProtocolResponses}
	urls := []string{
		"HTTPS://Relay.Example.Test:443/v1/?deployment=blue&api_key=fixture",
		"http://relay.example.test:80/v2/chat/completions?z=2&a=1",
		"https://relay.example.test/proxy/%61/v1?deployment=red",
	}
	// Cover one, two and three distinct bases, including non-adjacent reuse.
	for _, indices := range [][3]int{{0, 0, 0}, {0, 1, 0}, {0, 1, 2}} {
		t.Run(strings.Join([]string{urls[indices[0]], urls[indices[1]], urls[indices[2]]}, "|"), func(t *testing.T) {
			account := endpointIdentityAccountFixture("shared")
			bases := map[string]any{}
			versions := map[string]any{}
			for i, protocol := range protocols {
				bases[keys[i]] = urls[indices[i]]
				versions[string(protocol)] = string(protocol) + "-version"
			}
			account.Credentials["api_base_urls"] = bases
			account.Credentials["api_versions"] = versions
			before, err := json.Marshal(account.Credentials)
			require.NoError(t, err)
			identity, governed, err := BuildProtocolEndpointIdentity(account)
			require.NoError(t, err)
			require.True(t, governed)
			for i, protocol := range protocols {
				want, err := normalizeProtocolEndpointURL(urls[indices[i]], protocol)
				require.NoError(t, err)
				require.Equal(t, ProtocolEndpoint{URL: want, APIVersion: string(protocol) + "-version"}, identity.ProtocolEndpoints[protocol])
			}
			after, err := json.Marshal(account.Credentials)
			require.NoError(t, err)
			require.Equal(t, before, after)
			oldKey := identity.Key()
			bases[APIProtocolResponses] = "https://changed.example.test/v1"
			fresh, _, err := BuildProtocolEndpointIdentity(account)
			require.NoError(t, err)
			require.NotEqual(t, oldKey, fresh.Key())
			require.Equal(t, "https://changed.example.test/v1/responses", fresh.ProtocolEndpoints[protocolrouter.ProtocolResponses].URL)
			require.Equal(t, identity.ProtocolEndpoints[protocolrouter.ProtocolMessages], fresh.ProtocolEndpoints[protocolrouter.ProtocolMessages])
		})
	}
}

func TestProtocolEndpointIdentityReuseKeepsURLRejections(t *testing.T) {
	for _, raw := range []string{"ftp://relay.example.test", "https://user:password@relay.example.test", "https://relay.example.test/#fragment", "https://relay.example.test/%zz", "https:///missing-host"} {
		t.Run(raw, func(t *testing.T) {
			account := endpointIdentityAccountFixture("shared")
			account.Credentials["base_url"] = raw
			_, want := normalizeProtocolEndpointURL(raw, protocolrouter.ProtocolMessages)
			require.Error(t, want)
			identity, governed, err := BuildProtocolEndpointIdentity(account)
			require.True(t, governed)
			require.EqualError(t, err, "normalize messages endpoint identity: "+want.Error())
			require.Equal(t, ProtocolEndpointIdentity{}, identity)
		})
	}
}

func TestProtocolEndpointIdentityValidationKeepsCanonicalEncoding(t *testing.T) {
	identity, _, err := BuildProtocolEndpointIdentity(endpointIdentityAccountFixture("shared"))
	require.NoError(t, err)
	// JSON's string replacement/escaping behavior remains owned by json.Marshal.
	identity.RoutingHeaders = map[string]string{"x-tenant": "<tenant>\u2028\xff"}
	canonical, err := identity.CanonicalJSON()
	require.NoError(t, err)
	want, err := json.Marshal(identity)
	require.NoError(t, err)
	require.Equal(t, want, canonical)
	for _, tc := range []struct {
		name    string
		mutate  func(*ProtocolEndpointIdentity)
		message string
	}{
		{"schema", func(i *ProtocolEndpointIdentity) { i.KeySchemaVersion = 0 }, "unsupported protocol endpoint identity schema version 0"},
		{"platform", func(i *ProtocolEndpointIdentity) { i.Platform = " " }, "protocol endpoint identity is incomplete"},
		{"profile", func(i *ProtocolEndpointIdentity) { i.EndpointProfile = "" }, "protocol endpoint identity is incomplete"},
		{"request profile", func(i *ProtocolEndpointIdentity) { i.UpstreamRequestProfile = "" }, "protocol endpoint identity is incomplete"},
		{"endpoints", func(i *ProtocolEndpointIdentity) { i.ProtocolEndpoints = nil }, "protocol endpoint identity requires at least one endpoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := identity
			tc.mutate(&invalid)
			raw, err := invalid.CanonicalJSON()
			require.EqualError(t, err, tc.message)
			require.Nil(t, raw)
			require.Empty(t, invalid.Key())
		})
	}
}

// Test adapter for the standalone pre-refactor contract.
func normalizeProtocolEndpointURL(raw string, protocol protocolrouter.Protocol) (string, error) {
	parsed, err := normalizeEndpointIdentityURL(raw)
	if err != nil {
		return "", err
	}
	return protocolEndpointURL(*parsed, protocol)
}
