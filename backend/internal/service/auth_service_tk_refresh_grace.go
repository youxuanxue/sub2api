package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

const tkRefreshRotationGrace = 10 * time.Second

func (s *AuthService) refreshRotationCipher() (cipher.AEAD, error) {
	if s.cfg == nil || s.cfg.JWT.Secret == "" {
		return nil, ErrServiceUnavailable
	}
	key := sha256.Sum256([]byte("sub2api:refresh-rotation:v1:" + s.cfg.JWT.Secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *AuthService) rotateRefreshTokenWithinGrace(ctx context.Context, hash string, user *User) (*TokenPairWithUser, error) {
	cache, ok := s.refreshTokenCache.(RefreshTokenRotationCache)
	if !ok {
		return nil, ErrServiceUnavailable
	}
	claim, err := randomHexString(16)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	data, owned, err := cache.ClaimRefreshToken(ctx, hash, claim, tkRefreshRotationGrace)
	if err != nil {
		if errors.Is(err, ErrRefreshTokenNotFound) {
			return nil, ErrRefreshTokenInvalid
		}
		return nil, ErrServiceUnavailable
	}
	if data.UserID != user.ID || data.TokenVersion != resolvedTokenVersion(user) {
		return nil, ErrTokenRevoked
	}
	aead, err := s.refreshRotationCipher()
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	if !owned {
		waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		for {
			if data.RotatedAt == nil || time.Since(*data.RotatedAt) < 0 || time.Since(*data.RotatedAt) >= tkRefreshRotationGrace {
				return nil, ErrRefreshTokenInvalid
			}
			if data.RotationResult != "" {
				current, err := s.userRepo.GetByID(ctx, user.ID)
				if err != nil {
					return nil, ErrServiceUnavailable
				}
				if !current.IsActive() || data.TokenVersion != resolvedTokenVersion(current) {
					return nil, ErrTokenRevoked
				}
				sealed, err := base64.RawStdEncoding.DecodeString(data.RotationResult)
				if err != nil || len(sealed) < aead.NonceSize() {
					return nil, ErrServiceUnavailable
				}
				plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(hash))
				if err != nil {
					return nil, ErrServiceUnavailable
				}
				var pair TokenPairWithUser
				if json.Unmarshal(plain, &pair) != nil {
					return nil, ErrServiceUnavailable
				}
				return &pair, nil
			}
			select {
			case <-waitCtx.Done():
				return nil, ErrServiceUnavailable
			case <-time.After(10 * time.Millisecond):
			}
			data, err = s.refreshTokenCache.GetRefreshToken(waitCtx, hash)
			if err != nil {
				if errors.Is(err, ErrRefreshTokenNotFound) {
					return nil, ErrRefreshTokenInvalid
				}
				return nil, ErrServiceUnavailable
			}
		}
	}
	access, err := s.GenerateToken(ctx, user)
	if err != nil {
		return nil, err
	}
	raw, err := randomHexString(32)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	pair := &TokenPairWithUser{TokenPair: TokenPair{AccessToken: access, RefreshToken: refreshTokenPrefix + raw, ExpiresIn: s.GetAccessTokenExpiresIn()}, UserRole: user.Role}
	plain, err := json.Marshal(pair)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrServiceUnavailable
	}
	sealed := aead.Seal(nonce, nonce, plain, []byte(hash))
	now := time.Now()
	ttl := time.Duration(s.cfg.JWT.RefreshTokenExpireDays) * 24 * time.Hour
	successor := &RefreshTokenData{UserID: user.ID, TokenVersion: resolvedTokenVersion(user), FamilyID: data.FamilyID, BindingHash: data.BindingHash, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	complete, err := cache.CompleteRefreshTokenRotation(ctx, hash, claim, hashToken(pair.RefreshToken), successor, base64.RawStdEncoding.EncodeToString(sealed), ttl)
	if err != nil || !complete {
		return nil, ErrServiceUnavailable
	}
	return pair, nil
}
