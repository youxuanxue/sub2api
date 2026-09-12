//go:build unit

package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type handoffCacheStub struct {
	EdgeHandoffCache
	claims EdgeHandoffClaims
	calls  int
}

func (c *handoffCacheStub) Create(_ context.Context, _, _ string, claims EdgeHandoffClaims, _ time.Duration) error {
	c.calls++
	c.claims = claims
	return nil
}
func handoffTestOwner(t *testing.T) (*EdgeAdminHandoff, *handoffCacheStub) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	cache := &handoffCacheStub{}
	cfg := &config.EdgeHandoffConfig{Issuer: "https://prod.example", Signers: map[string]config.EdgeHandoffSigner{"e1": {Origin: "https://edge.example", KeyID: "k1", Seed: base64.RawURLEncoding.EncodeToString(key.Seed())}}, Receiver: &config.EdgeHandoffReceiver{Issuer: "https://prod.example", Origin: "https://edge.example", AdminUserID: 1, PublicKeys: map[string]string{"k1": base64.RawURLEncoding.EncodeToString(pub)}}}
	return &EdgeAdminHandoff{cfg: cfg, cache: cache, now: func() time.Time { return time.Unix(1000, 0) }}, cache
}
func TestEdgeAdminHandoff_DelegationValidation(t *testing.T) {
	for _, kind := range []string{"valid", "tamper", "signature", "audience", "issuer", "purpose", "future", "expired", "lifetime", "extreme lifetime", "key revoked", "challenge", "initiator"} {
		t.Run(kind, func(t *testing.T) {
			owner, cache := handoffTestOwner(t)
			envelope, err := owner.Sign("e1", "https://edge.example", 7, EdgeHandoffRequest{Challenge: EdgeHandoffDigest("verifier"), Attempt: EdgeHandoffDigest("attempt")})
			require.NoError(t, err)
			raw, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
			require.NoError(t, err)
			var claims EdgeHandoffClaims
			require.NoError(t, json.Unmarshal(raw, &claims))
			switch kind {
			case "audience":
				claims.Audience = "https://other.example"
			case "issuer":
				claims.Issuer = "https://other.example"
			case "purpose":
				claims.Purpose = "login"
			case "future":
				claims.IssuedAt += 10
				claims.ExpiresAt += 10
			case "expired":
				claims.IssuedAt -= 60
				claims.ExpiresAt -= 60
			case "extreme lifetime":
				claims.IssuedAt = -1 << 63
				claims.ExpiresAt = 1 << 62
			case "lifetime":
				claims.ExpiresAt += 1
			case "challenge":
				claims.Challenge = "invalid"
			case "initiator":
				claims.Initiator = 0
			case "key revoked":
				delete(owner.cfg.Receiver.PublicKeys, "k1")
			}
			raw, err = json.Marshal(claims)
			require.NoError(t, err)
			envelope.Payload = base64.RawURLEncoding.EncodeToString(raw)
			seed, err := base64.RawURLEncoding.DecodeString(owner.cfg.Signers["e1"].Seed)
			require.NoError(t, err)
			envelope.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed), []byte(envelope.Payload)))
			if kind == "tamper" {
				envelope.Payload += "A"
			}
			if kind == "signature" {
				envelope.Signature = "invalid"
			}
			result, err := owner.Mint(context.Background(), *envelope)
			if kind == "valid" {
				require.NoError(t, err)
				require.True(t, EdgeHandoffProof(result.Code))
				require.Equal(t, int64(7), cache.claims.Initiator)
			} else {
				require.ErrorIs(t, err, ErrEdgeHandoffInvalid)
				require.Zero(t, cache.calls)
			}
		})
	}
}
func TestEdgeAdminHandoff_TargetPinnedAndDisabled(t *testing.T) {
	owner, _ := handoffTestOwner(t)
	_, err := owner.Sign("e1", "https://other.example", 7, EdgeHandoffRequest{})
	require.ErrorIs(t, err, ErrEdgeHandoffUnavailable)
	var disabled *EdgeAdminHandoff
	require.False(t, disabled.CanSign("e1", "https://edge.example"))
	_, err = disabled.Mint(context.Background(), EdgeHandoffDelegation{})
	require.ErrorIs(t, err, ErrEdgeHandoffUnavailable)
}
