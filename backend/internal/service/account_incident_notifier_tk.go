package service

// TK: 账号失效事件 → 飞书即时告警（分级两形态）。
//
// 设计见 docs / plan「账号失效事件 → 飞书告警」。核心:
//   - 永久失效（status=error 终态，需人工重新 OAuth / 查 bug）→ 即时单条 P0 卡片（红头）。
//   - 临时冷却（429/529/oauth_401 临时/403temp/temp，自愈）→ 不逐条发,写入聚合 buffer,
//     由后台 ticker 按 feishu.account_incident_digest_seconds（默认 600s）flush 一条摘要（橙头）。
// 信噪比第一: 自愈类绝不和"账号挂了"挤在同一个即时 P0 流里淹没真故障。
//
// 唯一挂钩点是 RateLimitService.notifyAccountSchedulingBlocked（见 ratelimit_service_tk_incident.go）。
// 单副本 Stage0、无 leader,挂钩点直接发不会跨节点重复。

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// AccountIncidentKind 区分账号失效事件的两种形态。
type AccountIncidentKind int

const (
	IncidentKindUnknown           AccountIncidentKind = iota
	IncidentKindPermanentDisable                      // status=error 终态,需人工
	IncidentKindTemporaryCooldown                     // 429/529/temp,自愈,聚合摘要
)

// AccountIncidentNotifier 是注入给 RateLimitService 的最小通知面（仿 AccountRuntimeBlocker）。
type AccountIncidentNotifier interface {
	// detail (variadic, optional) is an upstream-dimension hint — e.g. the
	// Anthropic 5h/7d usage window or the rate-limited model class — that the
	// temporary-cooldown digest renders so operators see WHICH upstream limit
	// fired. Variadic keeps non-enriched call sites untouched.
	NotifyAccountIncident(account *Account, until time.Time, reason string, kind AccountIncidentKind, detail ...string)
	// NotifyAccountRecovered 在账号"真实清除事件"(ClearRateLimit / RecoverAccountState /
	// admin 重测恢复)时调用,对此前告警过的账号发一条即时恢复绿卡。事件驱动:纯定时器
	// 到期自愈不经此路径,也不播报(原冷却卡已写明 until)。未告警账号调用为 no-op。
	NotifyAccountRecovered(accountID int64)
	// NotifyPlatformPoolExhausted 在某平台可调度账号数降为 0 时发即时 P0 卡片
	// （事件驱动,由账号冷却汇聚点触发的池级检查上报;trigger 是压垮池的最后一个
	// 账号）。实现侧按 platform 去重防 flap 刷屏。见 account_incident_notifier_tk_pool.go。
	NotifyPlatformPoolExhausted(platform string, trigger *Account, until time.Time, reason string)
}

const (
	// 永久失效同 (site,account,reasonClass) 的去重窗口,防极端刷屏。
	accountIncidentPermanentDedupeWindow = time.Hour
	// 永久失效卡片每小时最多条数（滑动窗口防爆量）。
	accountIncidentPermanentRatePerHour = 30
	// 聚合摘要 flush 间隔的兜底值（配置缺失时）。
	accountIncidentDigestSecondsFallback = 600
	// 摘要里每个 reasonClass 展示的账号名样例上限。
	accountIncidentDigestMaxSamples = 8
	// 同账号恢复绿卡去重窗口,防 admin 批量恢复 / 重复 clear 刷屏。
	accountIncidentRecoveryDedupeWindow = 5 * time.Minute
	// 活跃台账内存修剪:临时冷却 until 过期超过此宽限仍无 clear 事件 → 静默删除(不发卡)。
	accountIncidentActiveStaleGrace = time.Hour
)

// activeIncident 是"当前处于告警状态"的账号事件台账条目,用于事件驱动恢复 + 内存修剪。
// 仅记录,不参与时间触发恢复;until 仅供 pruneStaleActive 内存修剪用(永久失效 until 为零值)。
type activeIncident struct {
	label       string
	reasonClass string
	kindZh      string
	kind        AccountIncidentKind
	until       time.Time
}

