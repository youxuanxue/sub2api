//go:build unit

package service

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKiroTokenRefresher_CanRefresh(t *testing.T) {
	r := NewKiroTokenRefresher()

	cases := []struct {
		name    string
		account *Account
		want    bool
	}{
		{"kiro oauth", &Account{Platform: PlatformKiro, Type: AccountTypeOAuth}, true},
		{"kiro non-oauth", &Account{Platform: PlatformKiro, Type: AccountTypeAPIKey}, false},
		{"anthropic oauth", &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, false},
		{"openai oauth", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, r.CanRefresh(tc.account))
		})
	}
}

func TestKiroTokenRefresher_NeedsRefresh(t *testing.T) {
	r := NewKiroTokenRefresher()
	window := time.Hour

	t.Run("no expires_at -> false", func(t *testing.T) {
		acct := &Account{Platform: PlatformKiro, Type: AccountTypeOAuth, Credentials: map[string]any{}}
		require.False(t, r.NeedsRefresh(acct, window))
	})

	t.Run("inside window -> true (unix seconds)", func(t *testing.T) {
		exp := time.Now().Add(30 * time.Minute).Unix()
		acct := &Account{Platform: PlatformKiro, Type: AccountTypeOAuth, Credentials: map[string]any{
			"expires_at": strconv.FormatInt(exp, 10),
		}}
		require.True(t, r.NeedsRefresh(acct, window))
	})

	t.Run("outside window -> false", func(t *testing.T) {
		exp := time.Now().Add(3 * time.Hour).Unix()
		acct := &Account{Platform: PlatformKiro, Type: AccountTypeOAuth, Credentials: map[string]any{
			"expires_at": strconv.FormatInt(exp, 10),
		}}
		require.False(t, r.NeedsRefresh(acct, window))
	})

	t.Run("already expired -> true", func(t *testing.T) {
		exp := time.Now().Add(-time.Minute).Unix()
		acct := &Account{Platform: PlatformKiro, Type: AccountTypeOAuth, Credentials: map[string]any{
			"expires_at": strconv.FormatInt(exp, 10),
		}}
		require.True(t, r.NeedsRefresh(acct, window))
	})
}

func TestKiroTokenRefresher_CacheKey(t *testing.T) {
	r := NewKiroTokenRefresher()
	require.Equal(t, "kiro:account:42", r.CacheKey(&Account{ID: 42}))
}

// TestKiroTokenRefresher_CredentialAssembly verifies the pure credential-merge
// logic used by Refresh: new token fields overwrite, profile_arn is conditional,
// and unrelated old fields are preserved. The vendor RefreshToken network call
// is not exercised here.
func TestKiroTokenRefresher_CredentialAssembly(t *testing.T) {
	old := map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"expires_at":    "100",
		"auth_method":   "social",
		"region":        "us-east-1",
		"profile_arn":   "arn:old",
	}

	t.Run("profile_arn present overwrites and preserves others", func(t *testing.T) {
		newCreds := map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_at":    strconv.FormatInt(int64(200), 10),
			"profile_arn":   "arn:new",
		}
		merged := MergeCredentials(old, newCreds)
		require.Equal(t, "new-access", merged["access_token"])
		require.Equal(t, "new-refresh", merged["refresh_token"])
		require.Equal(t, "200", merged["expires_at"])
		require.Equal(t, "arn:new", merged["profile_arn"])
		// preserved
		require.Equal(t, "social", merged["auth_method"])
		require.Equal(t, "us-east-1", merged["region"])
	})

	t.Run("empty profile_arn keeps old", func(t *testing.T) {
		newCreds := map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_at":    "200",
		}
		merged := MergeCredentials(old, newCreds)
		require.Equal(t, "arn:old", merged["profile_arn"])
		require.Equal(t, "social", merged["auth_method"])
	})
}
