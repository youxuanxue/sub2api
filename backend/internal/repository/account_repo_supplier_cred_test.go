package repository

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// supplierExclusiveChatCredentials mirrors service.supplierManagedCredentials for
// repository tests that cannot call the unexported helper: chat-only identity
// via api_base_urls + protocol_endpoints_exclusive (not supplier_source_id).
func supplierExclusiveChatCredentials(endpoint, apiKey string, mapping map[string]any) map[string]any {
	baseURL := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	return map[string]any{
		"base_url":      baseURL,
		"api_key":       strings.TrimSpace(apiKey),
		"model_mapping": mapping,
		"api_base_urls": map[string]any{
			service.APIProtocolChatCompletions: baseURL,
		},
		service.ProtocolEndpointsExclusiveCredentialKey: true,
	}
}

// supplierExclusiveMessagesCredentials mirrors Anthropic supplier messages-only
// exclusive credentials (channel_type=14 projection path).
func supplierExclusiveMessagesCredentials(endpoint, apiKey string, mapping map[string]any) map[string]any {
	baseURL := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	return map[string]any{
		"base_url":      baseURL,
		"api_key":       strings.TrimSpace(apiKey),
		"model_mapping": mapping,
		"api_base_urls": map[string]any{
			service.APIProtocolAnthropic: baseURL,
		},
		service.ProtocolEndpointsExclusiveCredentialKey: true,
	}
}
