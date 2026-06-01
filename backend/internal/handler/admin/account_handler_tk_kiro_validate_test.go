//go:build unit

package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestTkValidateKiroAccountCreate(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"tos_acknowledged": true,
			"access_token":     "at",
			"refresh_token":    "rt",
			"region":           "us-east-1",
			"auth_method":      "social",
		}
	}

	t.Run("non-kiro platform passes through", func(t *testing.T) {
		require.Empty(t, tkValidateKiroAccountCreate(domain.PlatformAnthropic, nil))
		require.Empty(t, tkValidateKiroAccountCreate(domain.PlatformNewAPI, map[string]any{}))
	})

	t.Run("valid social account", func(t *testing.T) {
		require.Empty(t, tkValidateKiroAccountCreate(domain.PlatformKiro, base()))
	})

	t.Run("valid idc account", func(t *testing.T) {
		creds := base()
		creds["auth_method"] = "idc"
		creds["client_id"] = "cid"
		creds["client_secret"] = "csecret"
		require.Empty(t, tkValidateKiroAccountCreate(domain.PlatformKiro, creds))
	})

	t.Run("tos string true accepted", func(t *testing.T) {
		creds := base()
		creds["tos_acknowledged"] = "true"
		require.Empty(t, tkValidateKiroAccountCreate(domain.PlatformKiro, creds))
		creds["tos_acknowledged"] = "TRUE"
		require.Empty(t, tkValidateKiroAccountCreate(domain.PlatformKiro, creds))
	})

	t.Run("missing tos rejected", func(t *testing.T) {
		creds := base()
		delete(creds, "tos_acknowledged")
		require.Contains(t, tkValidateKiroAccountCreate(domain.PlatformKiro, creds), "ToS must be acknowledged")
	})

	t.Run("tos false rejected", func(t *testing.T) {
		creds := base()
		creds["tos_acknowledged"] = false
		require.Contains(t, tkValidateKiroAccountCreate(domain.PlatformKiro, creds), "ToS must be acknowledged")
		creds["tos_acknowledged"] = "false"
		require.Contains(t, tkValidateKiroAccountCreate(domain.PlatformKiro, creds), "ToS must be acknowledged")
	})

	t.Run("missing access_token rejected", func(t *testing.T) {
		creds := base()
		delete(creds, "access_token")
		require.Contains(t, tkValidateKiroAccountCreate(domain.PlatformKiro, creds), "access_token is required")
	})

	t.Run("missing refresh_token rejected", func(t *testing.T) {
		creds := base()
		delete(creds, "refresh_token")
		require.Contains(t, tkValidateKiroAccountCreate(domain.PlatformKiro, creds), "refresh_token is required")
	})

	t.Run("missing region rejected", func(t *testing.T) {
		creds := base()
		delete(creds, "region")
		require.Contains(t, tkValidateKiroAccountCreate(domain.PlatformKiro, creds), "region is required")
	})

	t.Run("invalid auth_method rejected", func(t *testing.T) {
		creds := base()
		creds["auth_method"] = "oauth"
		require.Contains(t, tkValidateKiroAccountCreate(domain.PlatformKiro, creds), "auth_method")
	})

	t.Run("missing auth_method rejected", func(t *testing.T) {
		creds := base()
		delete(creds, "auth_method")
		require.Contains(t, tkValidateKiroAccountCreate(domain.PlatformKiro, creds), "auth_method")
	})

	t.Run("idc without client_id rejected", func(t *testing.T) {
		creds := base()
		creds["auth_method"] = "idc"
		creds["client_secret"] = "csecret"
		require.Contains(t, tkValidateKiroAccountCreate(domain.PlatformKiro, creds), "client_id is required")
	})

	t.Run("idc without client_secret rejected", func(t *testing.T) {
		creds := base()
		creds["auth_method"] = "idc"
		creds["client_id"] = "cid"
		require.Contains(t, tkValidateKiroAccountCreate(domain.PlatformKiro, creds), "client_secret is required")
	})
}
