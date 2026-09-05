//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTkWireSettingServiceExtras_NilSafe(t *testing.T) {
	require.NotPanics(t, func() {
		tkWireSettingServiceExtras(nil, nil)
	})
}
