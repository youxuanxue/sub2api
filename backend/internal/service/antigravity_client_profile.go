package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

// antigravityRequestContext applies one account's complete Manager identity to
// every retry of one upstream request. Keeping this at the request boundary
// prevents a Manager UA from being paired with a CLI TLS profile or from
// changing machine/session headers between retries.
func antigravityRequestContext(ctx context.Context, account *Account, sessionHash string) context.Context {
	if account == nil || account.AntigravityClientProfile() != antigravity.ClientProfileManager {
		return antigravity.WithClientProfile(ctx, antigravity.ClientProfileCLI)
	}

	accountKey := strconv.FormatInt(account.ID, 10)
	machineID := stableAntigravityIdentity("machine", accountKey)
	sessionKey := strings.TrimSpace(sessionHash)
	if sessionKey == "" {
		sessionKey = accountKey
	}
	sessionID := stableAntigravitySessionUUID(accountKey + ":" + sessionKey)
	return antigravity.WithManagerIdentity(
		antigravity.WithClientProfile(ctx, antigravity.ClientProfileManager),
		antigravity.ManagerIdentity{MachineID: machineID, SessionID: sessionID},
	)
}

func stableAntigravityIdentity(kind, value string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("antigravity-%s-v1:%s", kind, value)))
	return hex.EncodeToString(h[:16])
}

// stableAntigravitySessionUUID follows Manager's derive_session_uuid shape:
// deterministic SHA-256 material rendered as an RFC 4122 version-4 UUID. This
// keeps the header format compatible while retaining retry stability.
func stableAntigravitySessionUUID(value string) string {
	h := sha256.Sum256([]byte("antigravity-session-v1:" + value))
	h[6] = (h[6] & 0x0f) | 0x40
	h[8] = (h[8] & 0x3f) | 0x80
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		h[0], h[1], h[2], h[3], h[4], h[5], h[6], h[7], h[8], h[9],
		h[10], h[11], h[12], h[13], h[14], h[15])
}
