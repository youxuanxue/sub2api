//go:build unit

package newapi

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

func TestParseDeepSeekBalance(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantCNY   float64
		wantAvail bool
		wantErr   bool
	}{
		{
			name:      "healthy topped up",
			body:      `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"298.02","granted_balance":"0.00","topped_up_balance":"298.02"}]}`,
			wantCNY:   298.02,
			wantAvail: true,
		},
		{
			name:      "exhausted is_available false",
			body:      `{"is_available":false,"balance_infos":[{"currency":"CNY","total_balance":"0.00"}]}`,
			wantCNY:   0,
			wantAvail: false,
		},
		{
			name:      "picks CNY among multiple currencies",
			body:      `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"5.00"},{"currency":"CNY","total_balance":"42.50"}]}`,
			wantCNY:   42.50,
			wantAvail: true,
		},
		{
			name:    "no CNY entry is an error not zero",
			body:    `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"5.00"}]}`,
			wantErr: true,
		},
		{
			name:    "bad json",
			body:    `{not json`,
			wantErr: true,
		},
		{
			name:    "non-numeric total_balance",
			body:    `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"abc"}]}`,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDeepSeekBalance([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got result %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.AvailableCNY != tc.wantCNY {
				t.Errorf("AvailableCNY = %v, want %v", got.AvailableCNY, tc.wantCNY)
			}
			if got.IsAvailable != tc.wantAvail {
				t.Errorf("IsAvailable = %v, want %v", got.IsAvailable, tc.wantAvail)
			}
		})
	}
}

func TestBalanceProbeFor(t *testing.T) {
	if _, ok := BalanceProbeFor(newapiconstant.ChannelTypeDeepSeek); !ok {
		t.Errorf("expected DeepSeek (%d) to be registered", newapiconstant.ChannelTypeDeepSeek)
	}
	// An unrelated channel type must not resolve a probe — the sentinel skips it.
	if _, ok := BalanceProbeFor(0); ok {
		t.Errorf("channel_type 0 should not have a balance probe")
	}
}
