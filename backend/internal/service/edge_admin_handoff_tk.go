package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const EdgeHandoffTTL = 60 * time.Second

var ErrEdgeHandoffUnavailable = errors.New("edge handoff unavailable")
var ErrEdgeHandoffInvalid = errors.New("invalid or expired edge handoff")

type EdgeHandoffRequest struct {
	Challenge string `json:"challenge"`
	Attempt   string `json:"attempt"`
}
type EdgeHandoffDelegation struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}
type EdgeHandoffClaims struct {
	KeyID     string `json:"kid"`
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	Purpose   string `json:"purpose"`
	Initiator int64  `json:"initiator"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	Challenge string `json:"challenge"`
	Attempt   string `json:"attempt"`
}
type EdgeHandoffCode struct {
	Code    string `json:"code"`
	Attempt string `json:"attempt"`
}
type EdgeHandoffExchange struct {
	Code     string `json:"code"`
	Verifier string `json:"verifier"`
	Attempt  string `json:"attempt"`
}
type EdgeHandoffCache interface {
	Create(context.Context, string, string, EdgeHandoffClaims, time.Duration) error
	Consume(context.Context, string, string, string) (*EdgeHandoffClaims, error)
}
type EdgeAdminHandoff struct {
	cfg   *config.EdgeHandoffConfig
	cache EdgeHandoffCache
	now   func() time.Time
}

func NewEdgeAdminHandoff(cfg *config.Config, cache EdgeHandoffCache) (*EdgeAdminHandoff, error) {
	trust, err := config.LoadEdgeHandoffConfig(cfg.EdgeHandoffFile)
	if err != nil {
		return nil, err
	}
	return &EdgeAdminHandoff{cfg: trust, cache: cache, now: time.Now}, nil
}
func EdgeHandoffProof(value string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(value) == 43 && len(decoded) == 32
}
func EdgeHandoffDigest(value string) string {
	hash := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}
func EdgeHandoffFamily(claims *EdgeHandoffClaims) string {
	hash := sha256.Sum256([]byte(claims.Issuer + "\x00" + claims.Attempt))
	return "edge-handoff-" + hex.EncodeToString(hash[:])
}
func (s *EdgeAdminHandoff) Receiver() *config.EdgeHandoffReceiver {
	if s == nil || s.cfg == nil {
		return nil
	}
	return s.cfg.Receiver
}
func (s *EdgeAdminHandoff) CanSign(edgeID, origin string) bool {
	if s == nil || s.cfg == nil {
		return false
	}
	signer, ok := s.cfg.Signers[edgeID]
	return ok && signer.Origin == origin
}
func (s *EdgeAdminHandoff) Sign(edgeID, origin string, initiator int64, request EdgeHandoffRequest) (*EdgeHandoffDelegation, error) {
	if !s.CanSign(edgeID, origin) {
		return nil, ErrEdgeHandoffUnavailable
	}
	if initiator <= 0 || !EdgeHandoffProof(request.Challenge) || !EdgeHandoffProof(request.Attempt) {
		return nil, ErrEdgeHandoffInvalid
	}
	signer := s.cfg.Signers[edgeID]
	seed, err := base64.RawURLEncoding.Strict().DecodeString(signer.Seed)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, ErrEdgeHandoffUnavailable
	}
	now := s.now()
	claims := EdgeHandoffClaims{KeyID: signer.KeyID, Issuer: s.cfg.Issuer, Audience: origin, Purpose: "edge-handoff", Initiator: initiator, IssuedAt: now.Unix(), ExpiresAt: now.Add(EdgeHandoffTTL).Unix(), Challenge: request.Challenge, Attempt: request.Attempt}
	raw, err := json.Marshal(claims)
	if err != nil {
		return nil, err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	signature := ed25519.Sign(ed25519.NewKeyFromSeed(seed), []byte(payload))
	return &EdgeHandoffDelegation{Payload: payload, Signature: base64.RawURLEncoding.EncodeToString(signature)}, nil
}
func (s *EdgeAdminHandoff) Mint(ctx context.Context, envelope EdgeHandoffDelegation) (*EdgeHandoffCode, error) {
	receiver := s.Receiver()
	if receiver == nil || s.cache == nil {
		return nil, ErrEdgeHandoffUnavailable
	}
	if len(envelope.Payload) > 4096 {
		return nil, ErrEdgeHandoffInvalid
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(envelope.Payload)
	if err != nil {
		return nil, ErrEdgeHandoffInvalid
	}
	var claims EdgeHandoffClaims
	if json.Unmarshal(raw, &claims) != nil {
		return nil, ErrEdgeHandoffInvalid
	}
	pub, err := base64.RawURLEncoding.Strict().DecodeString(receiver.PublicKeys[claims.KeyID])
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, ErrEdgeHandoffInvalid
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(envelope.Signature)
	if err != nil || !ed25519.Verify(pub, []byte(envelope.Payload), sig) {
		return nil, ErrEdgeHandoffInvalid
	}
	now := s.now()
	if claims.Issuer != receiver.Issuer || claims.Audience != receiver.Origin || claims.Purpose != "edge-handoff" || claims.Initiator <= 0 || claims.IssuedAt > now.Unix()+5 || claims.IssuedAt < now.Unix()-int64(EdgeHandoffTTL.Seconds()) || claims.ExpiresAt <= now.Unix() || claims.ExpiresAt-claims.IssuedAt > int64(EdgeHandoffTTL.Seconds()) || claims.ExpiresAt <= claims.IssuedAt || !EdgeHandoffProof(claims.Challenge) || !EdgeHandoffProof(claims.Attempt) {
		return nil, ErrEdgeHandoffInvalid
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return nil, err
	}
	code := base64.RawURLEncoding.EncodeToString(bytes)
	ttl := time.Unix(claims.ExpiresAt, 0).Sub(now)
	if err := s.cache.Create(ctx, EdgeHandoffDigest(claims.Issuer+"\x00"+claims.Attempt), EdgeHandoffDigest(code), claims, ttl); err != nil {
		return nil, err
	}
	return &EdgeHandoffCode{Code: code, Attempt: claims.Attempt}, nil
}
func (s *EdgeAdminHandoff) Exchange(ctx context.Context, request EdgeHandoffExchange) (*EdgeHandoffClaims, error) {
	if s.Receiver() == nil || s.cache == nil {
		return nil, ErrEdgeHandoffUnavailable
	}
	if !EdgeHandoffProof(request.Code) || !EdgeHandoffProof(request.Verifier) || !EdgeHandoffProof(request.Attempt) {
		return nil, ErrEdgeHandoffInvalid
	}
	claims, err := s.cache.Consume(ctx, EdgeHandoffDigest(request.Code), request.Attempt, EdgeHandoffDigest(request.Verifier))
	if err != nil {
		return nil, err
	}
	if claims == nil || claims.ExpiresAt <= s.now().Unix() || claims.Issuer != s.Receiver().Issuer || claims.Audience != s.Receiver().Origin {
		return nil, ErrEdgeHandoffInvalid
	}
	// Recheck trust on exchange too: removing a key revokes outstanding codes.
	if _, ok := s.Receiver().PublicKeys[claims.KeyID]; !ok {
		return nil, ErrEdgeHandoffInvalid
	}
	return claims, nil
}
