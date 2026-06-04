//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// statefulRefreshCache 是一个有状态的内存 RefreshTokenCache，用于验证轮转宽限期行为：
// 它真实地存/取/删记录，并支持手动 evict 模拟 Redis 短 TTL 过期。
type statefulRefreshCache struct {
	store         map[string]*RefreshTokenData
	deletedFamily []string
}

func newStatefulRefreshCache() *statefulRefreshCache {
	return &statefulRefreshCache{store: map[string]*RefreshTokenData{}}
}

func (c *statefulRefreshCache) evict(tokenHash string) { delete(c.store, tokenHash) }

func (c *statefulRefreshCache) StoreRefreshToken(_ context.Context, tokenHash string, data *RefreshTokenData, _ time.Duration) error {
	cloned := *data
	c.store[tokenHash] = &cloned
	return nil
}

func (c *statefulRefreshCache) GetRefreshToken(_ context.Context, tokenHash string) (*RefreshTokenData, error) {
	if d, ok := c.store[tokenHash]; ok {
		cloned := *d
		return &cloned, nil
	}
	return nil, ErrRefreshTokenNotFound
}

func (c *statefulRefreshCache) DeleteRefreshToken(_ context.Context, tokenHash string) error {
	delete(c.store, tokenHash)
	return nil
}

func (c *statefulRefreshCache) DeleteUserRefreshTokens(_ context.Context, _ int64) error { return nil }

func (c *statefulRefreshCache) DeleteTokenFamily(_ context.Context, familyID string) error {
	c.deletedFamily = append(c.deletedFamily, familyID)
	return nil
}

func (c *statefulRefreshCache) AddToUserTokenSet(_ context.Context, _ int64, _ string, _ time.Duration) error {
	return nil
}

func (c *statefulRefreshCache) AddToFamilyTokenSet(_ context.Context, _ string, _ string, _ time.Duration) error {
	return nil
}

func (c *statefulRefreshCache) GetUserTokenHashes(_ context.Context, _ int64) ([]string, error) {
	return nil, nil
}

func (c *statefulRefreshCache) GetFamilyTokenHashes(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (c *statefulRefreshCache) IsTokenInFamily(_ context.Context, _ string, _ string) (bool, error) {
	return false, nil
}

func newTestAuthServiceForGrace(repo *userRepoStub, cache RefreshTokenCache) *AuthService {
	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:                 "test-secret-test-secret-test-secret",
			ExpireHour:             1,
			RefreshTokenExpireDays: 30,
		},
	}
	return NewAuthService(
		nil, repo, nil, cache, cfg,
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
}

func activeUser() *User {
	return &User{
		ID:                   42,
		Email:                "op@example.com",
		Role:                 RoleAdmin,
		Status:               StatusActive,
		TokenVersion:         7,
		TokenVersionResolved: true, // 直接用 TokenVersion，绕过密码哈希指纹
	}
}

// seedToken 在缓存里写入一个未轮转的 refresh token，返回原始 token 字符串。
func seedToken(c *statefulRefreshCache, user *User) string {
	raw := refreshTokenPrefix + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	c.store[hashToken(raw)] = &RefreshTokenData{
		UserID:       user.ID,
		TokenVersion: resolvedTokenVersion(user),
		FamilyID:     "fam-1",
		CreatedAt:    time.Now(),
		ExpiresAt:    time.Now().Add(30 * 24 * time.Hour),
	}
	return raw
}

func TestRefreshTokenPair_FirstRotation_MarksRotatedNotDeleted(t *testing.T) {
	cache := newStatefulRefreshCache()
	user := activeUser()
	raw := seedToken(cache, user)
	svc := newTestAuthServiceForGrace(&userRepoStub{user: user}, cache)

	pair, err := svc.RefreshTokenPair(context.Background(), raw)
	if err != nil {
		t.Fatalf("first rotation failed: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatalf("expected new token pair, got %+v", pair)
	}
	old := cache.store[hashToken(raw)]
	if old == nil {
		t.Fatalf("old token must NOT be hard-deleted during grace window")
	}
	if old.RotatedAt == nil {
		t.Fatalf("old token must be marked RotatedAt after first rotation")
	}
}

func TestRefreshTokenPair_WithinGrace_DuplicateRefresh_NoReuseAttack(t *testing.T) {
	cache := newStatefulRefreshCache()
	user := activeUser()
	raw := seedToken(cache, user)
	svc := newTestAuthServiceForGrace(&userRepoStub{user: user}, cache)

	if _, err := svc.RefreshTokenPair(context.Background(), raw); err != nil {
		t.Fatalf("first refresh failed: %v", err)
	}
	// 第二次用同一个旧 token（宽限窗口内，记录仍在且已 RotatedAt）→ 必须幂等成功，不报重放。
	pair, err := svc.RefreshTokenPair(context.Background(), raw)
	if err != nil {
		t.Fatalf("within-grace duplicate refresh must succeed, got: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatalf("within-grace refresh must reissue a valid pair, got %+v", pair)
	}
	if len(cache.deletedFamily) != 0 {
		t.Fatalf("within-grace refresh must NOT delete the token family, deleted=%v", cache.deletedFamily)
	}
}

func TestRefreshTokenPair_AfterGraceExpiry_StillDetectsReplay(t *testing.T) {
	cache := newStatefulRefreshCache()
	user := activeUser()
	raw := seedToken(cache, user)
	svc := newTestAuthServiceForGrace(&userRepoStub{user: user}, cache)

	if _, err := svc.RefreshTokenPair(context.Background(), raw); err != nil {
		t.Fatalf("first refresh failed: %v", err)
	}
	// 模拟宽限期后 Redis 短 TTL 到期：旧记录消失。
	cache.evict(hashToken(raw))

	_, err := svc.RefreshTokenPair(context.Background(), raw)
	if !errors.Is(err, ErrRefreshTokenInvalid) {
		t.Fatalf("after grace, reused token must be ErrRefreshTokenInvalid, got: %v", err)
	}
}

func TestRefreshTokenPair_TokenVersionMismatch_StillRevokes(t *testing.T) {
	cache := newStatefulRefreshCache()
	user := activeUser()
	raw := seedToken(cache, user)
	// 模拟改密：用户当前 TokenVersion 与记录里的不一致。
	user.TokenVersion = 999
	svc := newTestAuthServiceForGrace(&userRepoStub{user: user}, cache)

	_, err := svc.RefreshTokenPair(context.Background(), raw)
	if !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("password-change (version mismatch) must return ErrTokenRevoked, got: %v", err)
	}
	if len(cache.deletedFamily) == 0 {
		t.Fatalf("version mismatch must delete the token family")
	}
}
