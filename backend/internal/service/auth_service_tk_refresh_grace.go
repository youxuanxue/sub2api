package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TK: refresh token 轮转宽限期（idempotent refresh）。
//
// 背景：刷新采用轮转——一次刷新成功后旧 refresh token 立即失效。但管理后台常开多个
// 标签页，且前端 proactive 定时刷新与 401 拦截器刷新是两条互不共享 single-flight 的
// 路径，access token 又只有 1h，导致同一个旧 refresh token 经常被近乎同时地刷新两次。
// 原实现里第二个请求会命中"token 不存在"→ 被判为 possible reuse attack → 强制登出。
// 线上日志证实这是运营者一天被踢出多次的唯一来源。
//
// 方案：旧 token 轮转后不立即硬删，而是标记 RotatedAt 并保留一个很短的宽限窗口。窗口内
// 同一旧 token 的重复刷新被视为"同一次刷新"，幂等地重新签发有效 token 对、不报攻击；
// 窗口过后旧记录由 Redis TTL 自动消失，再用该 token 仍会命中"不存在"→ 保留真正的重放检测。
//
// 安全取舍：宽限窗口内，被盗 refresh token 的重放会被容忍（秒级）。对 admin-only、极低
// 流量的内部网关，该风险可忽略，换取消除每天多次的误登出。
const tkRefreshRotationGrace = 10 * time.Second

// tkMarkRotatedWithGrace 替代轮转时的硬删除：把记录标记为已轮转，并以很短的宽限 TTL
// 重写回缓存，让它在宽限期后自动过期。失败时退化为不可恢复（调用方继续主流程，旧 token
// 最终随原 TTL 失效），不影响新 token 的签发。
func (s *AuthService) tkMarkRotatedWithGrace(ctx context.Context, tokenHash string, data *RefreshTokenData) {
	now := time.Now()
	rotated := *data
	rotated.RotatedAt = &now
	if err := s.refreshTokenCache.StoreRefreshToken(ctx, tokenHash, &rotated, tkRefreshRotationGrace); err != nil {
		logger.LegacyPrintf("service.auth", "[Auth] Failed to mark refresh token rotated (grace); falling back to delete: %v", err)
		// 回退：宽限标记失败则按旧行为硬删，宁可偶发一次误判也不让旧 token 长期有效。
		_ = s.refreshTokenCache.DeleteRefreshToken(ctx, tokenHash)
	}
}

// tkReissueWithinGrace 处理宽限窗口内对已轮转 token 的重复刷新：保持同一 family 重新签发
// 一对有效 token，不删 family、不报重放攻击。user/active/version 校验在调用点已通过。
func (s *AuthService) tkReissueWithinGrace(ctx context.Context, user *User, data *RefreshTokenData) (*TokenPairWithUser, error) {
	pair, err := s.GenerateTokenPair(ctx, user, data.FamilyID)
	if err != nil {
		return nil, err
	}
	return &TokenPairWithUser{
		TokenPair: *pair,
		UserRole:  user.Role,
	}, nil
}
