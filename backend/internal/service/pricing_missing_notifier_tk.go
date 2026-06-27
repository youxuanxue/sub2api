package service

// TK: 缺价计费（pricing_missing_record_zero_cost）→ 飞书聚合告警。
//
// 背景（PR #675 遗留项）：catch-all 账号会把任意模型名转发到上游；若该模型没有
// 定价条目，两条计费 funnel（GatewayService.calculateRecordUsageCost 与
// OpenAIGatewayService.RecordUsage）会记一条零成本 usage log 并继续服务——即
// 免费用量 = 收入流失，此前只有结构化日志可见，无人主动通知。
//
// 设计决策：**不拒绝服务**。计费模型在请求前不可知（候选链含上游响应字段），
// 入口硬护栏 by-construction 不准；定价 ≠ 可服务（litellm 镜像滞后会把数据缺口
// 放大成客户侧故障）；servable 探测流水线也依赖真实请求穿过 catch-all。本通知器
// 只补"运营可见性"：缺价流量照常服务、照常记零成本日志，另路飞书告警提醒运营
// 热更定价（渠道定价 admin API，立即生效）并固化进 tk_pricing_overlay.json。
//
// 形态仿 account_incident_notifier_tk.go（#516），信噪比第一：
//   - 首见 (platform, model) → 即时一张橙头卡（24h 去重 + 每小时滑窗限量），
//     运营第一时间知道有新缺价模型在跑零成本流量。
//   - 全量事件进聚合 buffer，由后台 ticker 按
//     feishu.pricing_missing_digest_seconds（默认 1800s）flush 一条摘要——
//     这是"运营配置动作"级别的提醒，不是 P0 故障流。
//   - 运营补价后 ErrModelPricingUnavailable 不再触发，告警自然停止，无需手动清除。
//
// 唯一挂钩点是两条计费 funnel 的 pricing-missing 分支（见
// gateway_service_tk_billing_pricing_missing.go 与
// openai_gateway_service_tk_pricing_missing.go）；既有日志行保持不动——#675 的
// 探测交叉核验与 ops 工具仍 grep 它。单副本 Stage0、无 leader，挂钩点直接发
// 不会跨节点重复。

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	// 首见即时卡同 (site, platform, model) 的去重窗口。
	pricingMissingFirstSeenDedupeWindow = 24 * time.Hour
	// 首见即时卡每小时最多条数（滑动窗口防爆量——catch-all 被异常客户端
	// 喷洒大量不同模型名时退化为只看摘要）。
	pricingMissingFirstSeenRatePerHour = 10
	// 聚合摘要 flush 间隔兜底值（配置缺失时）。运营配置动作级别，半小时足够。
	pricingMissingDigestSecondsFallback = 1800
	// 摘要里每个 (platform, model) 展示的组名样例上限。
	pricingMissingDigestMaxGroupSamples = 8
)

// PricingMissingEvent 是单次"已服务但零计费"事件的最小快照。
type PricingMissingEvent struct {
	// Reason 是漏算原因，驱动卡片文案：
	//   "unpriced"            — 倍率前 TotalCost==0，价格本身为零（无价模型/渠道$0/
	//                           视频·per_request 无价/cost-calc 错误吞 $0）。
	//   "negative_multiplier" — 价格有效但被负倍率归零。
	// 空字符串向后兼容，按 "unpriced" 处理。
	Reason         string
	Platform       string // group 平台（anthropic/openai/gemini/newapi/...）
	BillingModel   string // 计费解析最终落到的模型名（聚合键）
	RequestedModel string // 客户端请求的模型名（样例展示）
	UpstreamModel  string // 上游实际服务的模型名（样例展示）
	GroupID        int64
	GroupName      string
	APIKeyID       int64
	Tokens         int64 // 本次未计费计费单元估算（token 总量或图片张数）
}

