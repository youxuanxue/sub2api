package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestManagedBatchEnabledDefaultsTrue(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults()
	require.True(t, viper.GetBool("managed_batch.enabled"),
		"managed Batch API surface defaults on; workers still require storage wiring")
}
