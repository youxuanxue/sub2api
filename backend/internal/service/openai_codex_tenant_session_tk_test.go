package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayCodexConvergenceKeepsTenantSessionIsolation(t *testing.T) {
	for _, mode := range []codexFingerprintMode{codexFingerprintSession, codexFingerprintFull} {
		account := &Account{ID: 17, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{codexFingerprintModeExtraKey: string(mode), codexFingerprintSeedExtraKey: "d2450fdd-2f21-4a61-9d75-e4c0c1829d91"}}
		resolve := func(key int64, session string, header bool) *codexFingerprintIDs {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Set("api_key", &APIKey{ID: key})
			if header {
				c.Request.Header.Set("session-id", session)
			}
			return resolveGatewayCodexFingerprintIDs(c, account, session)
		}
		a, b, same := resolve(1, "same-session", true), resolve(2, "same-session", true), resolve(1, "same-session", true)
		require.NotNil(t, a)
		require.NotEqual(t, a.sessionID, b.sessionID)
		require.NotEqual(t, a.threadID, b.threadID)
		require.Equal(t, a.sessionID, same.sessionID)
		require.Equal(t, a.threadID, same.threadID)
		require.Equal(t, a.installationID, b.installationID)
		require.Equal(t, a.sessionID, resolve(1, "same-session", false).sessionID)
		require.NotEqual(t, a.sessionID, resolve(1, "different-session", true).sessionID)
		require.NotEqual(t, resolve(1, "", false).sessionID, resolve(1, "", false).sessionID)
	}
}