// pricingMissingReasonLabel 把 Reason 码翻成中文卡片文案；空/未知按无价处理。
func pricingMissingReasonLabel(reason string) string {
	switch reason {
	case "negative_multiplier":
		return "负倍率归零（价格有效但被负费率倍率清零）"
	case tkPricedServingGateRejectReason:
		// 运行期价格闸拒绝：与「已服务零计费」不同——该请求被 404 拒掉、未服务客户，
		// 运维补价后即可放行（docs/approved/priced-or-it-doesnt-ship.md）。
		return "模型未定价被准入闸拒绝（已返回 404、未服务；补价后放行）"
	case tkServedAtFallbackReason:
		// 按家族兜底 floor 计费（非真价、非 $0、未拒客户）。设计转向后的收敛信号：
		// 补真价后自动改用真价，fallback 用量衰减到稳态（docs §4）。
		return "模型按家族兜底价(floor)服务、非真价（未拒客户、未漏 $0；补真价后改用真价）"
	default:
		return "模型无价（倍率前成本为零）"
	}
}

// PricingMissingNotifier 是计费 funnel 注入的最小通知面（仿 AccountIncidentNotifier）。
type PricingMissingNotifier interface {
	NotifyPricingMissing(ev PricingMissingEvent)
}

// pricingMissingDigestEntry 是聚合 buffer 的单个 (platform, model) 条目。
type pricingMissingDigestEntry struct {
	platform       string
	billingModel   string
	requestedModel string // 首个样例
	upstreamModel  string // 首个样例
	count          int
	tokens         int64
	groupIDs       map[int64]struct{}
	groupSamples   []string
	apiKeyIDs      map[int64]struct{}
	firstAt        time.Time
	lastAt         time.Time
}

type TKPricingMissingNotifier struct {
	cfgProvider opsFeishuConfigProvider
	httpClient  opsFeishuHTTPDoer
	siteID      string
	now         func() time.Time

	mu           sync.Mutex
	firstSentAt  map[string]time.Time // 首见即时卡去重: site|platform|model -> 上次发送
	firstLimiter *slidingWindowLimiter
	digest       map[string]*pricingMissingDigestEntry

	stopCh   chan struct{}
	stopOnce sync.Once
}

func newTKPricingMissingNotifier(cfgProvider opsFeishuConfigProvider, siteID string) *TKPricingMissingNotifier {
	n := &TKPricingMissingNotifier{
		cfgProvider:  cfgProvider,
		httpClient:   &http.Client{Timeout: opsFeishuWebhookTimeout},
		siteID:       strings.TrimSpace(siteID),
		now:          time.Now,
		firstSentAt:  map[string]time.Time{},
		firstLimiter: newSlidingWindowLimiter(pricingMissingFirstSeenRatePerHour, time.Hour),
		digest:       map[string]*pricingMissingDigestEntry{},
		stopCh:       make(chan struct{}),
	}
	if n.siteID == "" {
		n.siteID = "unknown"
	}
	return n
}

// Start 启动后台聚合 flush ticker。必须配对 Stop()。
func (n *TKPricingMissingNotifier) Start() {
	if n == nil {
		return
	}
	go n.digestLoop()
}

// Stop 优雅停 ticker，供 wire cleanup 调用。幂等。
func (n *TKPricingMissingNotifier) Stop() {
	if n == nil {
		return
	}
	n.stopOnce.Do(func() {
		close(n.stopCh)
	})
}

