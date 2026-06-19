package admin

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// tkValidateKiroAccountCreate returns a user-facing error message, or empty if OK.
//
// It enforces the Kiro (sixth platform) account-creation contract:
//   - access_token, refresh_token, region are required
//   - auth_method must be one of {"social", "idc"}
//   - when auth_method=="idc", client_id and client_secret are required
//   - credentials.tos_acknowledged must be true (string "true" or bool true) —
//     the ToS acknowledgement gate for creating a Kiro account.
//
// Non-kiro platforms pass through unchanged.
func tkValidateKiroAccountCreate(platform string, credentials map[string]any) string {
	if platform != domain.PlatformKiro {
		return ""
	}

	if !tkKiroCredTrue(credentials, "tos_acknowledged") {
		return "Kiro ToS must be acknowledged (credentials.tos_acknowledged=true) before creating a Kiro account"
	}

	if tkKiroCredString(credentials, "access_token") == "" {
		return "credentials.access_token is required for kiro platform"
	}
	if tkKiroCredString(credentials, "refresh_token") == "" {
		return "credentials.refresh_token is required for kiro platform"
	}
	if tkKiroCredString(credentials, "region") == "" {
		return "credentials.region is required for kiro platform"
	}

	authMethod := tkKiroCredString(credentials, "auth_method")
	switch authMethod {
	case "social":
		// no extra fields required
	case "idc":
		if tkKiroCredString(credentials, "client_id") == "" {
			return "credentials.client_id is required for kiro idc auth_method"
		}
		if tkKiroCredString(credentials, "client_secret") == "" {
			return "credentials.client_secret is required for kiro idc auth_method"
		}
	default:
		return `credentials.auth_method must be one of "social" or "idc" for kiro platform`
	}

	return ""
}

// tkKiroCredString reads a trimmed string credential, tolerating nil maps.
func tkKiroCredString(credentials map[string]any, key string) string {
	if credentials == nil {
		return ""
	}
	if v, ok := credentials[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// tkKiroCredTrue reports whether a credential equals true, accepting either a
// bool true or the string "true".
func tkKiroCredTrue(credentials map[string]any, key string) bool {
	if credentials == nil {
		return false
	}
	switch v := credentials[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}
