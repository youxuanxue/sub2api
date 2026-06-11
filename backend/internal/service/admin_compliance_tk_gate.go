package service

import (
	"context"
	"strconv"
	"strings"
)

// TokenKey: admin compliance gate default-off override (CLAUDE.md §5.x option 2).
//
// Upstream ships AdminComplianceGuard always-on: every /api/v1/admin/* request
// returns 423 until each admin user acknowledges the compliance document via
// the frontend dialog. TokenKey's fleet (prod + edge Stage0 nodes) drives
// admin APIs through automation — admin_api_key over x-api-key, forged
// short-lived JWTs on edges, SSM ops scripts — with no interactive ack step,
// so the gate is disabled unless an operator explicitly opts a node in by
// setting tk_admin_compliance_gate_enabled=true. The upstream feature stays
// compiled in and fully functional once enabled.

// SettingKeyTkAdminComplianceGateEnabled toggles the admin compliance
// acknowledgement gate. Absent/empty/non-"true" values mean disabled.
const SettingKeyTkAdminComplianceGateEnabled = "tk_admin_compliance_gate_enabled"

// IsTkAdminComplianceGateEnabled reports whether the admin compliance
// acknowledgement gate should run. Defaults to false (TokenKey override of
// the upstream always-on default); only an explicit "true" enables it.
func (s *SettingService) IsTkAdminComplianceGateEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyTkAdminComplianceGateEnabled)
	if err != nil {
		return false
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(raw))
	return err == nil && enabled
}
