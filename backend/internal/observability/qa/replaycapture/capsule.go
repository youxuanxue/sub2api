// Package replaycapture holds bounded, encrypted release evidence outside QA exports.
package replaycapture

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

const (
	MaxBodyBytes     = 4 << 20
	MaxStoreBytes    = 128 << 20
	MaxFiles         = 400
	PerCombination   = 2
	Retention        = 24 * time.Hour
	MaxEnvelopeBytes = 8 << 20
)

var ErrExpired = errors.New("replay evidence expired")

// Request contains only consumed request bytes and allowlisted protocol headers.
// Authentication is resolved by APIKeyID in the isolated database at execution.
type Request struct {
	RequestID  string            `json:"request_id"`
	UserID     int64             `json:"user_id"`
	APIKeyID   int64             `json:"api_key_id"`
	Model      string            `json:"requested_model"`
	Endpoint   string            `json:"inbound_endpoint"`
	Stream     bool              `json:"stream"`
	Tools      bool              `json:"tool_calls_present"`
	Multimodal bool              `json:"multimodal_present"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Headers    map[string]string `json:"headers"`
	Body       []byte            `json:"body"`
	CapturedAt time.Time         `json:"captured_at"`
}

type Envelope struct {
	Version    int       `json:"version"`
	KeyID      string    `json:"key_id"`
	CreatedAt  time.Time `json:"created_at"`
	WrappedKey []byte    `json:"wrapped_key"`
	Nonce      []byte    `json:"nonce"`
	Ciphertext []byte    `json:"ciphertext"`
}

func PublicKey(raw []byte) (*rsa.PublicKey, error) {
	block, rest := pem.Decode(raw)
	if block == nil || len(rest) != 0 || block.Type != "PUBLIC KEY" {
		return nil, errors.New("replay public key must be PKIX PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid replay public key")
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok || key.N.BitLen() < 3072 {
		return nil, errors.New("replay requires RSA >=3072 bits")
	}
	return key, nil
}

func PrivateKey(raw []byte) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode(raw)
	if block == nil || len(rest) != 0 || block.Type != "PRIVATE KEY" {
		return nil, errors.New("replay private key must be PKCS8 PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid replay private key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() < 3072 {
		return nil, errors.New("replay requires RSA >=3072 bits")
	}
	return key, key.Validate()
}

func KeyID(key *rsa.PublicKey) string {
	raw, _ := x509.MarshalPKIXPublicKey(key)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (e Envelope) aad() []byte {
	return []byte(fmt.Sprintf("tokenkey-replay-v%d\n%s\n%s", e.Version, e.KeyID, e.CreatedAt.UTC().Format(time.RFC3339Nano)))
}

func Encrypt(key *rsa.PublicKey, req Request) ([]byte, error) {
	if len(req.Body) > MaxBodyBytes || req.RequestID == "" || req.UserID <= 0 || req.APIKeyID <= 0 || req.CapturedAt.IsZero() {
		return nil, errors.New("invalid replay request")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return nil, err
	}
	defer clear(secret)
	block, err := aes.NewCipher(secret)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	e := Envelope{Version: 1, KeyID: KeyID(key), CreatedAt: req.CapturedAt.UTC(), Nonce: make([]byte, gcm.NonceSize())}
	if _, err = rand.Read(e.Nonce); err != nil {
		return nil, err
	}
	e.WrappedKey, err = rsa.EncryptOAEP(sha256.New(), rand.Reader, key, secret, []byte("tokenkey-replay-v1"))
	if err != nil {
		return nil, err
	}
	e.Ciphertext = gcm.Seal(nil, e.Nonce, raw, e.aad())
	clear(raw)
	return json.Marshal(e)
}

func Decrypt(key *rsa.PrivateKey, raw []byte, now time.Time) (Request, error) {
	var req Request
	if len(raw) > MaxEnvelopeBytes {
		return req, errors.New("replay envelope too large")
	}
	var e Envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return req, errors.New("invalid replay envelope")
	}
	if e.Version != 1 || e.KeyID != KeyID(&key.PublicKey) || e.CreatedAt.After(now.Add(time.Minute)) {
		return req, errors.New("replay envelope version/key/expiry mismatch")
	}
	secret, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, key, e.WrappedKey, []byte("tokenkey-replay-v1"))
	if err != nil {
		return req, errors.New("replay envelope authentication failed")
	}
	defer clear(secret)
	block, err := aes.NewCipher(secret)
	if err != nil {
		return req, errors.New("invalid replay encryption key")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return req, err
	}
	if len(e.Nonce) != gcm.NonceSize() {
		return req, errors.New("invalid replay nonce")
	}
	plain, err := gcm.Open(nil, e.Nonce, e.Ciphertext, e.aad())
	if err != nil {
		return req, errors.New("replay envelope authentication failed")
	}
	defer clear(plain)
	if err = json.Unmarshal(plain, &req); err != nil {
		return Request{}, errors.New("invalid replay plaintext")
	}
	if !req.CapturedAt.Equal(e.CreatedAt) || len(req.Body) > MaxBodyBytes || req.RequestID == "" || req.UserID <= 0 || req.APIKeyID <= 0 {
		return Request{}, errors.New("invalid replay identity")
	}
	if !now.Before(e.CreatedAt.Add(Retention)) {
		return Request{}, ErrExpired
	}
	return req, nil
}
