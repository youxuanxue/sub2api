package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

type hmacSupplierCredentialFingerprinter struct{ key []byte }

func NewSupplierCredentialFingerprinter(cfg *config.Config) SupplierCredentialFingerprinter {
	key := ""
	if cfg != nil {
		key = strings.TrimSpace(cfg.Totp.EncryptionKey)
	}
	return &hmacSupplierCredentialFingerprinter{key: []byte(key)}
}

func (f *hmacSupplierCredentialFingerprinter) Fingerprint(credential string) (string, error) {
	credential = strings.TrimSpace(credential)
	if credential == "" || len(f.key) == 0 {
		return "", errors.New("supplier credential fingerprint key or credential is empty")
	}
	mac := hmac.New(sha256.New, f.key)
	_, _ = mac.Write([]byte(credential))
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil)), nil
}
