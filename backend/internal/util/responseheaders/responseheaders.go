package responseheaders

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// defaultAllowed 定义允许透传的响应头白名单
// 注意：以下头部由 Go HTTP 包自动处理，不应手动设置：
//   - content-length: 由 ResponseWriter 根据实际写入数据自动设置
//   - transfer-encoding: 由 HTTP 库根据需要自动添加/移除
//   - connection: 由 HTTP 库管理连接复用
var defaultAllowed = map[string]struct{}{
	"content-type":                   {},
	"content-encoding":               {},
	"content-language":               {},
	"cache-control":                  {},
	"etag":                           {},
	"last-modified":                  {},
	"expires":                        {},
	"vary":                           {},
	"date":                           {},
	"x-request-id":                   {},
	"x-ratelimit-limit-requests":     {},
	"x-ratelimit-limit-tokens":       {},
	"x-ratelimit-remaining-requests": {},
	"x-ratelimit-remaining-tokens":   {},
	"x-ratelimit-reset-requests":     {},
	"x-ratelimit-reset-tokens":       {},
	"retry-after":                    {},
	"location":                       {},
	"www-authenticate":               {},
	// Codex uses this response header to avoid estimating reasoning tokens a
	// second time when upstream usage already includes them.
	"x-reasoning-included": {},
}

// hopByHopHeaders 是跳过的 hop-by-hop 头部，这些头部由 HTTP 库自动处理
var hopByHopHeaders = map[string]struct{}{
	"content-length":    {},
	"transfer-encoding": {},
	"connection":        {},
}

type CompiledHeaderFilter struct {
	allowed     map[string]struct{}
	forceRemove map[string]struct{}
}

var defaultCompiledHeaderFilter = CompileHeaderFilter(config.ResponseHeaderConfig{})

func CompileHeaderFilter(cfg config.ResponseHeaderConfig) *CompiledHeaderFilter {
	allowed := make(map[string]struct{}, len(defaultAllowed)+len(cfg.AdditionalAllowed))
	for key := range defaultAllowed {
		allowed[key] = struct{}{}
	}
	// 关闭时只使用默认白名单，additional/force_remove 不生效
	if cfg.Enabled {
		for _, key := range cfg.AdditionalAllowed {
			normalized := strings.ToLower(strings.TrimSpace(key))
			if normalized == "" {
				continue
			}
			allowed[normalized] = struct{}{}
		}
	}

	forceRemove := map[string]struct{}{}
	if cfg.Enabled {
		forceRemove = make(map[string]struct{}, len(cfg.ForceRemove))
		for _, key := range cfg.ForceRemove {
			normalized := strings.ToLower(strings.TrimSpace(key))
			if normalized == "" {
				continue
			}
			forceRemove[normalized] = struct{}{}
		}
	}

	return &CompiledHeaderFilter{
		allowed:     allowed,
		forceRemove: forceRemove,
	}
}

func FilterHeaders(src http.Header, filter *CompiledHeaderFilter) http.Header {
	if filter == nil {
		filter = defaultCompiledHeaderFilter
	}

	filtered := make(http.Header, len(src))
	for key, values := range src {
		lower := strings.ToLower(key)
		if _, blocked := filter.forceRemove[lower]; blocked {
			continue
		}
		if _, ok := filter.allowed[lower]; !ok {
			continue
		}
		// 跳过 hop-by-hop 头部，这些由 HTTP 库自动处理
		if _, isHopByHop := hopByHopHeaders[lower]; isHopByHop {
			continue
		}
		for _, value := range values {
			filtered.Add(key, value)
		}
	}
	return filtered
}

func WriteFilteredHeaders(dst http.Header, src http.Header, filter *CompiledHeaderFilter) {
	filtered := FilterHeaders(src, filter)
	for key, values := range filtered {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

// gatewayRuntimeHeaders are set by the gateway while a response is in flight
// (SSE buffering and connection hints). They are not upstream identity and
// must survive allowlist filtering.
var gatewayRuntimeHeaders = map[string]struct{}{
	"connection":        {},
	"x-accel-buffering": {},
}

// RetainClientResponseHeaders removes supplier response headers from dst.
// Headers already present in preexisting are restored, so an upstream write
// cannot replace gateway security headers or the gateway request id.
// filter nil uses the default client allowlist.
func RetainClientResponseHeaders(dst http.Header, preexisting http.Header, filter *CompiledHeaderFilter) {
	if dst == nil {
		return
	}
	if filter == nil {
		filter = defaultCompiledHeaderFilter
	}
	type prior struct {
		key    string
		values []string
	}
	previous := make(map[string]prior, len(preexisting))
	for key, values := range preexisting {
		copied := append([]string(nil), values...)
		previous[strings.ToLower(key)] = prior{key: key, values: copied}
	}

	keys := make([]string, 0, len(dst))
	for key := range dst {
		keys = append(keys, key)
	}
	for _, key := range keys {
		lower := strings.ToLower(key)
		if _, blocked := filter.forceRemove[lower]; blocked {
			dst.Del(key)
			continue
		}
		if snap, ok := previous[lower]; ok {
			dst.Del(key)
			for _, value := range snap.values {
				dst.Add(snap.key, value)
			}
			continue
		}
		if _, keep := gatewayRuntimeHeaders[lower]; keep {
			continue
		}
		if _, hop := hopByHopHeaders[lower]; hop {
			dst.Del(key)
			continue
		}
		if _, ok := filter.allowed[lower]; !ok {
			dst.Del(key)
		}
	}
}
