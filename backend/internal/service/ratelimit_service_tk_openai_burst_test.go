//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTkIsOpenAI429TransientBurst pins the TK policy predicate for upstream
// #2258. The matrix covers the four possible exhausted/with-reset crossings
// per window so a future change to the predicate that drops the "reset value
// must also be present" requirement is caught immediately.
func TestTkIsOpenAI429TransientBurst(t *testing.T) {
	t.Parallel()

	pct := func(v float64) *float64 { return &v }
	secs := func(v int) *int { return &v }

	cases := []struct {
		name string
		in   *NormalizedCodexLimits
		want bool
	}{
		{"nil snapshot", nil, false},
		{
			"both windows under 100 — burst",
			&NormalizedCodexLimits{
				Used5hPercent: pct(80), Reset5hSeconds: secs(5000),
				Used7dPercent: pct(90), Reset7dSeconds: secs(100000),
			},
			true,
		},
		{
			"7d exhausted with reset — NOT burst",
			&NormalizedCodexLimits{
				Used5hPercent: pct(3), Reset5hSeconds: secs(17369),
				Used7dPercent: pct(100), Reset7dSeconds: secs(384607),
			},
			false,
		},
		{
			"5h exhausted with reset — NOT burst",
			&NormalizedCodexLimits{
				Used5hPercent: pct(100), Reset5hSeconds: secs(3600),
				Used7dPercent: pct(50), Reset7dSeconds: secs(500000),
			},
			false,
		},
		{
			"5h reports 100% but no reset value — burst (cannot use missing reset)",
			&NormalizedCodexLimits{
				Used5hPercent: pct(100), Reset5hSeconds: nil,
				Used7dPercent: pct(50), Reset7dSeconds: secs(500000),
			},
			true,
		},
		{
			"both nil — burst (no signal)",
			&NormalizedCodexLimits{},
			true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, TkIsOpenAI429TransientBurst(tc.in))
		})
	}
}

// TestTkRecordOpenAI429BurstFallthrough_HandlesNil only asserts the slog
// helper does not panic on nil. Slog output assertions live in the broader
// rate_limit_429_cooldown_test.go suite which exercises the full handle429
// path.
func TestTkRecordOpenAI429BurstFallthrough_HandlesNil(t *testing.T) {
	t.Parallel()
	require.NotPanics(t, func() { TkRecordOpenAI429BurstFallthrough(nil) })
	require.NotPanics(t, func() { TkRecordOpenAI429BurstFallthrough(&NormalizedCodexLimits{}) })
}