// NotifyPricingMissing 登记一次缺价事件：写聚合 buffer；首见 (platform, model)
// 额外发一张即时卡。同步路径只做内存操作，发送全部异步，绝不阻塞计费 funnel。
func (n *TKPricingMissingNotifier) NotifyPricingMissing(ev PricingMissingEvent) {
	if n == nil {
		return
	}
	platform := strings.TrimSpace(strings.ToLower(ev.Platform))
	model := strings.TrimSpace(ev.BillingModel)
	if model == "" {
		model = strings.TrimSpace(ev.RequestedModel)
	}
	if model == "" {
		return
	}
	if platform == "" {
		platform = "unknown"
	}
	now := n.currentTime()
	key := platform + "\x1f" + strings.ToLower(model)

	n.mu.Lock()
	entry := n.digest[key]
	if entry == nil {
		entry = &pricingMissingDigestEntry{
			platform:       platform,
			billingModel:   model,
			requestedModel: strings.TrimSpace(ev.RequestedModel),
			upstreamModel:  strings.TrimSpace(ev.UpstreamModel),
			groupIDs:       map[int64]struct{}{},
			apiKeyIDs:      map[int64]struct{}{},
			firstAt:        now,
		}
		n.digest[key] = entry
	}
	entry.count++
	entry.tokens += ev.Tokens
	entry.lastAt = now
	if ev.GroupID > 0 {
		if _, ok := entry.groupIDs[ev.GroupID]; !ok {
			entry.groupIDs[ev.GroupID] = struct{}{}
			if len(entry.groupSamples) < pricingMissingDigestMaxGroupSamples {
				entry.groupSamples = append(entry.groupSamples, pricingMissingGroupLabel(ev.GroupID, ev.GroupName))
			}
		}
	}
	if ev.APIKeyID > 0 {
		entry.apiKeyIDs[ev.APIKeyID] = struct{}{}
	}

	// 首见即时卡判定（持锁只做去重 + 限量记账）。
	dedupeKey := n.siteID + "|" + key
	if last, seen := n.firstSentAt[dedupeKey]; seen && now.Sub(last) < pricingMissingFirstSeenDedupeWindow {
		n.mu.Unlock()
		return
	}
	if n.firstLimiter != nil && !n.firstLimiter.Allow(now) {
		n.mu.Unlock()
		return
	}
	n.firstSentAt[dedupeKey] = now
	n.mu.Unlock()

	title := fmt.Sprintf("TokenKey P0 计费漏算：已服务但零计费 [%s]", n.siteID)
	body := buildPricingMissingFirstSeenText(n.siteID, ev, platform, model, now)
	n.send(title, "red", body, fmt.Sprintf("reason=%s platform=%s model=%s", ev.Reason, platform, model))
}

func (n *TKPricingMissingNotifier) digestLoop() {
	defer func() {
		if r := recover(); r != nil {
			logger.LegacyPrintf("service.pricing_missing", "[PricingMissing] digest loop panic recovered: %v", r)
		}
	}()
	for {
		timer := time.NewTimer(n.digestInterval())
		select {
		case <-n.stopCh:
			timer.Stop()
			return
		case <-timer.C:
			n.flushDigest()
			n.pruneFirstSeen()
		}
	}
}

// digestInterval 从配置读 flush 间隔（秒），下限 30s，缺失则兜底 1800s。
func (n *TKPricingMissingNotifier) digestInterval() time.Duration {
	secs := pricingMissingDigestSecondsFallback
	if n.cfgProvider != nil {
		if cfg, err := n.cfgProvider.GetEmailNotificationConfig(context.Background()); err == nil && cfg != nil && cfg.Feishu.PricingMissingDigestSeconds > 0 {
			secs = cfg.Feishu.PricingMissingDigestSeconds
		}
	}
	if secs < 30 {
		secs = 30
	}
	return time.Duration(secs) * time.Second
}

