package service

// TK: 上游 400 连续失败兜底哨兵（只告警，不改账号状态）。
//
// 背景：prod 2026-11-02 火山 Agent Plan 订阅过期返回 HTTP 400 invalid_request_error。
// 欠费 SSOT（account_standing_billing_tk.go）靠短语白名单识别，这句文案不在名单里，于是
// 落回「调用方 400」默认策略：不惩罚账号、不 failover、不告警。4 个账号首次报错后 0 成功，
// 累计约 8.5k 次相同 400，飞书无任何信号。
//
// 400 不惩罚账号是 #617 的刻意设计（坏请求不能打掉共享账号），本哨兵不改变它：命中只发一张
// 橙头飞书卡提醒人工确认，绝不 SetError / 停调度。
//
// 判定（以 7 天线上回放定阈值）：同一 active 账号，自最近一次成功请求之后，最新一段连续的
// 「同一种」上游 400（消息归一化后相同）达到 N 次，且持续时长 >= minSpan。持续时长门槛滤掉
// 单用户坏请求的秒级重试风暴（回放中最长 51 分钟、20+ 次的同签名参数 400 都伴随成功请求
// 打断，或为单用户短促重放）。
//
// 设计对齐 upstream_balance_sentinel_tk.go：单 ticker + Redis leader-lock（无 redis 降级
// lockless）+ ops_job_heartbeats 心跳；告警去重按 (账号, 签名) 内存 armed，连续失败段结束
// （账号恢复成功或换了签名）后重新上膛。

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	upstream400StreakJobName       = "upstream_400_streak_sentinel"
	upstream400StreakLockKey       = "upstream:400streak:sentinel:leader"
	upstream400StreakLockTTL       = 4 * time.Minute
	upstream400StreakRunTO         = 60 * time.Second
	upstream400StreakCheckInterval = 5 * time.Minute
	upstream400StreakHeartbeatTO   = 5 * time.Second
	// upstream400StreakLookback bounds the scan. A streak older than this that is
	// still failing keeps producing rows inside the window, so it stays visible.
	upstream400StreakLookback = 6 * time.Hour
	// upstream400StreakMinCount / MinSpan: see the replay note above.
	upstream400StreakMinCount = 20
	upstream400StreakMinSpan  = 10 * time.Minute
	// upstream400StreakSampleMax caps the upstream text quoted in the card.
	upstream400StreakSampleMax = 300
)

// Upstream400Streak is one account's currently open run of identical upstream 400s.
type Upstream400Streak struct {
	AccountID     int64
	Signature     string
	SampleMessage string
	Count         int
	FirstAt       time.Time
	LastAt        time.Time
	DistinctUsers int
}

// Upstream400StreakReader is the narrow read dependency (repository implements it).
type Upstream400StreakReader interface {
	ListOpenUpstream400Streaks(ctx context.Context, since time.Time, minCount int, minSpan time.Duration) ([]Upstream400Streak, error)
}

// upstream400StreakAccountLookup resolves account labels/groups for the card.
type upstream400StreakAccountLookup interface {
	GetByID(ctx context.Context, id int64) (*Account, error)
}

// upstream400StreakAlerter is the narrow notifier surface (*TKAccountIncidentNotifier).
type upstream400StreakAlerter interface {
	NotifyUpstream400Streak(account *Account, streak Upstream400Streak)
}

// Upstream400StreakSentinel polls for open upstream-400 streaks and raises an
// advisory Feishu card. It never writes account state.
type Upstream400StreakSentinel struct {
	reader     Upstream400StreakReader
	accounts   upstream400StreakAccountLookup
	alerter    upstream400StreakAlerter
	cfg        opsFeishuConfigProvider
	heartbeat  balanceSentinelHeartbeat
	redis      *redis.Client
	instanceID string
	now        func() time.Time

	mu    sync.Mutex
	armed map[int64]string // accountID -> signature already alerted

	stopCh   chan struct{}
	stopOnce sync.Once
	startOne sync.Once
	wg       sync.WaitGroup

	warnNoRedisOnce sync.Once
}

