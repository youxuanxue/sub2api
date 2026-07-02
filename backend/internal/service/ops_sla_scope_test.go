package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsOpsSLAFaultOwner(t *testing.T) {
	require.True(t, IsOpsSLAFaultOwner(OpsErrorOwnerProvider))
	require.True(t, IsOpsSLAFaultOwner(OpsErrorOwnerPlatform))
	require.False(t, IsOpsSLAFaultOwner(OpsErrorOwnerClient))
	require.False(t, IsOpsSLAFaultOwner(""))
}

func TestComputeSLAMetrics_clientFaultsInDenominatorOnly(t *testing.T) {
	sla, errRate := ComputeSLAMetrics(800, 200, 50)
	require.InDelta(t, 0.95, sla, 1e-9)
	require.InDelta(t, 0.05, errRate, 1e-9)
}

func TestComputeSLAMetrics_zeroTraffic(t *testing.T) {
	sla, errRate := ComputeSLAMetrics(0, 0, 0)
	require.Equal(t, 0.0, sla)
	require.Equal(t, 0.0, errRate)
}
