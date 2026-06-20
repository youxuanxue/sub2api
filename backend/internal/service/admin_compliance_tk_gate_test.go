package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// These cover the TokenKey default-off override of the upstream admin
// compliance gate (see admin_compliance_tk_gate.go). The regression they guard:
// the frontend dialog is driven solely by GetAdminComplianceStatus().Required,
// and that status endpoint is NOT wrapped by the middleware gate, so without the
// gate check in GetAdminComplianceStatus the dialog blocks the console even when
// the gate is disabled.

func TestAdminComplianceStatusNotRequiredWhenGateDisabledByDefault(t *testing.T) {
	// No tk_admin_compliance_gate_enabled setting and no acknowledgement: the
	// default-off gate must report the ack as not required so the dialog stays hidden.
	svc := NewSettingService(&adminComplianceRepoStub{}, &config.Config{})

	status, err := svc.GetAdminComplianceStatus(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, status.Required)
	require.Nil(t, status.Acknowledgement)
}

func TestAdminComplianceStatusNotRequiredWhenGateExplicitlyFalse(t *testing.T) {
	svc := NewSettingService(&adminComplianceRepoStub{
		values: map[string]string{SettingKeyTkAdminComplianceGateEnabled: "false"},
	}, &config.Config{})

	status, err := svc.GetAdminComplianceStatus(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, status.Required)
}

func TestAdminComplianceStatusRequiredWhenGateEnabledWithoutAck(t *testing.T) {
	svc := NewSettingService(&adminComplianceRepoStub{
		values: map[string]string{SettingKeyTkAdminComplianceGateEnabled: "true"},
	}, &config.Config{})

	status, err := svc.GetAdminComplianceStatus(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, status.Required)
}

func TestAdminComplianceStatusRespectsAckWhenGateEnabled(t *testing.T) {
	current, err := json.Marshal(AdminComplianceAcknowledgement{
		Version:     AdminComplianceVersion,
		AdminUserID: 1,
	})
	require.NoError(t, err)
	svc := NewSettingService(&adminComplianceRepoStub{
		values: map[string]string{
			SettingKeyTkAdminComplianceGateEnabled: "true",
			adminComplianceAcknowledgementKey(1):   string(current),
		},
	}, &config.Config{})

	status, err := svc.GetAdminComplianceStatus(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, status.Required)
	require.NotNil(t, status.Acknowledgement)
}

// countingGateRepoStub counts GetValue calls so the cache-behavior tests can
// assert the gate flag is read from the DB once and served from the per-instance
// cache thereafter.
type countingGateRepoStub struct {
	*adminComplianceRepoStub
	getValueCalls int
}

func (r *countingGateRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	r.getValueCalls++
	return r.adminComplianceRepoStub.GetValue(ctx, key)
}

func TestIsTkAdminComplianceGateEnabledCachesEnabledRead(t *testing.T) {
	repo := &countingGateRepoStub{adminComplianceRepoStub: &adminComplianceRepoStub{
		values: map[string]string{SettingKeyTkAdminComplianceGateEnabled: "true"},
	}}
	svc := NewSettingService(repo, &config.Config{})

	for i := 0; i < 5; i++ {
		require.True(t, svc.IsTkAdminComplianceGateEnabled(context.Background()))
	}
	require.Equal(t, 1, repo.getValueCalls, "enabled gate flag should hit the DB once, then serve from cache")
}

func TestIsTkAdminComplianceGateEnabledCachesAbsentAsFalse(t *testing.T) {
	// The default-off case (no setting row) is the common path: it must also be
	// cached so the gate middleware does not re-query the DB on every admin request.
	repo := &countingGateRepoStub{adminComplianceRepoStub: &adminComplianceRepoStub{values: map[string]string{}}}
	svc := NewSettingService(repo, &config.Config{})

	for i := 0; i < 5; i++ {
		require.False(t, svc.IsTkAdminComplianceGateEnabled(context.Background()))
	}
	require.Equal(t, 1, repo.getValueCalls, "absent (default-off) gate flag should hit the DB once, then serve from cache")
}

func TestIsAdminComplianceAcknowledgedTrueWhenGateDisabled(t *testing.T) {
	// Defense in depth: even though the middleware guard is skipped entirely when
	// the gate is off, the acknowledgement predicate must not report a missing ack
	// as un-acknowledged for a disabled gate.
	svc := NewSettingService(&adminComplianceRepoStub{}, &config.Config{})

	ok, err := svc.IsAdminComplianceAcknowledged(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, ok)
}