// NewUpstream400StreakSentinel constructs the sentinel. Nil reader/alerter makes Start a no-op.
func NewUpstream400StreakSentinel(
	reader Upstream400StreakReader,
	accounts upstream400StreakAccountLookup,
	alerter upstream400StreakAlerter,
	cfg opsFeishuConfigProvider,
	heartbeat balanceSentinelHeartbeat,
	redisClient *redis.Client,
) *Upstream400StreakSentinel {
	return &Upstream400StreakSentinel{
		reader:     reader,
		accounts:   accounts,
		alerter:    alerter,
		cfg:        cfg,
		heartbeat:  heartbeat,
		redis:      redisClient,
		instanceID: uuid.NewString(),
		now:        time.Now,
		armed:      map[int64]string{},
		stopCh:     make(chan struct{}),
	}
}

// Start launches the ticker goroutine. Safe to call once.
func (s *Upstream400StreakSentinel) Start() {
	if s == nil || s.reader == nil || s.alerter == nil {
		return
	}
	s.startOne.Do(func() {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(upstream400StreakCheckInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					s.runOnceLocked()
				case <-s.stopCh:
					return
				}
			}
		}()
	})
}

// Stop signals the goroutine to exit and waits.
func (s *Upstream400StreakSentinel) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
}

func (s *Upstream400StreakSentinel) runOnceLocked() {
	release, ok := s.tryAcquireLock()
	if !ok {
		return
	}
	if release != nil {
		defer release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), upstream400StreakRunTO)
	defer cancel()
	s.runOnce(ctx)
}

func (s *Upstream400StreakSentinel) tryAcquireLock() (func(), bool) {
	if s.redis == nil {
		s.warnNoRedisOnce.Do(func() {
			slog.Warn("upstream 400 streak sentinel running without distributed lock (no redis)")
		})
		return nil, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	acquired, err := s.redis.SetNX(ctx, upstream400StreakLockKey, s.instanceID, upstream400StreakLockTTL).Result()
	if err != nil {
		slog.Warn("upstream 400 streak sentinel leader lock SetNX failed; skipping cycle", "err", err)
		return nil, false
	}
	if !acquired {
		return nil, false
	}
	return func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		_, _ = s.redis.Del(releaseCtx, upstream400StreakLockKey).Result()
	}, true
}

func (s *Upstream400StreakSentinel) feishuEnabled(ctx context.Context) bool {
	if s.cfg == nil {
		return false
	}
	cfg, err := s.cfg.GetEmailNotificationConfig(ctx)
	return err == nil && cfg != nil && cfg.Feishu.Enabled
}

// runOnce performs one scan. Tests drive it directly.
func (s *Upstream400StreakSentinel) runOnce(ctx context.Context) {
	startedAt := s.now()
	if !s.feishuEnabled(ctx) {
		return
	}
	streaks, err := s.reader.ListOpenUpstream400Streaks(ctx, startedAt.Add(-upstream400StreakLookback), upstream400StreakMinCount, upstream400StreakMinSpan)
	if err != nil {
		s.recordHeartbeat(startedAt, "", err)
		return
	}

	open := make(map[int64]string, len(streaks))
	alerted := 0
	for _, st := range streaks {
		open[st.AccountID] = st.Signature
		s.mu.Lock()
		already := s.armed[st.AccountID] == st.Signature
		if !already {
			s.armed[st.AccountID] = st.Signature
		}
		s.mu.Unlock()
		if already {
			continue
		}
		account := s.lookupAccount(ctx, st.AccountID)
		s.alerter.NotifyUpstream400Streak(account, st)
		alerted++
		slog.Warn("upstream_400_streak_detected",
			"account_id", st.AccountID, "count", st.Count, "users", st.DistinctUsers,
			"first_at", st.FirstAt, "last_at", st.LastAt)
	}
	// Re-arm accounts whose streak ended (success, or a different failure shape).
	s.mu.Lock()
	for id, sig := range s.armed {
		if open[id] != sig {
			delete(s.armed, id)
		}
	}
	s.mu.Unlock()

	s.recordHeartbeat(startedAt, fmt.Sprintf("open_streaks=%d alerted=%d", len(streaks), alerted), nil)
}

