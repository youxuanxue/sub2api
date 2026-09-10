package handler

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOIDCECPublicKey(t *testing.T) {
	for _, curve := range []elliptic.Curve{elliptic.P256(), elliptic.P384(), elliptic.P521()} {
		t.Run(curve.Params().Name, func(t *testing.T) {
			privateKey, err := ecdsa.GenerateKey(curve, rand.Reader)
			require.NoError(t, err)
			encoded, err := privateKey.PublicKey.Bytes()
			require.NoError(t, err)
			size := (len(encoded) - 1) / 2
			jwk := oidcJWK{Kty: "EC", Crv: curve.Params().Name,
				X: base64.RawURLEncoding.EncodeToString(encoded[1 : 1+size]),
				Y: base64.RawURLEncoding.EncodeToString(encoded[1+size:])}
			publicKey, err := jwk.publicKey()
			require.NoError(t, err)
			ecKey, ok := publicKey.(*ecdsa.PublicKey)
			require.True(t, ok)
			digest := []byte("oidc-jwk-verification")
			signature, err := ecdsa.SignASN1(rand.Reader, privateKey, digest)
			require.NoError(t, err)
			require.True(t, ecdsa.VerifyASN1(ecKey, digest, signature))
			jwk.X, jwk.Y = "AA", "AA"
			_, err = jwk.publicKey()
			require.Error(t, err)
			jwk.X = base64.RawURLEncoding.EncodeToString(append([]byte{1}, make([]byte, size)...))
			_, err = jwk.publicKey()
			require.Error(t, err)
		})
	}
}
