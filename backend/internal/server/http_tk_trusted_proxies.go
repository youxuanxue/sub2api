package server

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
)

// http_tk_trusted_proxies.go (TokenKey-only companion)
//
// 背景：TokenKey 的所有 prod/edge 节点都是 Caddy → 容器内 app 的拓扑。Caddy 用
// `header_up X-Real-IP/X-Forwarded-For {remote_host}` 把这两个头覆写为真实 TCP 源
// （可信、不可伪造）。但出厂 `server.trusted_proxies` 为空，导致 gin 在
// SetTrustedProxies(nil) 下让 c.ClientIP() 退回到直接对端——也就是 docker 网桥 IP
// （172.x）。后果：按 c.ClientIP() 分桶的认证端点限流（rate_limiter.go）塌缩成
// 单一全局桶，支付审计 IP 失真。详见 Wei-Shaw/sub2api#1326 / #2410。
//
// 这里把“运维未配置”的语义从“信任链关闭”改为“信任 loopback + 私网网段”，使 gin 的
// c.ClientIP() 在两种拓扑下都正确且防伪造：
//   - 反代后：直接对端是私网跳点（受信）→ gin 沿 X-Forwarded-For 回溯到第一个非受信
//     地址 = 真实公网客户端；
//   - 直连公网：直接对端是公网客户端（不受信）→ gin 忽略 X-Forwarded-For，返回对端，
//     伪造的 XFF 无效。
//
// 显式配置仍以运维为准；运维可用关闭哨兵（none/off/disabled）强制回到“信任链关闭”。

// tkDefaultTrustedProxies 返回运维未显式配置 trusted_proxies 时默认信任的网段
// （loopback + RFC1918 私网 + IPv6 ULA）。唯一来源是 ip.PrivateCIDRs——反向代理
// （Caddy/nginx/Docker bridge）必然落在这些网段内。返回副本，避免调用方/gin
// 意外改动共享的包级切片。
func tkDefaultTrustedProxies() []string {
	return append([]string(nil), ip.PrivateCIDRs...)
}

// tkTrustedProxyOptOutTokens 是运维显式关闭信任链的哨兵值（大小写不敏感）。
// 适用于真正直接暴露公网、且不希望信任任何转发头的极少数场景。
var tkTrustedProxyOptOutTokens = map[string]struct{}{
	"none":     {},
	"off":      {},
	"disabled": {},
	"disable":  {},
}

// tkResolveTrustedProxies 根据运维配置决定 gin 的可信代理列表。
// 返回 (proxies, trust)：
//   - trust=false 表示应调用 SetTrustedProxies(nil)（信任链关闭）；
//   - trust=true  表示应调用 SetTrustedProxies(proxies)。
//
// 规则：
//  1. configured 含任一关闭哨兵 → (nil, false)；
//  2. configured 去空白后非空 → (该列表, true)（运维优先，行为不变）；
//  3. configured 为空/全空白/nil → (tkDefaultTrustedProxies, true)（自动信任私网）。
func tkResolveTrustedProxies(configured []string) (proxies []string, trust bool) {
	cleaned := make([]string, 0, len(configured))
	for _, p := range configured {
		v := strings.TrimSpace(p)
		if v == "" {
			continue
		}
		if _, ok := tkTrustedProxyOptOutTokens[strings.ToLower(v)]; ok {
			// 任一哨兵都强制关闭信任链。
			return nil, false
		}
		cleaned = append(cleaned, v)
	}
	if len(cleaned) == 0 {
		return tkDefaultTrustedProxies(), true
	}
	return cleaned, true
}
