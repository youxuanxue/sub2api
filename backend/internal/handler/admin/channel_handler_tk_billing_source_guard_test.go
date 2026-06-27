//go:build unit

package admin

import (
	"errors"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// TestTkRequireBillingModelSourceConfirm pins the B1 guardrail
// (docs/approved/priced-or-it-doesnt-ship.md 复审 B1): switching a channel's
// billing_model_source to a risky value (requested/upstream) — which can reopen
// the $0-leak by charging on a key the priced-serving gate let through under a
// different key — MUST require explicit human confirmation. The safe default
// (channel_mapped) and the omitted/empty case (Update keeps existing value) pass
// freely with no confirmation.
func TestTkRequireBillingModelSourceConfirm(t *testing.T) {
	cases := []struct {
		name      string
		source    string
		confirmed bool
		wantErr   bool
	}{
		{"empty/omitted passes (Update keeps existing)", "", false, false},
		{"channel_mapped (safe default) passes", service.BillingModelSourceChannelMapped, false, false},
		{"channel_mapped with confirm still passes", service.BillingModelSourceChannelMapped, true, false},
		{"requested without confirm is BLOCKED", service.BillingModelSourceRequested, false, true},
		{"requested WITH confirm passes", service.BillingModelSourceRequested, true, false},
		{"upstream without confirm is BLOCKED", service.BillingModelSourceUpstream, false, true},
		{"upstream WITH confirm passes", service.BillingModelSourceUpstream, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tkRequireBillingModelSourceConfirm(tc.source, tc.confirmed)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var appErr *infraerrors.ApplicationError
			require.True(t, errors.As(err, &appErr), "must be an ApplicationError")
			require.Equal(t, "BILLING_MODEL_SOURCE_CONFIRM_REQUIRED", appErr.Reason)
			require.EqualValues(t, 400, appErr.Code, "must be a 400 (client must re-submit with confirm)")
		})
	}
}
