package service

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestUS048_SupplierCredentialFingerprintFollowsEncryptionKeyNotJWTKey(t *testing.T) {
	firstConfig := &config.Config{}
	firstConfig.Totp.EncryptionKey = strings.Repeat("a", 64)
	firstConfig.JWT.Secret = "jwt-secret-one"
	secondConfig := &config.Config{}
	secondConfig.Totp.EncryptionKey = firstConfig.Totp.EncryptionKey
	secondConfig.JWT.Secret = "jwt-secret-two"
	rotatedEncryptionConfig := &config.Config{}
	rotatedEncryptionConfig.Totp.EncryptionKey = strings.Repeat("b", 64)
	rotatedEncryptionConfig.JWT.Secret = firstConfig.JWT.Secret

	first, err := NewSupplierCredentialFingerprinter(firstConfig).Fingerprint("supplier-secret")
	require.NoError(t, err)
	second, err := NewSupplierCredentialFingerprinter(secondConfig).Fingerprint("supplier-secret")
	require.NoError(t, err)
	rotated, err := NewSupplierCredentialFingerprinter(rotatedEncryptionConfig).Fingerprint("supplier-secret")
	require.NoError(t, err)

	require.Equal(t, first, second, "JWT rotation must not change supplier credential identity")
	require.NotEqual(t, first, rotated, "credential fingerprint must follow the encryption-key lifecycle")
}
