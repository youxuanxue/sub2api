package repository

import (
	"net"
	"strings"
)

// upstreamProtocolModeCloudcodeH2 keeps HTTP/2 for Google Code Assist and
// disables proactive health PINGs. The two hosts below answer a quiet
// Gemini thinking stream with GOAWAY ENHANCE_YOUR_CALM too_many_pings when
// the shared Antigravity transport PINGs every 10s. Other hosts stay on the
// long_stream 10s/5s or OpenAI 15s/15s transports; this mode is selected from
// the request host and isolated in the client cache.
const upstreamProtocolModeCloudcodeH2 = "cloudcode_h2"

func googleCodeAssistDisablesHTTP2Ping(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	switch host {
	case "cloudcode-pa.googleapis.com", "daily-cloudcode-pa.googleapis.com":
		return true
	default:
		return false
	}
}

// cloudcodeHTTP2ProtocolMode overrides only non-OpenAI transports. OpenAI
// keeps its own ping policy even if a request is somehow aimed at Code Assist.
func cloudcodeHTTP2ProtocolMode(protocolMode, host string) string {
	if isOpenAITLSProtocolMode(protocolMode) {
		return protocolMode
	}
	if googleCodeAssistDisablesHTTP2Ping(host) {
		return upstreamProtocolModeCloudcodeH2
	}
	return protocolMode
}
