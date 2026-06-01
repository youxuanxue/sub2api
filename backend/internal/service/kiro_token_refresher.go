package service

import (
	"context"
	"strconv"
	"time"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
)

// KiroTokenRefresher 处理 Kiro（第六平台）SSO token 刷新。
//
// Kiro 凭证刷新走 vendored 的 kiroproto.RefreshToken：
//   - auth_method=="social" 打 prod.us-east-1.auth.desktop.kiro.dev/refreshToken
//   - 否则打 oidc.<region>.amazonaws.com/token（需 client_id/client_secret）
//
// 刷新器自身无外部依赖（凭证映射由 (*Account).toKiroProtoAccount 完成，
// 网络调用由 vendor 包级函数完成），因此在 NewTokenRefreshService 内部直接 new。
type KiroTokenRefresher struct{}

// NewKiroTokenRefresher 创建 Kiro token 刷新器。
func NewKiroTokenRefresher() *KiroTokenRefresher {
	return &KiroTokenRefresher{}
}

// CacheKey 返回用于分布式锁的缓存键。
func (r *KiroTokenRefresher) CacheKey(account *Account) string {
	return "kiro:account:" + strconv.FormatInt(account.ID, 10)
}

// CanRefresh 检查是否能处理此账号：仅 kiro 平台的 oauth 类型账号。
func (r *KiroTokenRefresher) CanRefresh(account *Account) bool {
	return account.Platform == PlatformKiro && account.Type == AccountTypeOAuth
}

// NeedsRefresh 基于 expires_at 判断 token 是否在刷新窗口内。
func (r *KiroTokenRefresher) NeedsRefresh(account *Account, refreshWindow time.Duration) bool {
	exp := account.GetKiroExpiresAt()
	if exp == nil {
		return false
	}
	return time.Until(*exp) < refreshWindow
}

// Refresh 执行 token 刷新，保留原有 credentials 中的所有字段，只更新 token 相关字段。
//
// vendor.RefreshToken 返回的 error 在 invalid_grant 场景文本含字面量
// "invalid_grant"，由 isNonRetryableRefreshError 识别并标 error 移出调度，
// 因此这里直接透传 error，不做额外失败处理。
func (r *KiroTokenRefresher) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	kiroAcct := account.toKiroProtoAccount()
	access, refresh, expiresAt, profileArn, err := kiroproto.RefreshToken(kiroAcct)
	if err != nil {
		return nil, err
	}

	// expires_at 以 unix 秒字符串存储：vendor 返回 unix 秒，GetCredentialAsTime
	// 既能解析 unix 秒整数字符串也能解析 RFC3339，与 Claude 刷新器的存储风格一致。
	newCreds := map[string]any{
		"access_token":  access,
		"refresh_token": refresh,
		"expires_at":    strconv.FormatInt(expiresAt, 10),
	}
	if profileArn != "" {
		newCreds["profile_arn"] = profileArn
	}

	return MergeCredentials(account.Credentials, newCreds), nil
}
