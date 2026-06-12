package service

// TK: 上游账号低余额主动哨兵。
//
// 背景 / 动机见 docs / plan「上游账号低余额主动告警」。一句话：newapi 第五平台上游渠道
// 账号余额耗尽时，bridge 把 402/429 吞码掩成 502 无限锤上游（2026-06-11 DeepSeek 余额归零
// 事故）。被动 402 告警只能事后报；本哨兵定时**主动**拉有公开余额 API 的渠道账号余额，低于
// 阈值就提前发一条橙头飞书预警，让运营在归零前充值。
//
// 设计对齐 anthropic_config_reconciler：单 ticker + Redis leader-lock（无 redis 降级 lockless）+
// 心跳写 ops_job_heartbeats。只读上游 + 只发告警，不写账号状态、不改任何配置。
// 当前只接 DeepSeek（唯一有公开 /user/balance 的渠道，见 newapi.BalanceProbeFor）。

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/integration/newapi"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	upstreamBalanceSentinelJobName = "upstream_balance_sentinel"
	upstreamBalanceSentinelLockKey = "upstream:balance:sentinel:leader"
	upstreamBalanceSentinelLockTTL = 4 * time.Minute
	upstreamBalanceSentinelRunTO   = 60 * time.Second
	upstreamBalanceCheckInterval   = 5 * time.Minute
	upstreamBalanceHeartbeatTO     = 5 * time.Second
)

// balanceSentinelAccountStore is the narrow account dependency (the live
// *accountRepository satisfies it). A small interface keeps runOnce unit-testable
// without a full repository stub.
type balanceSentinelAccountStore interface {
	ListByPlatform(ctx context.Context, platform string) ([]Account, error)
}

// balanceSentinelHeartbeat is the narrow heartbeat dependency (*OpsService's repo
// path satisfies it via OpsService). nil → heartbeat is skipped.
type balanceSentinelHeartbeat interface {
	UpsertJobHeartbeat(ctx context.Context, in *OpsUpsertJobHeartbeatInput) error
}

// UpstreamBalanceSentinel polls upstream channel-account balances and fires a
// pre-emptive low-balance Feishu warning before the account hits zero.
type UpstreamBalanceSentinel struct {
	accounts   balanceSentinelAccountStore
	http       newapi.HTTPDoer
	notifier   *TKAccountIncidentNotifier
	cfg        opsFeishuConfigProvider // reads Feishu.Enabled + threshold
	recharge   rechargeURLResolver     // reuses the existing balance_low_notify_recharge_url setting
	heartbeat  balanceSentinelHeartbeat
	redis      *redis.Client
	instanceID string

	// armed tracks the per-account re-arm dedup: true once we've alerted on a
	// crossing into low, cleared when the account recovers to >= threshold. Avoids
	// re-alerting every tick while still low, and re-arms after a top-up.
	armed map[int64]bool

	stopCh   chan struct{}
	stopOnce sync.Once
	startOne sync.Once
	wg       sync.WaitGroup

	warnNoRedisOnce sync.Once
}

// rechargeURLResolver reads the operator-facing recharge link surfaced in the
// alert card. *SettingService satisfies it; nil → no link in the card.
type rechargeURLResolver interface {
	GetValue(ctx context.Context, key string) (string, error)
}

// NewUpstreamBalanceSentinel constructs the sentinel. A nil accounts store or nil
// notifier makes Start a no-op (keeps wire wiring safe for minimal deps).
func NewUpstreamBalanceSentinel(
	accounts balanceSentinelAccountStore,
	httpUpstream newapi.HTTPDoer,
	notifier *TKAccountIncidentNotifier,
	cfg opsFeishuConfigProvider,
	recharge rechargeURLResolver,
	heartbeat balanceSentinelHeartbeat,
	redisClient *redis.Client,
) *UpstreamBalanceSentinel {
	return &UpstreamBalanceSentinel{
		accounts:   accounts,
		http:       httpUpstream,
		notifier:   notifier,
		cfg:        cfg,
		recharge:   recharge,
		heartbeat:  heartbeat,
		redis:      redisClient,
		instanceID: uuid.NewString(),
		armed:      map[int64]bool{},
		stopCh:     make(chan struct{}),
	}
}

