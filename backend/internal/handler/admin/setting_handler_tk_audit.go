package admin

import "github.com/Wei-Shaw/sub2api/internal/service"

// tkAppendTokenKeySettingsAuditDiff appends audit keys for TokenKey-specific settings fields.
func tkAppendTokenKeySettingsAuditDiff(changed []string, before, after *service.SystemSettings) []string {
	if before.NewAPIBridgeEnabled != after.NewAPIBridgeEnabled {
		changed = append(changed, "newapi_bridge_enabled")
	}
	return changed
}