// incidentClass 是 reason 经分类后的派生信息。
type incidentClass struct {
	alert       bool
	kind        AccountIncidentKind
	reasonClass string // 去重 / 聚合分组键
	kindZh      string // 中文事件类型
	advice      string // 运营建议动作
}

// classifyIncident 把底层 reason 字符串映射成 (形态, 去重类, 中文文案, 建议)。
// reason 精确匹配优先（覆盖全部已知挂钩点）;未知 reason 时用显式 kind + until 兜底。
func classifyIncident(reason string, until time.Time, kind AccountIncidentKind) incidentClass {
	switch strings.TrimSpace(strings.ToLower(reason)) {
	case "auth_error":
		return incidentClass{true, IncidentKindPermanentDisable, "auth", "账号永久失效（认证失败）", "立即重新 OAuth 授权该账号"}
	case "custom_error_code":
		return incidentClass{true, IncidentKindPermanentDisable, "custom_code", "账号被自定义错误码罚下", "检查命中的自定义错误码规则与上游响应"}
	case "stream_timeout_error":
		return incidentClass{true, IncidentKindPermanentDisable, "stream_timeout", "账号永久失效（流超时累计）", "检查上游连通性后重测该账号"}
	case "oauth_401":
		return incidentClass{true, IncidentKindTemporaryCooldown, "oauth401", "OAuth 401 临时冷却", "观察是否升级为永久失效;检查 token 刷新链路"}
	case "429", "429_fallback":
		return incidentClass{true, IncidentKindTemporaryCooldown, "429", "限流冷却（429）", "容量/节奏问题,关注是否反复触发"}
	case "529":
		return incidentClass{true, IncidentKindTemporaryCooldown, "529", "过载冷却（529）", "上游过载,通常自愈"}
	case "openai_403_temp":
		return incidentClass{true, IncidentKindTemporaryCooldown, "403", "OpenAI 403 临时冷却", "检查 IP/账号风控状态"}
	case "temp_unschedulable", "stream_timeout_temp_unschedulable":
		return incidentClass{true, IncidentKindTemporaryCooldown, "temp", "临时不可调度", "观察是否自愈"}
	case "429_model_class":
		// G4(#600)模型维度 cooldown：单模型类(如 opus)打穿 5h/7d 用量窗口,只冷却该模型类,
		// 账号其它模型仍可调度。不能复用兜底的"账号临时冷却"——那会误报成整账号下线。
		return incidentClass{true, IncidentKindTemporaryCooldown, "429_model_class", "模型类限流冷却（账号其它模型仍可调度）", "单模型类(如 opus)用量窗口耗尽;账号其它模型不受影响,关注是否反复触发"}
	}
	// 未知 reason: 显式 kind 优先,缺省再看 until。
	k := kind
	if k == IncidentKindUnknown {
		if until.IsZero() {
			k = IncidentKindPermanentDisable
		} else {
			k = IncidentKindTemporaryCooldown
		}
	}
	if k == IncidentKindPermanentDisable {
		return incidentClass{true, IncidentKindPermanentDisable, "other", "账号永久失效", "人工检查该账号（可能需重新授权）"}
	}
	return incidentClass{true, IncidentKindTemporaryCooldown, "other", "账号临时冷却", "观察是否自愈"}
}

// accountIncidentDigestEntry 是临时冷却聚合 buffer 的单个 (reasonClass × detail) 条目。
// detail 是上游限流维度提示（如 Anthropic 5h/7d 用量窗口、被限流的模型类），用于把同一
// reasonClass 下不同维度（如 opus·5h 与 sonnet·7d）拆成独立行,让运营一眼看清是哪个上游
// 维度触发的冷却。空 detail 退化为按 reasonClass 聚合（与历史行为一致）。
type accountIncidentDigestEntry struct {
	reasonClass    string
	detail         string
	kindZh         string
	advice         string
	accountIDs     map[int64]struct{}
	accountSamples []string
	count          int
	firstAt        time.Time
	lastAt         time.Time
}

