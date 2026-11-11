package service

import "context"

// ClearStickySessionIfBoundTo 仅当粘性会话仍绑定在 accountID 上时才删除绑定。
//
// 用于"字节已写给客户端、无法在本次请求内重放"的上游故障（如 Gemini 原生流截断）：
// 客户端的下一次重试会带着同一个会话键回来，若绑定不清，调度器会把重试再次钉回
// 刚断流的账号。比较后再删，避免误删并发请求刚刚改绑到其他账号的健康绑定。
func (s *GatewayService) ClearStickySessionIfBoundTo(ctx context.Context, groupID *int64, sessionHash string, accountID int64) bool {
	if sessionHash == "" || accountID <= 0 || s.cache == nil {
		return false
	}
	bound, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
	if err != nil || bound != accountID {
		return false
	}
	return s.cache.DeleteSessionAccountID(ctx, derefGroupID(groupID), sessionHash) == nil
}
