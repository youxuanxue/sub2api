package service

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// proxy_unreachable_count — 出口代理 IP:port 从本节点 TCP 不可达。
//
// 背景（prod 2026-10-17，jp-us5 / 52.68.182.0:1080）：SOCKS 出口宕机时，
// OpenAI OAuth 等账号仍可调度，请求在 socks connect 阶段超时/host unreachable，
// failover 耗尽后用户侧变成 502「Upstream service temporarily unavailable」。
// 既有 user_visible_failure_count / upstream_error_rate 要等流量砸上去才看见，
// 且文案被收成通用上游故障，网络/运维无法立刻对上「哪台代理 IP」。
//
// 本指标在 ops_alert_evaluator 节拍里对「status=active 且仍绑定未删除账号」的
// 代理做 TCP dial（只验端口可达，不做 SOCKS/HTTP 握手）。任一不可达 → 计数 +1；
// 飞书主因行直接打出 name host:port 与 dial 错误，方便网络工程师对号入座。
//
// 仅在 prod 评估（见 isProdOnlyAlertRule）：现网 SOCKS 出口由 prod 拨号；
// edge 退役后在 edge 上探测会得到错误网络路径的假信号。

const proxyUnreachableDialTimeout = 2 * time.Second

// proxyTCPDialFunc is the seam for unit tests. Production uses net.Dialer.
type proxyTCPDialFunc func(ctx context.Context, network, address string) error

func defaultProxyTCPDial(ctx context.Context, network, address string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, address)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

type proxyUnreachableInfo struct {
	ID      int64
	Name    string
	Host    string
	Port    int
	DialErr string
}

type proxyUnreachableSnapshot struct {
	at          time.Time
	unreachable []proxyUnreachableInfo
}

func (s *OpsAlertEvaluatorService) proxyTCPDial() proxyTCPDialFunc {
	if s != nil && s.proxyTCPDialFn != nil {
		return s.proxyTCPDialFn
	}
	return defaultProxyTCPDial
}

// SetProxyTCPDialForTest injects a dialer for unit tests. Not for production Wire.
func (s *OpsAlertEvaluatorService) SetProxyTCPDialForTest(fn proxyTCPDialFunc) {
	if s == nil {
		return
	}
	s.proxyTCPDialFn = fn
}

func (s *OpsAlertEvaluatorService) computeProxyUnreachableCount(ctx context.Context) (float64, bool) {
	if s == nil || s.proxyRepo == nil {
		return 0, false
	}
	unreachable, ok := s.probeUnreachableProxies(ctx)
	if !ok {
		return 0, false
	}
	s.storeProxyUnreachableSnapshot(unreachable)
	return float64(len(unreachable)), true
}

func (s *OpsAlertEvaluatorService) probeUnreachableProxies(ctx context.Context) ([]proxyUnreachableInfo, bool) {
	if s == nil || s.proxyRepo == nil {
		return nil, false
	}
	proxies, err := s.proxyRepo.ListActiveWithAccountCount(ctx)
	if err != nil {
		return nil, false
	}

	targets := make([]ProxyWithAccountCount, 0, len(proxies))
	for _, p := range proxies {
		if p.AccountCount <= 0 {
			continue
		}
		host := strings.TrimSpace(p.Host)
		if host == "" || p.Port <= 0 {
			continue
		}
		targets = append(targets, p)
	}
	if len(targets) == 0 {
		return nil, true
	}

	dial := s.proxyTCPDial()
	outCh := make(chan proxyUnreachableInfo, len(targets))
	var wg sync.WaitGroup
	for _, p := range targets {
		wg.Add(1)
		go func(p ProxyWithAccountCount) {
			defer wg.Done()
			addr := net.JoinHostPort(strings.TrimSpace(p.Host), fmt.Sprintf("%d", p.Port))
			dialCtx, cancel := context.WithTimeout(ctx, proxyUnreachableDialTimeout)
			defer cancel()
			if err := dial(dialCtx, "tcp", addr); err != nil {
				outCh <- proxyUnreachableInfo{
					ID:      p.ID,
					Name:    strings.TrimSpace(p.Name),
					Host:    strings.TrimSpace(p.Host),
					Port:    p.Port,
					DialErr: truncateProxyDialErr(err),
				}
			}
		}(p)
	}
	wg.Wait()
	close(outCh)

	unreachable := make([]proxyUnreachableInfo, 0, len(targets))
	for info := range outCh {
		unreachable = append(unreachable, info)
	}
	sort.Slice(unreachable, func(i, j int) bool {
		if unreachable[i].Name != unreachable[j].Name {
			return unreachable[i].Name < unreachable[j].Name
		}
		return unreachable[i].ID < unreachable[j].ID
	})
	return unreachable, true
}

func (s *OpsAlertEvaluatorService) storeProxyUnreachableSnapshot(unreachable []proxyUnreachableInfo) {
	if s == nil {
		return
	}
	s.proxyUnreachableMu.Lock()
	defer s.proxyUnreachableMu.Unlock()
	s.proxyUnreachableSnap = proxyUnreachableSnapshot{
		at:          time.Now().UTC(),
		unreachable: append([]proxyUnreachableInfo(nil), unreachable...),
	}
}

func (s *OpsAlertEvaluatorService) loadProxyUnreachableSnapshot() []proxyUnreachableInfo {
	if s == nil {
		return nil
	}
	s.proxyUnreachableMu.Lock()
	defer s.proxyUnreachableMu.Unlock()
	if len(s.proxyUnreachableSnap.unreachable) == 0 {
		return nil
	}
	// Same evaluator tick (metric → dimensions) reuses the probe; stale snaps
	// older than two evaluation intervals are ignored so a recovered probe
	// cannot keep printing a ghost cause line on an unrelated rule.
	if time.Since(s.proxyUnreachableSnap.at) > 2*time.Minute {
		return nil
	}
	return append([]proxyUnreachableInfo(nil), s.proxyUnreachableSnap.unreachable...)
}

func formatProxyUnreachableCause(unreachable []proxyUnreachableInfo) string {
	if len(unreachable) == 0 {
		return ""
	}
	parts := make([]string, 0, len(unreachable))
	for _, u := range unreachable {
		name := u.Name
		if name == "" {
			name = fmt.Sprintf("proxy#%d", u.ID)
		}
		addr := net.JoinHostPort(u.Host, fmt.Sprintf("%d", u.Port))
		if u.DialErr == "" {
			parts = append(parts, fmt.Sprintf("%s %s", name, addr))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %s (%s)", name, addr, u.DialErr))
	}
	return strings.Join(parts, "; ")
}

func truncateProxyDialErr(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	// net errors often embed the dial address already; keep the message short
	// for Feishu first-screen readability.
	if len(msg) > 120 {
		return msg[:117] + "..."
	}
	return msg
}
