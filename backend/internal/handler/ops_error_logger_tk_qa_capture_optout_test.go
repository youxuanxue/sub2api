package handler

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/stretchr/testify/require"
)

func TestMarkOpsErrorLogUserOptOut(t *testing.T) {
	optedOut := &service.APIKey{User: &service.User{ID: 7, QACaptureEnabled: false}}
	capturing := &service.APIKey{User: &service.User{ID: 8, QACaptureEnabled: true}}

	for _, tc := range []struct {
		name   string
		apiKey *service.APIKey
		want   bool
	}{
		{"opted out user is suppressed", optedOut, true},
		{"capturing user is kept", capturing, false},
		// No resolvable user means there is no opt-out to honor; dropping these
		// would blind operators to platform-wide failures and junk traffic.
		{"nil key is kept", nil, false},
		{"key without user is kept", &service.APIKey{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := &service.OpsInsertErrorLogInput{ErrorType: "api_error"}
			markOpsErrorLogUserOptOut(entry, tc.apiKey)
			require.Equal(t, tc.want, entry.SuppressForUserOptOut)
		})
	}
}

// TestMarkOpsErrorLogUserOptOutNeverUnsets guards the one-way contract: the
// stamp is applied at several sites, and a later call for an unattributable
// key must not clear a suppression an earlier call established.
func TestMarkOpsErrorLogUserOptOutNeverUnsets(t *testing.T) {
	entry := &service.OpsInsertErrorLogInput{SuppressForUserOptOut: true}
	markOpsErrorLogUserOptOut(entry, &service.APIKey{User: &service.User{ID: 9, QACaptureEnabled: true}})
	require.True(t, entry.SuppressForUserOptOut, "stamp must not be cleared by a later capturing-user call")

	markOpsErrorLogUserOptOut(entry, nil)
	require.True(t, entry.SuppressForUserOptOut, "stamp must not be cleared by an unattributable call")
}

// TestOpsCoalescedSummaryCarriesUserOptOut covers the leak the coalescer would
// otherwise reopen: the summary outlives the request that produced it, so a
// field-by-field rebuild that drops the stamp would write a suppressed user's
// collapsed tail after the fact.
func TestOpsCoalescedSummaryCarriesUserOptOut(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	suppressed := opsCoalescedSummary(&service.OpsInsertErrorLogInput{
		ErrorType:             "api_error",
		SuppressForUserOptOut: true,
	}, now)
	require.True(t, suppressed.SuppressForUserOptOut, "summary must carry the opt-out")

	kept := opsCoalescedSummary(&service.OpsInsertErrorLogInput{
		ErrorType: "api_error",
	}, now)
	require.False(t, kept.SuppressForUserOptOut)
}

// TestOpsErrorLogOptOutZeroValueHazard documents why suppression keys off an
// explicitly-loaded flag and why fixtures must set it.
//
// service.User.QACaptureEnabled is a bare bool, so its Go zero value (false)
// means "opted out" — the inverse of the ent schema default
// (users.qa_capture_enabled = true). Production is safe because the auth path
// loads the column (api_key_repo selects user.FieldQaCaptureEnabled), but any
// hand-built User defaults to suppressed. This test pins the asymmetry so the
// next reader does not "simplify" the predicate into trusting a zero value,
// and testutil.NewTestUser sets the field for the same reason.
func TestOpsErrorLogOptOutZeroValueHazard(t *testing.T) {
	zeroValueUser := &service.APIKey{User: &service.User{ID: 1}}
	entry := &service.OpsInsertErrorLogInput{}
	markOpsErrorLogUserOptOut(entry, zeroValueUser)
	require.True(t, entry.SuppressForUserOptOut,
		"a zero-value User reads as opted out; production must load qa_capture_enabled, and fixtures must set it")

	loadedUser := &service.APIKey{User: &service.User{ID: 1, QACaptureEnabled: true}}
	loadedEntry := &service.OpsInsertErrorLogInput{}
	markOpsErrorLogUserOptOut(loadedEntry, loadedUser)
	require.False(t, loadedEntry.SuppressForUserOptOut)
}
