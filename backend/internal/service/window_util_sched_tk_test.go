//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckWindowUtilSchedulability_Default098002(t *testing.T) {
	const (
		threshold = windowUtilStickyThresholdDefault
		reserve   = windowUtilStickyReserveDefault
	)
	cases := []struct {
		name string
		util float64
		want WindowUtilSchedulability
	}{
		{"well below", 0.50, WindowUtilSchedulable},
		{"just below threshold", 0.979, WindowUtilSchedulable},
		{"at threshold => sticky-only", 0.98, WindowUtilStickyOnly},
		{"inside reserve band", 0.99, WindowUtilStickyOnly},
		{"at hard edge => not schedulable", 1.0, WindowUtilNotSchedulable},
		{"above hard edge", 1.001, WindowUtilNotSchedulable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, checkWindowUtilSchedulability(tc.util, threshold, reserve))
		})
	}
}

func TestCheckWindowUtilSchedulability_DisabledThreshold(t *testing.T) {
	require.Equal(t, WindowUtilSchedulable, checkWindowUtilSchedulability(0.99, 0, 0.02))
	require.Equal(t, WindowUtilSchedulable, checkWindowUtilSchedulability(0.99, 1.0, 0.02))
}
