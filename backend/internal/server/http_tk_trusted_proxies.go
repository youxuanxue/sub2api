package server

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
)

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