// accountIncidentDigestKey 合成 digest 聚合键。detail 为空时退化为 reasonClass,保持历史
// 键名（如 "429"）不变,使既有按 reasonClass 取数的调用与测试不受影响。
func accountIncidentDigestKey(reasonClass, detail string) string {
	if detail == "" {
		return reasonClass
	}
	return reasonClass + "\x1f" + detail
}

// opsFeishuConfigProvider 是 notifier 读取飞书配置的最小面（*OpsService 天然满足）。
// 收成接口便于单测注入,而不必搭建整个 OpsService + setting repo。
type opsFeishuConfigProvider interface {
	GetEmailNotificationConfig(ctx context.Context) (*OpsEmailNotificationConfig, error)
}

type TKAccountIncidentNotifier struct {
	cfgProvider opsFeishuConfigProvider
	httpClient  opsFeishuHTTPDoer
	siteID      string
	now         func() time.Time

	mu                sync.Mutex
	permSentAt        map[string]time.Time                   // 永久失效去重: key -> 上次发送
	permLimiter       *slidingWindowLimiter                  // 永久失效防爆量
	digest            map[string]*accountIncidentDigestEntry // reasonClass -> 聚合条目
	active            map[int64]map[string]*activeIncident   // accountID -> reasonClass -> 活跃事件(恢复台账)
	recoverySentAt    map[int64]time.Time                    // 恢复绿卡去重: accountID -> 上次发送
	poolExhaustSentAt map[string]time.Time                   // 平台池全不可调度 P0 去重: platform -> 上次发送

	stopCh   chan struct{}
	stopOnce sync.Once
}

func newTKAccountIncidentNotifier(cfgProvider opsFeishuConfigProvider, siteID string) *TKAccountIncidentNotifier {
	n := &TKAccountIncidentNotifier{
		cfgProvider:       cfgProvider,
		httpClient:        &http.Client{Timeout: opsFeishuWebhookTimeout},
		siteID:            strings.TrimSpace(siteID),
		now:               time.Now,
		permSentAt:        map[string]time.Time{},
		permLimiter:       newSlidingWindowLimiter(accountIncidentPermanentRatePerHour, time.Hour),
		digest:            map[string]*accountIncidentDigestEntry{},
		active:            map[int64]map[string]*activeIncident{},
		recoverySentAt:    map[int64]time.Time{},
		poolExhaustSentAt: map[string]time.Time{},
		stopCh:            make(chan struct{}),
	}
	if n.siteID == "" {
		n.siteID = "unknown"
	}
	return n
}

// Start 启动后台聚合 flush ticker。必须配对 Stop()。
func (n *TKAccountIncidentNotifier) Start() {
	if n == nil {
		return
	}
	go n.digestLoop()
}

// Stop 优雅停 ticker,供 wire cleanup 调用。幂等。
func (n *TKAccountIncidentNotifier) Stop() {
	if n == nil {
		return
	}
	n.stopOnce.Do(func() {
		close(n.stopCh)
	})
}

func (n *TKAccountIncidentNotifier) NotifyAccountIncident(account *Account, until time.Time, reason string, kind AccountIncidentKind, detail ...string) {
	if n == nil || account == nil {
		return
	}
	cls := classifyIncident(reason, until, kind)
	if !cls.alert {
		return
	}
	if cls.kind == IncidentKindTemporaryCooldown && !n.temporaryDigestEnabled() {
		// 自愈类临时冷却摘要默认关（opt-in）：不记恢复台账、不入 digest buffer、不发摘要。
		// 临时冷却卡片本身已写明 until，自愈静默即可。池级全不可调度 P0
		// （NotifyPlatformPoolExhausted）、永久失效 P0（handlePermanent）、永久恢复绿卡
		// 走不同路径，恒发不受影响。设 feishu.account_incident_digest_seconds 为正数即恢复。
		return
	}
	n.trackActive(account, cls, until)
	d := ""
	if len(detail) > 0 {
		d = strings.TrimSpace(detail[0])
	}
	if cls.kind == IncidentKindPermanentDisable {
		// 永久失效卡片把 detail 渲染为「详情」行（携带真实上游 status + message,
		// 如 "Payment required (402): Insufficient Balance"）,运营无需再查 DB
		// error_message 才能定位是余额耗尽还是凭证吊销。
		n.handlePermanent(account, reason, cls, d)
		return
	}
	n.recordTemporary(account, cls, d)
}