// Start launches the sentinel goroutine. Safe to call once; nil deps no-op.
func (s *UpstreamBalanceSentinel) Start() {
	if s == nil || s.accounts == nil || s.notifier == nil || s.http == nil {
		return
	}
	s.startOne.Do(func() {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(upstreamBalanceCheckInterval)
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

// Stop signals the goroutine to exit and waits. Safe to call once.
func (s *UpstreamBalanceSentinel) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()
}

// runOnceLocked acquires the redis leader lock (best-effort; lockless on no redis)
// and runs one pass. Multiple replicas thus alert at most once per tick fleet-wide.
func (s *UpstreamBalanceSentinel) runOnceLocked() {
	release, ok := s.tryAcquireLock()
	if !ok {
		return
	}
	if release != nil {
		defer release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), upstreamBalanceSentinelRunTO)
	defer cancel()
	s.runOnce(ctx)
}

func (s *UpstreamBalanceSentinel) tryAcquireLock() (func(), bool) {
	if s.redis == nil {
		s.warnNoRedisOnce.Do(func() {
			slog.Warn("upstream balance sentinel running without distributed lock (no redis)")
		})
		return nil, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	acquired, err := s.redis.SetNX(ctx, upstreamBalanceSentinelLockKey, s.instanceID, upstreamBalanceSentinelLockTTL).Result()
	if err != nil {
		slog.Warn("upstream balance sentinel leader lock SetNX failed; skipping cycle", "err", err)
		return nil, false
	}
	if !acquired {
		return nil, false
	}
	return func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		// Best-effort release; TTL backstops a missed delete.
		_, _ = s.redis.Del(releaseCtx, upstreamBalanceSentinelLockKey).Result()
	}, true
}

// runOnce performs a single balance-poll pass. Tests call it directly to drive
// the threshold-crossing + re-arm invariants without the ticker / lock.
func (s *UpstreamBalanceSentinel) runOnce(ctx context.Context) {
	startedAt := time.Now()
	threshold, enabled := s.thresholdConfig(ctx)
	if !enabled || threshold <= 0 {
		// Feature off (Feishu disabled or no threshold). Skip silently — no upstream
		// HTTP, no heartbeat noise.
		return
	}

	accounts, err := s.accounts.ListByPlatform(ctx, PlatformNewAPI)
	if err != nil {
		s.recordHeartbeatError(startedAt, fmt.Errorf("list newapi accounts: %w", err))
		return
	}

	rechargeURL := s.rechargeURL(ctx)

	checked, low, probeErrs := 0, 0, 0
	for i := range accounts {
		acc := &accounts[i]
		probe, ok := newapi.BalanceProbeFor(acc.ChannelType)
		if !ok || !acc.IsActive() {
			continue
		}
		checked++
		apiKey := strings.TrimSpace(acc.GetCredential("api_key"))
		baseURL := strings.TrimSpace(acc.GetCredential("base_url"))
		proxyURL := ""
		if acc.ProxyID != nil && acc.Proxy != nil {
			proxyURL = acc.Proxy.URL()
		}
		res, perr := probe(ctx, s.http, baseURL, apiKey, proxyURL, acc.ID, acc.Concurrency)
		if perr != nil {
			// Upstream blip / bad credential — NOT a low-balance signal. Count it for
			// the heartbeat but never alert (avoid false positives on transient errors).
			probeErrs++
			slog.Warn("upstream balance probe failed", "account_id", acc.ID, "name", acc.Name, "err", perr)
			continue
		}
		isLow := !res.IsAvailable || res.AvailableCNY < threshold
		if isLow {
			low++
			if !s.armed[acc.ID] {
				s.armed[acc.ID] = true
				s.notifier.NotifyUpstreamBalanceLow(acc, res.AvailableCNY, res.IsAvailable, threshold, rechargeURL)
			}
		} else {
			// Recovered above threshold → re-arm so a future dip alerts again.
			delete(s.armed, acc.ID)
		}
	}

	s.recordHeartbeatSuccess(startedAt, checked, low, probeErrs)
}

// thresholdConfig reads the low-balance threshold and whether Feishu alerting is
// enabled. Returns (threshold, enabled).
func (s *UpstreamBalanceSentinel) thresholdConfig(ctx context.Context) (float64, bool) {
	if s.cfg == nil {
		return 0, false
	}
	cfg, err := s.cfg.GetEmailNotificationConfig(ctx)
	if err != nil || cfg == nil {
		return 0, false
	}
	return cfg.Feishu.UpstreamBalanceLowThresholdCNY, cfg.Feishu.Enabled
}

func (s *UpstreamBalanceSentinel) rechargeURL(ctx context.Context) string {
	if s.recharge == nil {
		return ""
	}
	v, err := s.recharge.GetValue(ctx, SettingKeyBalanceLowNotifyRechargeURL)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

func (s *UpstreamBalanceSentinel) recordHeartbeatSuccess(runAt time.Time, checked, low, probeErrs int) {
	if s == nil || s.heartbeat == nil {
		return
	}
	now := time.Now().UTC()
	durMs := time.Since(runAt).Milliseconds()
	result := fmt.Sprintf("checked=%d low=%d probe_errors=%d", checked, low, probeErrs)
	ctx, cancel := context.WithTimeout(context.Background(), upstreamBalanceHeartbeatTO)
	defer cancel()
	_ = s.heartbeat.UpsertJobHeartbeat(ctx, &OpsUpsertJobHeartbeatInput{
		JobName:        upstreamBalanceSentinelJobName,
		LastRunAt:      &runAt,
		LastSuccessAt:  &now,
		LastDurationMs: &durMs,
		LastResult:     &result,
	})
}

func (s *UpstreamBalanceSentinel) recordHeartbeatError(runAt time.Time, err error) {
	if s == nil || s.heartbeat == nil || err == nil {
		return
	}
	now := time.Now().UTC()
	durMs := time.Since(runAt).Milliseconds()
	msg := err.Error()
	if len(msg) > 2048 {
		msg = msg[:2048]
	}
	ctx, cancel := context.WithTimeout(context.Background(), upstreamBalanceHeartbeatTO)
	defer cancel()
	_ = s.heartbeat.UpsertJobHeartbeat(ctx, &OpsUpsertJobHeartbeatInput{
		JobName:        upstreamBalanceSentinelJobName,
		LastRunAt:      &runAt,
		LastErrorAt:    &now,
		LastError:      &msg,
		LastDurationMs: &durMs,
	})
}
