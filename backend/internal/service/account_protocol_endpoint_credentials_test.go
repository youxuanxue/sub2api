package service

import (
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func TestMergeAccountCredentials_CRSPreserveAllDropsStaleExclusive(t *testing.T) {
	existing := supplierManagedCredentials(
		"https://supplier.example/v1", "supplier-secret",
		map[string]string{"deepseek-v4-pro": "deepseek-v4-pro"},
		newapiconstant.ChannelTypeOpenAI,
	)
	incoming := map[string]any{
		"base_url": "https://crs.example",
		"api_key":  "crs-secret",
	}

	merged := MergeAccountCredentials(existing, incoming, newapiconstant.ChannelTypeOpenAI, CredentialMergePreserveAll)

	require.Equal(t, "https://crs.example", merged["base_url"])
	require.Equal(t, "crs-secret", merged["api_key"])
	mapping, ok := merged["model_mapping"].(map[string]any)
	require.True(t, ok, "CRS preserve-all must keep model_mapping")
	require.Equal(t, "deepseek-v4-pro", mapping["deepseek-v4-pro"],
		"CRS preserve-all must keep non-identity keys such as model_mapping")
	require.NotContains(t, merged, apiBaseURLsCredentialKey)
	require.NotContains(t, merged, ProtocolEndpointsExclusiveCredentialKey)

	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: merged}
	baseURL, baseURLs, _ := protocolAccountEndpoints(account)
	require.Equal(t, "https://crs.example", baseURL)
	require.Nil(t, baseURLs, "without exclusive, identity must fan from the new base_url only")
}

func TestMergeAccountCredentials_AdminRealignsExclusiveWhenIncomingSpreadsStaleURLs(t *testing.T) {
	existing := supplierManagedCredentials(
		"https://supplier.example/v1", "supplier-secret",
		map[string]string{"m": "m"},
		newapiconstant.ChannelTypeOpenAI,
	)
	// Frontend NewAPI edit spreads current credentials then overwrites base_url/api_key.
	incoming := map[string]any{}
	for key, value := range existing {
		incoming[key] = value
	}
	incoming["base_url"] = "https://relay.example/v1"
	incoming["api_key"] = "rotated-secret"

	merged := MergeAccountCredentials(existing, incoming, newapiconstant.ChannelTypeOpenAI, CredentialMergeAdmin)

	require.Equal(t, "https://relay.example/v1", merged["base_url"])
	require.Equal(t, "rotated-secret", merged["api_key"])
	require.Equal(t, true, merged[ProtocolEndpointsExclusiveCredentialKey])
	require.Equal(t, map[string]any{
		APIProtocolChatCompletions: "https://relay.example/v1",
	}, merged[apiBaseURLsCredentialKey])

	account := &Account{
		Platform: PlatformNewAPI, Type: AccountTypeAPIKey, Credentials: merged,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
	}
	_, baseURLs, _ := protocolAccountEndpoints(account)
	require.Equal(t, map[protocolrouter.Protocol]string{
		protocolrouter.ProtocolChatCompletions: "https://relay.example/v1",
	}, baseURLs)
}

func TestFinalizeAccountCredentials_RealignsQianfanExclusiveChatPath(t *testing.T) {
	credentials := map[string]any{
		"base_url": newapiintegration.QianfanBaseURL,
		"api_key":  "qf-key",
		apiBaseURLsCredentialKey: map[string]any{
			APIProtocolChatCompletions: "https://stale.example/v2/chat/completions",
		},
		ProtocolEndpointsExclusiveCredentialKey: true,
	}

	got := FinalizeAccountCredentials(credentials, newapiconstant.ChannelTypeBaiduV2)
	wantChat := strings.TrimRight(newapiintegration.QianfanBaseURL, "/") + "/v2/chat/completions"
	require.Equal(t, map[string]any{
		APIProtocolChatCompletions: wantChat,
	}, got[apiBaseURLsCredentialKey])
}

func TestPrepareBulkCredentialPatch_ClearsIdentityWhenRotatingBaseURL(t *testing.T) {
	patch := PrepareBulkCredentialPatch(map[string]any{
		"base_url": "https://bulk.example/v1",
		"api_key":  "bulk-secret",
	})
	require.Equal(t, "https://bulk.example/v1", patch["base_url"])
	require.Nil(t, patch[apiBaseURLsCredentialKey])
	require.Nil(t, patch[ProtocolEndpointsExclusiveCredentialKey])
}

func TestPrepareBulkCredentialPatch_LeavesUnrelatedPatchesAlone(t *testing.T) {
	patch := PrepareBulkCredentialPatch(map[string]any{
		"model_mapping": map[string]any{"a": "a"},
	})
	require.Equal(t, map[string]any{"a": "a"}, patch["model_mapping"])
	require.NotContains(t, patch, apiBaseURLsCredentialKey)
}

func TestSupplierManagedCredentials_UsesSharedExclusiveChatSSOT(t *testing.T) {
	got := supplierManagedCredentials(
		"https://supplier.example/v1/", "secret",
		map[string]string{"m": "m"},
		newapiconstant.ChannelTypeOpenAI,
	)
	require.Equal(t, "https://supplier.example/v1", got["base_url"])
	require.Equal(t, true, got[ProtocolEndpointsExclusiveCredentialKey])
	require.Equal(t, map[string]any{
		APIProtocolChatCompletions: "https://supplier.example/v1",
	}, got[apiBaseURLsCredentialKey])
}