// trackActive 登记/刷新账号的活跃事件台账(按 reasonClass 去重)。无论永久卡是否被去重抑制,
// 账号确实处于该事件态,故台账无条件登记——恢复绿卡据此判断"是否值得发"。
func (n *TKAccountIncidentNotifier) trackActive(account *Account, cls incidentClass, until time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	byClass := n.active[account.ID]
	if byClass == nil {
		byClass = map[string]*activeIncident{}
		n.active[account.ID] = byClass
	}
	byClass[cls.reasonClass] = &activeIncident{
		label:       accountIncidentLabel(account),
		reasonClass: cls.reasonClass,
		kindZh:      cls.kindZh,
		kind:        cls.kind,
		until:       until,
	}
}

// NotifyAccountRecovered 对此前告警过的账号发一条即时恢复绿卡(事件驱动)。
// 仅当该账号在 active 台账里有条目才发(未告警账号 no-op);发后清掉该账号的 active 与
// permSentAt(让未来 re-disable 能立即重新告警,不被 1h 永久去重窗压住)。短窗内重复 clear
// 不重发绿卡但仍清理台账。
func (n *TKAccountIncidentNotifier) NotifyAccountRecovered(accountID int64) {
	if n == nil || accountID <= 0 {
		return
	}
	now := n.currentTime()
	n.mu.Lock()
	byClass := n.active[accountID]
	if len(byClass) == 0 {
		n.mu.Unlock()
		return
	}
	recovered := make([]*activeIncident, 0, len(byClass))
	for _, inc := range byClass {
		recovered = append(recovered, inc)
	}
	// 清台账 + 重置永久去重(无论是否重发卡)。
	delete(n.active, accountID)
	for _, inc := range recovered {
		delete(n.permSentAt, accountIncidentDedupeKey(n.siteID, accountID, inc.reasonClass))
	}
	if last, ok := n.recoverySentAt[accountID]; ok && now.Sub(last) < accountIncidentRecoveryDedupeWindow {
		n.mu.Unlock()
		return
	}
	n.recoverySentAt[accountID] = now
	n.mu.Unlock()

	sort.Slice(recovered, func(i, j int) bool { return recovered[i].reasonClass < recovered[j].reasonClass })
	label := recovered[0].label
	title := fmt.Sprintf("TokenKey 账号恢复 [%s]", n.siteID)
	body := buildAccountIncidentRecoveryText(n.siteID, label, recovered, now)
	n.send(title, "green", body, fmt.Sprintf("account_id=%d recovered", accountID))
}

// handlePermanent: 同步只做内存去重判定,命中后异步发即时单条 P0 卡片。
// detail（可为空）是上游真实错误摘要,渲染为卡片「详情」行。
func (n *TKAccountIncidentNotifier) handlePermanent(account *Account, reason string, cls incidentClass, detail string) {
	now := n.currentTime()
	key := accountIncidentDedupeKey(n.siteID, account.ID, cls.reasonClass)
	n.mu.Lock()
	if last, seen := n.permSentAt[key]; seen && now.Sub(last) < accountIncidentPermanentDedupeWindow {
		n.mu.Unlock()
		return
	}
	if n.permLimiter != nil && !n.permLimiter.Allow(now) {
		n.mu.Unlock()
		return
	}
	n.permSentAt[key] = now
	n.mu.Unlock()

	title := fmt.Sprintf("TokenKey 账号永久失效 [%s]", n.siteID)
	body := buildAccountIncidentPermanentText(n.siteID, account, reason, cls, now, detail)
	n.send(title, "red", body, fmt.Sprintf("account_id=%d reason=%s", account.ID, reason))
}

