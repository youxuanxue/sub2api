//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTkIsAnthropicAccountAuthFatal403(t *testing.T) {
	t.Run("account auth fatal", func(t *testing.T) {
		require.True(t, tkIsAnthropicAccountAuthFatal403(
			"Invalid bearer token",
			[]byte(`{"error":{"message":"Invalid bearer token","type":"permission_error"},"type":"error"}`),
		))
		require.True(t, tkIsAnthropicAccountAuthFatal403(
			"",
			[]byte(`{"type":"error","error":{"type":"permission_error","message":"OAuth token lacks required scopes"}}`),
		))
	})

	t.Run("failover only", func(t *testing.T) {
		require.False(t, tkIsAnthropicAccountAuthFatal403(
			"",
			[]byte(`{"type":"error","error":{"type":"permission_error","message":"you do not have access to this model"}}`),
		))
		require.False(t, tkIsAnthropicAccountAuthFatal403(
			"Upstream request failed",
			[]byte(`{"error":{"message":"Upstream request failed","type":"upstream_error"},"type":"error"}`),
		))
		require.False(t, tkIsAnthropicAccountAuthFatal403("", nil))
	})
}
