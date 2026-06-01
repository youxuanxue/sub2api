package service

import "context"

// OAuth401AfterRefreshCounterCache 追踪 OAuth 账号「token 被成功刷新过、却仍持续 401」的次数。
//
// 与单纯的 401 计数不同：这里用 token 版本戳（credentials["_token_version"]，每次成功
// refresh 盖一个 UnixMilli）做闸门——只有「本次 401 所用 token 的版本 > 上一次 401 记录的
// 版本」才计数，即两次 401 之间确实发生过一次成功 refresh，换了新 token 却仍 401。这精确
// 对应「refresh 端点成功但 grant 实质已被上游吊销」的 flap 盲区，把它和「token 只是过期
// 待刷新 / 并发同 token 突发 / 首次 401」区分开，避免把可恢复账号误判为永久禁用。
type OAuth401AfterRefreshCounterCache interface {
	// RecordOAuth401AfterRefresh 原子记录一次 401：
	//   - 首次（窗口内无 baseline 版本）→ 仅种下 baseline 版本，返回 0（不计数）。
	//   - tokenVersion > 已存版本（其间发生过成功 refresh）→ INCR 并返回当前计数。
	//   - tokenVersion == 已存版本（同 token / 并发突发）→ 仅刷新窗口，返回当前计数（不变）。
	//   - tokenVersion < 已存版本（请求快照过旧）→ 忽略，返回当前计数（不变）。
	// baseline 版本带 windowMinutes TTL：跨窗口的 401 会重新种 baseline 而非升级，
	// 防止几小时前的良性瞬时 401 和今天一次新瞬时 401 凑成误升级。
	RecordOAuth401AfterRefresh(ctx context.Context, accountID int64, tokenVersion int64, windowMinutes int) (int64, error)
	// ResetOAuth401AfterRefresh 在账号成功响应 / 恢复后清零计数与 baseline 版本。
	ResetOAuth401AfterRefresh(ctx context.Context, accountID int64) error
}