// recordTemporary: 仅写入聚合 buffer,不立即发。detail 把同一 reasonClass 下不同上游
// 维度（如 opus·5h、sonnet·7d）拆成独立条目。
func (n *TKAccountIncidentNotifier) recordTemporary(account *Account, cls incidentClass, detail string) {
	now := n.currentTime()
	key := accountIncidentDigestKey(cls.reasonClass, detail)
	n.mu.Lock()
	defer n.mu.Unlock()
	entry := n.digest[key]
	if entry == nil {
		entry = &accountIncidentDigestEntry{
			reasonClass: cls.reasonClass,
			detail:      detail,
			kindZh:      cls.kindZh,
			advice:      cls.advice,
			accountIDs:  map[int64]struct{}{},
			firstAt:     now,
		}
		n.digest[key] = entry
	}
	entry.count++
	entry.lastAt = now
	if _, ok := entry.accountIDs[account.ID]; !ok {
		entry.accountIDs[account.ID] = struct{}{}
		if len(entry.accountSamples) < accountIncidentDigestMaxSamples {
			entry.accountSamples = append(entry.accountSamples, accountIncidentLabel(account))
		}
	}
}

func (n *TKAccountIncidentNotifier) digestLoop() {
	defer func() {
		if r := recover(); r != nil {
			logger.LegacyPrintf("service.account_incident", "[AccountIncident] digest loop panic recovered: %v", r)
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
			n.pruneStaleActive()
		}
	}
}

// pruneStaleActive 静默修剪活跃台账:临时冷却条目 until 过期超过 accountIncidentActiveStaleGrace
// 仍无 clear 事件(纯定时器自愈)→ 删除,且**不发恢复卡**(按设计决策,定时器到期不播报)。
// 永久条目(until 零值)不在此修剪,只能由 NotifyAccountRecovered 清。顺带修剪恢复去重台账。
func (n *TKAccountIncidentNotifier) pruneStaleActive() {
	now := n.currentTime()
	staleBefore := now.Add(-accountIncidentActiveStaleGrace)
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, byClass := range n.active {
		for rc, inc := range byClass {
			if !inc.until.IsZero() && inc.until.Before(staleBefore) {
				delete(byClass, rc)
			}
		}
		if len(byClass) == 0 {
			delete(n.active, id)
		}
	}
	for id, t := range n.recoverySentAt {
		if t.Before(now.Add(-accountIncidentRecoveryDedupeWindow)) {
			delete(n.recoverySentAt, id)
		}
	}
}

// temporaryDigestEnabled 报告自愈类临时冷却摘要是否开启。opt-in 语义：仅当
// feishu.account_incident_digest_seconds 被显式设为 > 0 才开启；默认（未配 / <=0）
// 关闭——运营判定 529/429/temp 这类自愈橙头摘要在 provider 抖动时是噪音。池级 P0
// 与永久失效 P0/恢复绿卡在另一条路径，恒发。设正数间隔即重新开启。
func (n *TKAccountIncidentNotifier) temporaryDigestEnabled() bool {
	if n == nil || n.cfgProvider == nil {
		return false
	}
	cfg, err := n.cfgProvider.GetEmailNotificationConfig(context.Background())
	if err != nil || cfg == nil {
		return false
	}
	return cfg.Feishu.AccountIncidentDigestSeconds > 0
}

// digestInterval 从配置读 flush 间隔（秒）,下限 30s,缺失则兜底 600s。
func (n *TKAccountIncidentNotifier) digestInterval() time.Duration {
	secs := accountIncidentDigestSecondsFallback
	if n.cfgProvider != nil {
		if cfg, err := n.cfgProvider.GetEmailNotificationConfig(context.Background()); err == nil && cfg != nil && cfg.Feishu.AccountIncidentDigestSeconds > 0 {
			secs = cfg.Feishu.AccountIncidentDigestSeconds
		}
	}
	if secs < 30 {
		secs = 30
	}
	return time.Duration(secs) * time.Second
}