// flushDigest 取出并清空 buffer，有内容则异步发一条摘要；空则跳过。
// panic 就地兜住，不传播到 digestLoop（同 account incident 的理由）。
func (n *TKPricingMissingNotifier) flushDigest() {
	defer func() {
		if r := recover(); r != nil {
			logger.LegacyPrintf("service.pricing_missing", "[PricingMissing] flushDigest panic recovered: %v", r)
		}
	}()
	n.mu.Lock()
	if len(n.digest) == 0 {
		n.mu.Unlock()
		return
	}
	entries := make([]*pricingMissingDigestEntry, 0, len(n.digest))
	for _, e := range n.digest {
		entries = append(entries, e)
	}
	n.digest = map[string]*pricingMissingDigestEntry{}
	n.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].platform != entries[j].platform {
			return entries[i].platform < entries[j].platform
		}
		return entries[i].billingModel < entries[j].billingModel
	})
	now := n.currentTime()
	title := fmt.Sprintf("TokenKey 已服务零计费摘要 [%s]", n.siteID)
	body := buildPricingMissingDigestText(n.siteID, entries, now)
	n.send(title, "orange", body, "digest")
}

// pruneFirstSeen 修剪过期的首见去重台账（超出去重窗口的条目）。
func (n *TKPricingMissingNotifier) pruneFirstSeen() {
	now := n.currentTime()
	n.mu.Lock()
	defer n.mu.Unlock()
	for k, t := range n.firstSentAt {
		if now.Sub(t) >= pricingMissingFirstSeenDedupeWindow {
			delete(n.firstSentAt, k)
		}
	}
}

// send 异步发送（绝不阻塞计费 funnel / flush goroutine）。
func (n *TKPricingMissingNotifier) send(title, headerTemplate, body, logCtx string) {
	go n.sendNow(title, headerTemplate, body, logCtx)
}

// sendNow 同步发送一条飞书卡片。独立 5s ctx，不继承请求 ctx；panic recover。
func (n *TKPricingMissingNotifier) sendNow(title, headerTemplate, body, logCtx string) {
	defer func() {
		if r := recover(); r != nil {
			logger.LegacyPrintf("service.pricing_missing", "[PricingMissing] send panic recovered (%s): %v", logCtx, r)
		}
	}()
	if n == nil || n.cfgProvider == nil {
		return
	}
	cfg, err := n.cfgProvider.GetEmailNotificationConfig(context.Background())
	if err != nil || cfg == nil || !cfg.Feishu.Enabled || strings.TrimSpace(cfg.Feishu.WebhookURL) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), opsFeishuWebhookTimeout)
	defer cancel()
	payload := feishuCardPayload(cfg.Feishu, n.now, headerTemplate, title, body)
	if err := sendFeishuPayload(ctx, n.httpClient, cfg.Feishu, payload); err != nil {
		logger.LegacyPrintf("service.pricing_missing", "[PricingMissing] feishu send failed (%s): %s", logCtx, err.Error())
	}
}

func (n *TKPricingMissingNotifier) currentTime() time.Time {
	if n != nil && n.now != nil {
		return n.now()
	}
	return time.Now()
}

// pricingMissingActionSteps 是各类卡片共用的补价动作（与「是否已服务客户」无关）。
const pricingMissingActionSteps = "运营动作：\n" +
	"1. 热更止血：`python3 ops/pricing/apply-pricing-hotfix.py lookup --model <模型名>` 取价，再 `apply` 经 admin API 写入渠道定价（立即生效，无需发版）；\n" +
	"2. 固化：`stage-overlay` 把 fill-only 条目写入 `tk_pricing_overlay.json` 提 PR（litellm 镜像补上后自动让位）。"

// pricingMissingSituationText 按 Reason 给出「这次到底发生了什么」。运行期价格闸拒绝是
// 404、未服务客户、未记账——与「已服务零计费」是相反的客户影响，绝不能共用「已照常服务」
// 措辞（否则运维会把一次真实的 404 拒绝当成无害的零计费日志而低估）。
func pricingMissingSituationText(reason string) string {
	switch reason {
	case tkPricedServingGateRejectReason:
		return "说明：该请求已被运行期价格闸**返回 404 拒绝**（未服务客户、未记账）；补价后下次请求即放行。"
	case tkServedAtFallbackReason:
		return "说明：该请求已按**家族兜底价 floor 计费**（非真价、非 $0、未拒客户）；补真价后自动改用真价。"
	default:
		return "说明：该流量**已照常服务、按零成本记录**（未拒绝客户）。"
	}
}

