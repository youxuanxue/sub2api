package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
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

// tkAdminComplianceGateTTL bounds how long a resolved gate flag is served from
// the per-instance cache before the next read re-checks the DB. The gate is an
// operator-driven, set-once toggle (default off), so a short TTL trades
// sub-second fleet-wide convergence — irrelevant for a compliance-ack switch —
// for eliminating a settings SELECT on EVERY admin request. Matches the 30s TTL
// of the sibling admin snapshot caches.
const tkAdminComplianceGateTTL = 30 * time.Second

type cachedTkComplianceGate struct {
	enabled   bool
	expiresAt int64 // UnixNano
}

// IsTkAdminComplianceGateEnabled reports whether the admin compliance
// acknowledgement gate should run. Defaults to false (TokenKey override of
// the upstream always-on default); only an explicit "true" enables it.
//
// The result is memoized per SettingService instance for tkAdminComplianceGateTTL
// so the gate middleware — which runs on every /api/v1/admin/* request — does not
// issue a settings SELECT per request just to discover the default-off flag.
func (s *SettingService) IsTkAdminComplianceGateEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	if cached, _ := s.tkAdminComplianceGateCache.Load().(*cachedTkComplianceGate); cached != nil && time.Now().UnixNano() < cached.expiresAt {
		return cached.enabled
	}
	// Refresh read-through under singleflight so a burst of concurrent admin
	// requests on a cold/stale cache collapses to ONE settings SELECT. The read
	// is synchronous (not background stale-while-revalidate) because the first
	// read after process start / expiry must return the live value — the gate's
	// unit tests assert a fresh service reports the current flag on first call.
	v, _, _ := s.tkAdminComplianceGateSF.Do(SettingKeyTkAdminComplianceGateEnabled, func() (any, error) {
		raw, err := s.settingRepo.GetValue(ctx, SettingKeyTkAdminComplianceGateEnabled)
		if err != nil {
			if errors.Is(err, ErrSettingNotFound) {
				// Definitive absence (the default-off case): cache false so the
				// common path stops hitting the DB on every request.
				s.tkAdminComplianceGateCache.Store(&cachedTkComplianceGate{enabled: false, expiresAt: time.Now().Add(tkAdminComplianceGateTTL).UnixNano()})
				return false, nil
			}
			// Transient error: do not cache; fail closed to disabled, matching the
			// prior behavior of returning false on any read error.
			return false, nil
		}
		enabled, _ := strconv.ParseBool(strings.TrimSpace(raw))
		s.tkAdminComplianceGateCache.Store(&cachedTkComplianceGate{enabled: enabled, expiresAt: time.Now().Add(tkAdminComplianceGateTTL).UnixNano()})
		return enabled, nil
	})
	enabled, _ := v.(bool)
	return enabled
}