func (s *Upstream400StreakSentinel) lookupAccount(ctx context.Context, id int64) *Account {
	if s.accounts != nil {
		if acc, err := s.accounts.GetByID(ctx, id); err == nil && acc != nil {
			return acc
		}
	}
	return &Account{ID: id}
}

func (s *Upstream400StreakSentinel) recordHeartbeat(runAt time.Time, result string, runErr error) {
	if s == nil || s.heartbeat == nil {
		return
	}
	now := s.now().UTC()
	durMs := now.Sub(runAt).Milliseconds()
	in := &OpsUpsertJobHeartbeatInput{JobName: upstream400StreakJobName, LastRunAt: &runAt, LastDurationMs: &durMs}
	if runErr != nil {
		msg := runErr.Error()
		if len(msg) > 2048 {
			msg = msg[:2048]
		}
		in.LastErrorAt, in.LastError = &now, &msg
	} else {
		in.LastSuccessAt, in.LastResult = &now, &result
	}
	ctx, cancel := context.WithTimeout(context.Background(), upstream400StreakHeartbeatTO)
	defer cancel()
	_ = s.heartbeat.UpsertJobHeartbeat(ctx, in)
}

// NotifyUpstream400Streak sends the advisory orange card. The sentinel already
// deduped per (account, signature); every call sends one card.
func (n *TKAccountIncidentNotifier) NotifyUpstream400Streak(account *Account, streak Upstream400Streak) {
	if n == nil || account == nil {
		return
	}
	title := fmt.Sprintf("TokenKey 上游 400 连续失败待确认 [%s]", n.siteID)
	body := buildUpstream400StreakText(n.siteID, account, streak, n.currentTime())
	n.send(title, "orange", body, fmt.Sprintf("account_id=%d upstream_400_streak", account.ID))
}

func buildUpstream400StreakText(site string, account *Account, st Upstream400Streak, now time.Time) string {
	sample := strings.TrimSpace(sanitizeUpstreamErrorMessage(st.SampleMessage))
	if len(sample) > upstream400StreakSampleMax {
		sample = sample[:upstream400StreakSampleMax] + "…"
	}
	span := st.LastAt.Sub(st.FirstAt).Round(time.Minute)
	return fmt.Sprintf("**节点**：%s\n**账号**：%s\n**平台**：%s\n**组**：%s\n**连续次数**：%d（涉及用户 %d）\n**持续**：%s（%s → %s）\n**期间成功请求**：0\n**上游原文**：%s\n**时间**：%s\n\n**建议**：同一账号持续返回同一种上游 400 且无成功请求，疑似账号侧失效（订阅过期/欠费/权限变更）而非用户请求错误。请人工确认：若属账号问题，在 Admin 停调度并处理上游，同时把该文案补进欠费 SSOT；若属用户坏请求，可忽略。本卡不改变账号状态。",
		escapeFeishuText(site),
		escapeFeishuText(accountIncidentLabel(account)),
		escapeFeishuText(strings.TrimSpace(account.Platform)),
		escapeFeishuText(accountGroupNames(account)),
		st.Count, st.DistinctUsers,
		span, escapeFeishuText(formatAlertTime(st.FirstAt)), escapeFeishuText(formatAlertTime(st.LastAt)),
		escapeFeishuText(sample),
		escapeFeishuText(formatAlertTime(now)),
	)
}

// ProvideUpstream400StreakSentinel creates and starts the advisory upstream-400
// streak sentinel. Read-only against the DB; never writes accounts.
func ProvideUpstream400StreakSentinel(
	reader Upstream400StreakReader,
	accountRepo AccountRepository,
	notifier *TKAccountIncidentNotifier,
	ops *OpsService,
	opsRepo OpsRepository,
	redisClient *redis.Client,
) *Upstream400StreakSentinel {
	var cfgProvider opsFeishuConfigProvider
	if ops != nil {
		cfgProvider = ops
	}
	var alerter upstream400StreakAlerter
	if notifier != nil {
		alerter = notifier
	}
	s := NewUpstream400StreakSentinel(reader, accountRepo, alerter, cfgProvider, opsRepo, redisClient)
	s.Start()
	return s
}