// flushDigest 取出并清空 buffer,有内容则异步发一条摘要;空则跳过。
func (n *TKAccountIncidentNotifier) flushDigest() {
	// 单次 flush 的 panic 必须就地兜住,不能传播到 digestLoop——否则其函数级 recover
	// 会让整个聚合 goroutine 退出,临时冷却摘要永久静默。持锁区只做 map 取数+清空、
	// 不会 panic,故 recover 兜的是 unlock 之后的排序/构造/发送,无持锁 panic 死锁路径。
	defer func() {
		if r := recover(); r != nil {
			logger.LegacyPrintf("service.account_incident", "[AccountIncident] flushDigest panic recovered: %v", r)
		}
	}()
	n.mu.Lock()
	if len(n.digest) == 0 {
		n.mu.Unlock()
		return
	}
	entries := make([]*accountIncidentDigestEntry, 0, len(n.digest))
	for _, e := range n.digest {
		entries = append(entries, e)
	}
	n.digest = map[string]*accountIncidentDigestEntry{}
	n.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].reasonClass != entries[j].reasonClass {
			return entries[i].reasonClass < entries[j].reasonClass
		}
		return entries[i].detail < entries[j].detail
	})
	now := n.currentTime()
	title := fmt.Sprintf("TokenKey 账号临时冷却摘要 [%s]", n.siteID)
	body := buildAccountIncidentDigestText(n.siteID, entries, now)
	n.send(title, "orange", body, "digest")
}

// send 异步发送（绝不阻塞挂钩点 / flush goroutine）。
func (n *TKAccountIncidentNotifier) send(title, headerTemplate, body, logCtx string) {
	go n.sendNow(title, headerTemplate, body, logCtx)
}

