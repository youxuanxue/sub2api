package service

import (
	"net/http"
	"strings"
)

// TK: See upstream Wei-Shaw/sub2api#3285 — Claude Code / Kiro clients send
// x-stainless-timeout (~120s) on API-key passthrough; forwarding it makes the
// upstream honor a ~125s cancel boundary on long non-stream requests. OpenAI
// passthrough already strips these headers by default; Anthropic/Kiro paths
// must match.

func isAnthropicPassthroughTimeoutHeader(lowerKey string) bool {
	switch strings.ToLower(strings.TrimSpace(lowerKey)) {
	case "x-stainless-timeout", "x-stainless-read-timeout", "x-stainless-connect-timeout", "x-request-timeout", "request-timeout", "grpc-timeout":
		return true
	default:
		return false
	}
}

func (s *GatewayService) anthropicPassthroughAllowTimeoutHeaders() bool {
	return s != nil && s.cfg != nil && s.cfg.Gateway.AnthropicPassthroughAllowTimeoutHeaders
}

func isAnthropicPassthroughAllowedHeader(lowerKey string, allowTimeoutHeaders bool) bool {
	if lowerKey == "" {
		return false
	}
	if isAnthropicPassthroughTimeoutHeader(lowerKey) {
		return allowTimeoutHeaders
	}
	return allowedHeaders[lowerKey]
}

func copyAnthropicPassthroughHeaders(dst http.Header, src http.Header, allowTimeoutHeaders bool) {
	if dst == nil || src == nil {
		return
	}
	for key, values := range src {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		if !isAnthropicPassthroughAllowedHeader(lowerKey, allowTimeoutHeaders) {
			continue
		}
		wireKey := resolveWireCasing(key)
		for _, v := range values {
			addHeaderRaw(dst, wireKey, v)
		}
	}
}

func collectAnthropicPassthroughTimeoutHeaders(h http.Header) []string {
	if h == nil {
		return nil
	}
	var matched []string
	for key, values := range h {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		if !isAnthropicPassthroughTimeoutHeader(lowerKey) {
			continue
		}
		entry := lowerKey
		if len(values) > 0 {
			entry = lowerKey + "=" + strings.Join(values, "|")
		}
		matched = append(matched, entry)
	}
	return matched
}
