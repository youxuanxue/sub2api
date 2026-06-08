package service

import "context"

// OAuth401AfterRefreshCounterCache 追踪 OAuth 账号在「grant 被上游吊销」两种特征下的 401 累计。
//
// 用 token 版本戳（credentials["_token_version"]，每次成功 refresh 盖一个 UnixMilli）做闸门，
// 同时维护两个互补的计数维度：
//
//   - versionBumpCount（refresh 成功却仍 401）：只有「本次 401 所用 token 的版本 > 上一次
//     401 记录的版本」才计数，即两次 401 之间确实换过新 token 却仍 401。对应「refresh 端点
//     成功但 grant 实质已被吊销」的 flap，把它和「过期待刷新 / 并发同 token 突发 / 首次 401」
//     区分开。
//   - sameVersionCount（token 仍有效却持续 401）：版本相等（其间没换 token）且距上次同版本
//     计数 ≥ debounce 秒时 +1。补前者的结构性盲区——grant 被吊销而 access_token 仍在有效期
//     内时 NeedsRefresh=false → 永不刷新 → 版本冻结 → versionBumpCount 永不前进。debounce
//     把一个冷却周期内的并发突发折叠成 1，使每个冷却周期至多 +1。
//
// 两维度同窗口 TTL（windowMinutes）：跨窗口的 401 重新种 baseline 而非升级，防止久远的良性
// 瞬时 401 与今天一次新瞬时 401 凑成误升级。调用方对两个计数各自配阈值，任一达阈值即升级。
type OAuth401AfterRefreshCounterCache interface {
	// RecordOAuth401AfterRefresh 原子记录一次 401，返回 (versionBumpCount, sameVersionCount)：
	//   - 首次（窗口内无 baseline）→ 种下 baseline 版本与时间戳，返回 (0, 0)。
	//   - tokenVersion > 已存版本（其间换过新 token）→ versionBumpCount+1，并把 sameVersion
	//     维度重置到新版本 baseline；返回更新后的 (versionBumpCount, 0)。
	//   - tokenVersion == 已存版本（同 token）→ 距上次同版本计数 ≥ debounceSeconds 才
	//     sameVersionCount+1，否则只刷新窗口（折叠并发突发）；返回当前 (versionBumpCount, sameVersionCount)。
	//   - tokenVersion < 已存版本（请求快照过旧）→ 忽略，返回当前 (versionBumpCount, sameVersionCount)。
	RecordOAuth401AfterRefresh(ctx context.Context, accountID int64, tokenVersion int64, windowMinutes, debounceSeconds int) (versionBumpCount int64, sameVersionCount int64, err error)
	// ResetOAuth401AfterRefresh 在账号成功响应 / 恢复后清零两维度计数与 baseline 版本。
	ResetOAuth401AfterRefresh(ctx context.Context, accountID int64) error
}
