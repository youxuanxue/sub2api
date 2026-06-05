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
	NotifyAccountIncident(account *Account, until time.Time, reason string, kind AccountIncidentKind)
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
)

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

// accountIncidentDigestEntry 是临时冷却聚合 buffer 的单个 reasonClass 条目。
type accountIncidentDigestEntry struct {
	reasonClass    string
	kindZh         string
	advice         string
	accountIDs     map[int64]struct{}
	accountSamples []string
	count          int
	firstAt        time.Time
	lastAt         time.Time
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

	mu          sync.Mutex
	permSentAt  map[string]time.Time                   // 永久失效去重: key -> 上次发送
	permLimiter *slidingWindowLimiter                  // 永久失效防爆量
	digest      map[string]*accountIncidentDigestEntry // reasonClass -> 聚合条目

	stopCh   chan struct{}
	stopOnce sync.Once
}

func newTKAccountIncidentNotifier(cfgProvider opsFeishuConfigProvider, siteID string) *TKAccountIncidentNotifier {
	n := &TKAccountIncidentNotifier{
		cfgProvider: cfgProvider,
		httpClient:  &http.Client{Timeout: opsFeishuWebhookTimeout},
		siteID:      strings.TrimSpace(siteID),
		now:         time.Now,
		permSentAt:  map[string]time.Time{},
		permLimiter: newSlidingWindowLimiter(accountIncidentPermanentRatePerHour, time.Hour),
		digest:      map[string]*accountIncidentDigestEntry{},
		stopCh:      make(chan struct{}),
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

func (n *TKAccountIncidentNotifier) NotifyAccountIncident(account *Account, until time.Time, reason string, kind AccountIncidentKind) {
	if n == nil || account == nil {
		return
	}
	cls := classifyIncident(reason, until, kind)
	if !cls.alert {
		return
	}
	if cls.kind == IncidentKindPermanentDisable {
		n.handlePermanent(account, reason, cls)
		return
	}
	n.recordTemporary(account, cls)
}

// handlePermanent: 同步只做内存去重判定,命中后异步发即时单条 P0 卡片。
func (n *TKAccountIncidentNotifier) handlePermanent(account *Account, reason string, cls incidentClass) {
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
	body := buildAccountIncidentPermanentText(n.siteID, account, reason, cls, now)
	n.send(title, "red", body, fmt.Sprintf("account_id=%d reason=%s", account.ID, reason))
}

// recordTemporary: 仅写入聚合 buffer,不立即发。
func (n *TKAccountIncidentNotifier) recordTemporary(account *Account, cls incidentClass) {
	now := n.currentTime()
	n.mu.Lock()
	defer n.mu.Unlock()
	entry := n.digest[cls.reasonClass]
	if entry == nil {
		entry = &accountIncidentDigestEntry{
			reasonClass: cls.reasonClass,
			kindZh:      cls.kindZh,
			advice:      cls.advice,
			accountIDs:  map[int64]struct{}{},
			firstAt:     now,
		}
		n.digest[cls.reasonClass] = entry
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
		}
	}
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

	sort.Slice(entries, func(i, j int) bool { return entries[i].reasonClass < entries[j].reasonClass })
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

func buildAccountIncidentPermanentText(site string, account *Account, reason string, cls incidentClass, now time.Time) string {
	return fmt.Sprintf("**节点**：%s\n**账号**：%s\n**平台**：%s\n**组**：%s\n**事件**：%s\n**reason**：%s\n**时间**：%s\n\n**建议**：%s",
		escapeFeishuText(site),
		escapeFeishuText(accountIncidentLabel(account)),
		escapeFeishuText(strings.TrimSpace(account.Platform)),
		escapeFeishuText(accountGroupNames(account)),
		escapeFeishuText(cls.kindZh),
		escapeFeishuText(strings.TrimSpace(reason)),
		escapeFeishuText(formatAlertTime(now)),
		escapeFeishuText(cls.advice),
	)
}

func buildAccountIncidentDigestText(site string, entries []*accountIncidentDigestEntry, now time.Time) string {
	lines := make([]string, 0, len(entries)+1)
	lines = append(lines, fmt.Sprintf("**节点**：%s\n**时间**：%s\n\n临时冷却（自愈类）聚合摘要：",
		escapeFeishuText(site), escapeFeishuText(formatAlertTime(now))))
	for _, e := range entries {
		samples := strings.Join(e.accountSamples, ", ")
		if len(e.accountIDs) > len(e.accountSamples) {
			samples += fmt.Sprintf(" 等共%d个", len(e.accountIDs))
		}
		lines = append(lines, fmt.Sprintf("- **%s**：%d 次 / %d 账号（%s）",
			escapeFeishuText(e.kindZh), e.count, len(e.accountIDs), escapeFeishuText(samples)))
	}
	return strings.Join(lines, "\n")
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