// sendNow 同步发送一条飞书卡片。独立 5s ctx,不继承请求 ctx;panic recover。
func (n *TKAccountIncidentNotifier) sendNow(title, headerTemplate, body, logCtx string) {
	defer func() {
		if r := recover(); r != nil {
			logger.LegacyPrintf("service.account_incident", "[AccountIncident] send panic recovered (%s): %v", logCtx, r)
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
		logger.LegacyPrintf("service.account_incident", "[AccountIncident] feishu send failed (%s): %s", logCtx, err.Error())
	}
}

func (n *TKAccountIncidentNotifier) currentTime() time.Time {
	if n != nil && n.now != nil {
		return n.now()
	}
	return time.Now()
}

func buildAccountIncidentPermanentText(site string, account *Account, reason string, cls incidentClass, now time.Time, detail string) string {
	detailLine := ""
	if d := strings.TrimSpace(detail); d != "" {
		// 上游真实错误摘要（含 upstream status,如 "Payment required (402): …"）。
		detailLine = "\n**详情**：" + escapeFeishuText(d)
	}
	return fmt.Sprintf("**节点**：%s\n**账号**：%s\n**平台**：%s\n**组**：%s\n**事件**：%s\n**reason**：%s%s\n**时间**：%s\n\n**建议**：%s",
		escapeFeishuText(site),
		escapeFeishuText(accountIncidentLabel(account)),
		escapeFeishuText(strings.TrimSpace(account.Platform)),
		escapeFeishuText(accountGroupNames(account)),
		escapeFeishuText(cls.kindZh),
		escapeFeishuText(strings.TrimSpace(reason)),
		detailLine,
		escapeFeishuText(formatAlertTime(now)),
		escapeFeishuText(cls.advice),
	)
}

func buildAccountIncidentDigestText(site string, entries []*accountIncidentDigestEntry, now time.Time) string {
	lines := make([]string, 0, len(entries)+2)
	lines = append(lines, fmt.Sprintf("**节点**：%s\n**时间**：%s\n\n临时冷却（自愈类）聚合摘要：",
		escapeFeishuText(site), escapeFeishuText(formatAlertTime(now))))
	for _, e := range entries {
		samples := strings.Join(e.accountSamples, ", ")
		if len(e.accountIDs) > len(e.accountSamples) {
			samples += fmt.Sprintf(" 等共%d个", len(e.accountIDs))
		}
		// detail（如 "opus·5h 窗口"）以 ｜ 分隔追加到类型后,把同一类型不同上游维度
		// 拆成可区分的行;无 detail 时退化为历史形态。
		label := e.kindZh
		if e.detail != "" {
			label = e.kindZh + "｜" + e.detail
		}
		lines = append(lines, fmt.Sprintf("- **%s**：%d 次 / %d 账号（%s）",
			escapeFeishuText(label), e.count, len(e.accountIDs), escapeFeishuText(samples)))
	}
	// 认知纠正脚注:本摘要里的冷却全部是「上游对账号的限流/冷却」(账号级用量窗口 5h/7d
	// 或上游错误策略),不是 TK 内部 rpm/并发/会话 配额。内部配额超限不会冷却账号,而是在
	// 请求侧快速失败(HTTP 429 no available accounts),根本不进本摘要。详见 account 冷却汇聚点。
	lines = append(lines, "\n说明：以上均为「上游对账号的限流/冷却」（账号级用量窗口 5h/7d 或上游错误策略），非 TK 内部 rpm/并发/会话 配额——内部配额超限只会让请求侧快速失败（HTTP 429 no available accounts），不计入本摘要。")
	return strings.Join(lines, "\n")
}

func buildAccountIncidentRecoveryText(site, label string, recovered []*activeIncident, now time.Time) string {
	kinds := make([]string, 0, len(recovered))
	for _, inc := range recovered {
		kinds = append(kinds, inc.kindZh)
	}
	return fmt.Sprintf("**节点**：%s\n**账号**：%s\n**时间**：%s\n\n账号已恢复调度。此前事件：%s",
		escapeFeishuText(site),
		escapeFeishuText(label),
		escapeFeishuText(formatAlertTime(now)),
		escapeFeishuText(strings.Join(kinds, "、")),
	)
}

func accountIncidentLabel(account *Account) string {
	if account == nil {
		return "-"
	}
	name := strings.TrimSpace(account.Name)
	if name == "" {
		return fmt.Sprintf("#%d", account.ID)
	}
	return fmt.Sprintf("%s(#%d)", name, account.ID)
}

func accountGroupNames(account *Account) string {
	if account == nil || len(account.Groups) == 0 {
		return "-"
	}
	names := make([]string, 0, len(account.Groups))
	for _, g := range account.Groups {
		if g == nil {
			continue
		}
		gn := strings.TrimSpace(g.Name)
		if gn == "" {
			gn = fmt.Sprintf("#%d", g.ID)
		}
		names = append(names, gn)
	}
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ",")
}

var bjLoc = time.FixedZone("Asia/Shanghai", 8*60*60)

func formatAlertTime(t time.Time) string {
	bj := t.In(bjLoc)
	return fmt.Sprintf("%s（北京时间 %s）", t.UTC().Format(time.RFC3339), bj.Format("15:04:05"))
}

func accountIncidentDedupeKey(site string, accountID int64, reasonClass string) string {
	return fmt.Sprintf("%s|%d|%s", site, accountID, reasonClass)
}

// siteFromFrontendURL 从 server.frontend_url 域名提取节点名:
//
//	api.tokenkey.dev      -> prod
//	api-us1.tokenkey.dev  -> edge-us1
//
// 无法解析时返回 "unknown"。
func siteFromFrontendURL(frontendURL string) string {
	raw := strings.TrimSpace(frontendURL)
	if raw == "" {
		return "unknown"
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "unknown"
	}
	host := parsed.Hostname()
	first := host
	if idx := strings.IndexByte(host, '.'); idx > 0 {
		first = host[:idx]
	}
	switch {
	case first == "":
		return "unknown"
	case first == "api":
		return "prod"
	case strings.HasPrefix(first, "api-"):
		return "edge-" + strings.TrimPrefix(first, "api-")
	default:
		return first
	}
}
