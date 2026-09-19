package service

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/stretchr/testify/require"
)

func TestAntigravityAuthorizationLinkMatchesSession(t *testing.T) {
	s := NewAntigravityOAuthService(nil)
	t.Cleanup(s.Stop)
	result, err := s.GenerateAuthURL(context.Background(), nil)
	require.NoError(t, err)
	session, ok := s.sessionStore.Get(result.SessionID)
	require.True(t, ok)
	u, err := url.Parse(result.AuthURL)
	require.NoError(t, err)
	require.Equal(t, session.State, u.Query().Get("state"))
	require.Equal(t, antigravity.GenerateCodeChallenge(session.CodeVerifier), u.Query().Get("code_challenge"))
	require.Equal(t, antigravity.RedirectURI, u.Query().Get("redirect_uri"))
	require.Equal(t, session.CreatedAt.Add(antigravity.SessionTTL).Unix(), result.ExpiresAt)
	// Expired or mismatched state fails before any Google request is attempted.
	_, err = s.ExchangeCode(context.Background(), &AntigravityExchangeCodeInput{SessionID: result.SessionID, State: "different", Code: "unused"})
	require.ErrorContains(t, err, "state 无效")
	session.CreatedAt = time.Now().Add(-antigravity.SessionTTL - time.Second)
	_, err = s.ExchangeCode(context.Background(), &AntigravityExchangeCodeInput{SessionID: result.SessionID, State: result.State, Code: "unused"})
	require.ErrorContains(t, err, "session 不存在或已过期")
}
