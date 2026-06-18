package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

// pool_load_rate — 池级并发负载率，"账号池总容量触顶"的前瞻信号。
//
// 设计来由（2026-06）：既有告警只有「平台池全不可调度」即时 P0（空池火警，
// account_incident_notifier_tk_pool.go）——那是 0 席位、用户已在吃 429 的「火警」。
// 缺一个「烟雾报警器」：在席位被打满之前就发现某调度池正在逼近饱和，触发「补号」。
// 上游请求暴涨 / 排队 / 容量触顶其实是同一件事的因/症/果，用一个归一化比值统一覆盖：
//
//	LoadRate = (在途 + 排队) / Σ席位      （>100% 即已开始排队）
//
//   - 前瞻：90% 就响，早于空池火警；
//   - 归一化：比值而非绝对 QPS，号池超配不误报、欠配不漏报；
//   - 池级：按调度可替代边界聚合，不是单账号噪音。
//
// 池的边界 = 调度可替代边界 = (platform, group_id, channel_type)。聚合得比调度
// 能替代的范围粗一格，就会用一个空闲池掩盖一个已死的池。channel_type 对四个原生
// 平台恒为 0（自然收敛成 platform×group）；仅第五平台 newapi 起作用——deepseek
// (ch43) / qwen(ch17) / volcengine(ch45) 互不可替代，揉成一个「newapi 负载率」会把
// deepseek 的饱和平均进 volcengine 的空闲里（会骗人的均值）。

// poolLoadMinAccounts 是一个池计入「池级容量饱和」的最小账号数。单账号池的 100%
// 就是「单账号触顶」，由单账号事件摘要 + 空池 P0 兜底，不该污染池级信号——用户明确
// 要「非单个账号触顶噪音」。
const poolLoadMinAccounts = 2

// poolLoadTopCauseMax 是飞书卡片「主因」里最多展开几个最饱和池（卡片回答「哪几个池」
// 而非「所有池」）。
const poolLoadTopCauseMax = 3

// PoolLoad 是一个调度池的并发负载快照。
type PoolLoad struct {
	Platform    string
	GroupID     int64
	ChannelType int
	Accounts    int     // 池内可调度账号数
	Seats       int     // Σ 有界并发上限（分母）
	InFlight    int     // Σ 当前在途并发
	Waiting     int     // Σ 排队等待槽位的请求数
	LoadRatePct float64 // (InFlight+Waiting)/Seats*100，可 >100（已排队）
}

// ComputePoolLoadRates 按 (platform, group_id, channel_type) 派生每个调度池的并发
// 负载率。读路径与 collectConcurrencyQueueDepth 同源（ListSchedulable +
// GetAccountsLoadBatch，CurrentConcurrency/WaitingCount 是实测值，与传入 cap 无关）。
//
// 只返回「可饱和且有意义」的池：
//   - 账号数 ≥ poolLoadMinAccounts（排除单账号噪音）；
//   - 所有成员都有有界并发上限——任一成员并发无上限（Concurrency<=0 且无 LoadFactor）
//     则该池可无限吸收、永不饱和，整池排除。
//
// best-effort：依赖缺失/查询失败返回 error，让调用方跳过本轮（不误清告警）。
func (s *OpsService) ComputePoolLoadRates(ctx context.Context) ([]PoolLoad, error) {
	if s == nil || s.accountRepo == nil || s.concurrencyService == nil {
		return nil, errors.New("pool load rate: missing dependencies")
	}
	accounts, err := s.accountRepo.ListSchedulable(ctx)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, nil
	}

	batch := make([]AccountWithConcurrency, 0, len(accounts))
	for i := range accounts {
		acc := &accounts[i]
		if acc.ID <= 0 {
			continue
		}
		batch = append(batch, AccountWithConcurrency{ID: acc.ID, MaxConcurrency: acc.EffectiveLoadFactor()})
	}
	loadMap, err := s.concurrencyService.GetAccountsLoadBatch(ctx, batch)
	if err != nil {
		return nil, err
	}
	return aggregatePoolLoads(accounts, loadMap), nil
}

