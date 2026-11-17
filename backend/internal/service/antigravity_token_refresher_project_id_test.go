package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFinalizeAntigravityRefreshCredentials_NeverHadProjectID(t *testing.T) {
	t.Parallel()

	account := &Account{
		ID:       1,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "old",
			"refresh_token": "old-ref",
		},
	}
	creds, err := finalizeAntigravityRefreshCredentials(account, &AntigravityTokenInfo{
		AccessToken:      "new",
		RefreshToken:     "new-ref",
		ProjectIDMissing: true,
	}, map[string]any{
		"access_token":  "new",
		"refresh_token": "new-ref",
	})
	require.ErrorIs(t, err, errAntigravityOAuthMissingProjectID)
	require.Equal(t, "new", creds["access_token"])
	require.True(t, isNonRetryableRefreshError(err))
	require.Contains(t, err.Error(), antigravityMissingProjectIDErrorPrefix)
}

func TestFinalizeAntigravityRefreshCredentials_RetainPriorProjectID(t *testing.T) {
	t.Parallel()

	account := &Account{
		ID:       2,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "old",
			"refresh_token": "old-ref",
			"project_id":    "kept-project",
		},
	}
	creds, err := finalizeAntigravityRefreshCredentials(account, &AntigravityTokenInfo{
		AccessToken:      "new",
		RefreshToken:     "new-ref",
		ProjectIDMissing: true,
	}, map[string]any{
		"access_token":  "new",
		"refresh_token": "new-ref",
	})
	require.NoError(t, err)
	require.Equal(t, "kept-project", creds["project_id"])
	require.True(t, AntigravityOAuthProjectIDReady(&Account{
		Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: creds,
	}))
}

func TestFinalizeAntigravityRefreshCredentials_FreshProjectID(t *testing.T) {
	t.Parallel()

	creds, err := finalizeAntigravityRefreshCredentials(&Account{
		Platform: PlatformAntigravity, Type: AccountTypeOAuth,
	}, &AntigravityTokenInfo{
		AccessToken: "new",
		ProjectID:   "new-project",
	}, map[string]any{
		"access_token": "new",
		"project_id":   "new-project",
	})
	require.NoError(t, err)
	require.Equal(t, "new-project", creds["project_id"])
	require.False(t, errors.Is(err, errAntigravityOAuthMissingProjectID))
}

func TestFinalizeAntigravityRefreshCredentials_FallbackCredentialSkipsQuarantine(t *testing.T) {
	t.Parallel()

	account := &Account{
		ID:       3,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "old",
			"refresh_token": "old-ref",
			antigravityProjectIDFallbackCredentialKey: "fallback-project",
		},
	}
	require.True(t, AntigravityOAuthProjectIDReady(account))

	creds, err := finalizeAntigravityRefreshCredentials(account, &AntigravityTokenInfo{
		AccessToken:      "new",
		RefreshToken:     "new-ref",
		ProjectIDMissing: true,
	}, map[string]any{
		"access_token":  "new",
		"refresh_token": "new-ref",
	})
	require.NoError(t, err)
	require.Equal(t, "fallback-project", creds[antigravityProjectIDFallbackCredentialKey])
	require.True(t, AntigravityOAuthProjectIDReady(&Account{
		Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: creds,
	}))
}

func TestFinalizeAntigravityRefreshCredentials_FallbackExtraSkipsQuarantine(t *testing.T) {
	t.Parallel()

	account := &Account{
		ID:       4,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "old",
			"refresh_token": "old-ref",
		},
		Extra: map[string]any{
			antigravityProjectIDFallbackCredentialKey: "extra-fallback",
		},
	}
	require.True(t, AntigravityOAuthProjectIDReady(account))

	creds, err := finalizeAntigravityRefreshCredentials(account, &AntigravityTokenInfo{
		AccessToken:      "new",
		RefreshToken:     "new-ref",
		ProjectIDMissing: true,
	}, map[string]any{
		"access_token":  "new",
		"refresh_token": "new-ref",
	})
	require.NoError(t, err)
	require.True(t, AntigravityOAuthProjectIDReady(&Account{
		Platform: PlatformAntigravity, Type: AccountTypeOAuth,
		Credentials: creds, Extra: account.Extra,
	}))
}