// pricingMissingFirstSeenHeadline 按 Reason 给首见卡的一句话归纳（served vs rejected）。
func pricingMissingFirstSeenHeadline(reason string) string {
	switch reason {
	case tkPricedServingGateRejectReason:
		return "首次发现该（platform, model）被价格闸拒绝（已返回 404、未服务）"
	case tkServedAtFallbackReason:
		return "首次发现该（platform, model）按家族兜底 floor 计费（非真价）"
	default:
		return "首次发现该（platform, model）已服务却零计费"
	}
}

func buildPricingMissingFirstSeenText(site string, ev PricingMissingEvent, platform, model string, now time.Time) string {
	requested := strings.TrimSpace(ev.RequestedModel)
	if requested == "" {
		requested = "-"
	}
	upstream := strings.TrimSpace(ev.UpstreamModel)
	if upstream == "" {
		upstream = "-"
	}
	group := "-"
	if ev.GroupID > 0 {
		group = pricingMissingGroupLabel(ev.GroupID, ev.GroupName)
	}
	return fmt.Sprintf("**节点**：%s\n**原因**：%s\n**平台**：%s\n**计费模型**：%s\n**请求模型**：%s\n**上游模型**：%s\n**组**：%s\n**api_key**：#%d\n**本次计费单元**：%d\n**时间**：%s\n\n%s（24h 内同模型不再即时提醒，后续进周期摘要）。\n\n%s\n%s",
		escapeFeishuText(site),
		escapeFeishuText(pricingMissingReasonLabel(ev.Reason)),
		escapeFeishuText(platform),
		escapeFeishuText(model),
		escapeFeishuText(requested),
		escapeFeishuText(upstream),
		escapeFeishuText(group),
		ev.APIKeyID,
		ev.Tokens,
		escapeFeishuText(formatAlertTime(now)),
		pricingMissingFirstSeenHeadline(ev.Reason),
		pricingMissingSituationText(ev.Reason),
		pricingMissingActionSteps,
	)
}

func buildPricingMissingDigestText(site string, entries []*pricingMissingDigestEntry, now time.Time) string {
	lines := make([]string, 0, len(entries)+2)
	lines = append(lines, fmt.Sprintf("**节点**：%s\n**时间**：%s\n\n缺价模型零成本流量摘要：",
		escapeFeishuText(site), escapeFeishuText(formatAlertTime(now))))
	for _, e := range entries {
		samples := strings.Join(e.groupSamples, ", ")
		if len(e.groupIDs) > len(e.groupSamples) {
			samples += fmt.Sprintf(" 等共%d个", len(e.groupIDs))
		}
		if samples == "" {
			samples = "-"
		}
		lines = append(lines, fmt.Sprintf("- **%s / %s**：%d 次 / 约 %s tokens 未计费 / %d 个 key / 组：%s",
			escapeFeishuText(e.platform),
			escapeFeishuText(e.billingModel),
			e.count,
			formatPricingMissingTokens(e.tokens),
			len(e.apiKeyIDs),
			escapeFeishuText(samples)))
	}
	lines = append(lines, "\n"+pricingMissingActionSteps)
	return strings.Join(lines, "\n")
}

func pricingMissingGroupLabel(id int64, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Sprintf("#%d", id)
	}
	return fmt.Sprintf("%s(#%d)", name, id)
}

// formatPricingMissingTokens 把 token 数格式化成人类可读量级（1.2k / 3.4M）。
func formatPricingMissingTokens(t int64) string {
	switch {
	case t >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(t)/1_000_000)
	case t >= 1_000:
		return fmt.Sprintf("%.1fk", float64(t)/1_000)
	default:
		return fmt.Sprintf("%d", t)
	}
}