// aggregatePoolLoads 是纯聚合逻辑（不碰 IO，便于单测）：把可调度账号 + 每账号
// 在途/排队读数，按 (platform, group_id, channel_type) 折成各调度池的负载率，
// 并按 LoadRate 降序返回合格池（见 ComputePoolLoadRates 的合格判据）。
func aggregatePoolLoads(accounts []Account, loadMap map[int64]*AccountLoadInfo) []PoolLoad {
	type poolAgg struct {
		platform    string
		groupID     int64
		channelType int
		accounts    int
		seats       int
		inFlight    int
		waiting     int
		unbounded   bool
	}
	pools := map[string]*poolAgg{}

	for i := range accounts {
		acc := &accounts[i]
		if acc.ID <= 0 {
			continue
		}
		// 不属于任何分组的账号收不到调度流量，不计入任何池。
		if len(acc.GroupIDs) == 0 {
			continue
		}
		capacity, bounded := accountConcurrencyCap(acc)
		inFlight, waiting := 0, 0
		if info := loadMap[acc.ID]; info != nil {
			inFlight = info.CurrentConcurrency
			waiting = info.WaitingCount
		}
		platform := strings.TrimSpace(acc.Platform)
		// 账号可在多个分组 → 对每个分组各计一次（每个分组的可替代集合各自成池）。
		for _, gid := range acc.GroupIDs {
			key := fmt.Sprintf("%s|%d|%d", platform, gid, acc.ChannelType)
			p := pools[key]
			if p == nil {
				p = &poolAgg{platform: platform, groupID: gid, channelType: acc.ChannelType}
				pools[key] = p
			}
			p.accounts++
			p.inFlight += inFlight
			p.waiting += waiting
			if bounded {
				p.seats += capacity
			} else {
				p.unbounded = true
			}
		}
	}

	out := make([]PoolLoad, 0, len(pools))
	for _, p := range pools {
		if p.unbounded || p.seats <= 0 {
			continue
		}
		if p.accounts < poolLoadMinAccounts {
			continue
		}
		out = append(out, PoolLoad{
			Platform:    p.platform,
			GroupID:     p.groupID,
			ChannelType: p.channelType,
			Accounts:    p.accounts,
			Seats:       p.seats,
			InFlight:    p.inFlight,
			Waiting:     p.waiting,
			LoadRatePct: float64(p.inFlight+p.waiting) / float64(p.seats) * 100,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LoadRatePct > out[j].LoadRatePct })
	return out
}

// accountConcurrencyCap 返回账号的真实有界并发上限。EffectiveLoadFactor() 会把无界
// 账号折成 1，无法判别无界——这里读原始字段：无正向上限即视为无界。
func accountConcurrencyCap(a *Account) (int, bool) {
	if a == nil {
		return 0, false
	}
	if a.LoadFactor != nil && *a.LoadFactor > 0 {
		return *a.LoadFactor, true
	}
	if a.Concurrency > 0 {
		return a.Concurrency, true
	}
	return 0, false
}

// poolMatchesScope 过滤规则 scope（platform / group_id）。channel_type 永不进 scope
// （规则 filters 无此维度）——所以一条 newapi 规则天然覆盖它的所有 channel 子池。
func poolMatchesScope(p PoolLoad, platform string, groupID *int64) bool {
	if platform = strings.TrimSpace(platform); platform != "" && p.Platform != platform {
		return false
	}
	if groupID != nil && *groupID > 0 && p.GroupID != *groupID {
		return false
	}
	return true
}

// maxPoolLoadRate 返回 scope 内最饱和池的负载率。found=false 表示 scope 内没有合格池
// （全无界 / 全单账号）→ 调用方按「无饱和」处理。
func maxPoolLoadRate(pools []PoolLoad, platform string, groupID *int64) (float64, bool) {
	maxRate := 0.0
	found := false
	for _, p := range pools {
		if !poolMatchesScope(p, platform, groupID) {
			continue
		}
		if !found || p.LoadRatePct > maxRate {
			maxRate = p.LoadRatePct
			found = true
		}
	}
	return maxRate, found
}

// formatPoolLoadCause 渲染飞书卡片「主因」：scope 内、越过规则阈值的最饱和的几个池。
// 例如：newapi/分组11/DeepSeek 96%（在途7+排队5/席位12·账号3·排队中） · openai/分组2 91%（…）
// pools 已按 LoadRate 降序，取 top N。
func formatPoolLoadCause(pools []PoolLoad, rule *OpsAlertRule, platform string, groupID *int64) string {
	parts := make([]string, 0, poolLoadTopCauseMax)
	for _, p := range pools {
		if !poolMatchesScope(p, platform, groupID) {
			continue
		}
		if rule != nil && !compareMetric(p.LoadRatePct, rule.Operator, rule.Threshold) {
			continue
		}
		queued := ""
		if p.Waiting > 0 {
			queued = "·排队中"
		}
		parts = append(parts, fmt.Sprintf("%s %.0f%%（在途%d+排队%d/席位%d·账号%d%s）",
			poolLabel(p), p.LoadRatePct, p.InFlight, p.Waiting, p.Seats, p.Accounts, queued))
		if len(parts) >= poolLoadTopCauseMax {
			break
		}
	}
	return strings.Join(parts, " · ")
}

// poolLabel 给池一个可读标签。newapi 子池带 channel 名（deepseek/qwen/…），其余平台
// channel_type=0 不显示。
func poolLabel(p PoolLoad) string {
	plat := strings.TrimSpace(p.Platform)
	if plat == "" {
		plat = "(unknown)"
	}
	label := fmt.Sprintf("%s/分组%d", plat, p.GroupID)
	if p.ChannelType > 0 {
		if name := strings.TrimSpace(newapi.ChannelTypeName(p.ChannelType)); name != "" {
			label += "/" + name
		} else {
			label += fmt.Sprintf("/渠道%d", p.ChannelType)
		}
	}
	return label
}
