package repository

import (
	"net/http"
	"net/url"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Codex CLI (hyper 1.x / h2 0.4) HTTP/2 preface captured from real codex 0.167.0
// against a local capture endpoint (deploy/aws/stage0/tk_canonical_codex_cli.json):
//
//	SETTINGS ENABLE_PUSH=0, INITIAL_WINDOW_SIZE=2097152, MAX_FRAME_SIZE=16384,
//	MAX_HEADER_LIST_SIZE=16384; WINDOW_UPDATE(conn)=5177345.
//
// Go's http2 client derives these from HTTP2Config / MaxResponseHeaderBytes;
// the values below reproduce the same SETTINGS + WINDOW_UPDATE bytes (Go adds
// 320 bytes of per-field overhead to MaxResponseHeaderBytes when advertising
// MAX_HEADER_LIST_SIZE). Pseudo-header order stays Go's a,m,p,s (codex: m,s,a,p);
// net/http exposes no knob for it.
const (
	codexH2InitialStreamWindow         = 2097152
	codexH2ConnectionWindow            = 5177345
	codexH2MaxFrameSize                = 16384
	codexH2MaxResponseHeaderBytesForGo = 16384 - 320
)

// applyTLSProfileHTTP2Settings aligns the HTTP/2 preface with the client whose
// TLS identity the profile mimics. Only the Codex CLI profile is tuned today;
// other profiles keep Go defaults.
//
// profile.HTTP2Windows=go_default keeps Go's flow-control windows (4 MiB
// stream / 1 GiB connection): one pooled connection carries every concurrent
// user stream of the account, and the single-user Codex ~5 MiB connection
// window lets a few slow readers stall all of them (peer RST INTERNAL_ERROR).
// MAX_FRAME_SIZE / MAX_HEADER_LIST_SIZE stay at Codex values in both modes:
// they never throttle throughput, so the A/B changes only the window bytes.
func applyTLSProfileHTTP2Settings(transport *http.Transport, profile *tlsfingerprint.Profile) {
	if transport == nil || profile == nil || profile.Name != service.CanonicalCodexCLITLSProfileName {
		return
	}
	if transport.HTTP2 == nil {
		transport.HTTP2 = &http.HTTP2Config{}
	}
	if !profile.UsesGoDefaultHTTP2Windows() {
		transport.HTTP2.MaxReceiveBufferPerStream = codexH2InitialStreamWindow
		transport.HTTP2.MaxReceiveBufferPerConnection = codexH2ConnectionWindow
	}
	transport.HTTP2.MaxReadFrameSize = codexH2MaxFrameSize
	transport.MaxResponseHeaderBytes = codexH2MaxResponseHeaderBytesForGo
}

// resolveTLSFingerprintProtocolMode keeps every non-OpenAI fingerprint
// transport on its historical default key/long_stream_h2 path; OpenAI follows
// the same gateway.openai_http2 policy (incl. HTTP-proxy H1 fallback) as Do.
func (s *httpUpstreamService) resolveTLSFingerprintProtocolMode(upstreamProfile service.HTTPUpstreamProfile, proxyKey string, parsedProxy *url.URL) string {
	if upstreamProfile != service.HTTPUpstreamProfileOpenAI {
		return upstreamProtocolModeDefault
	}
	return s.resolveProtocolMode(upstreamProfile, proxyKey, parsedProxy)
}

func isOpenAITLSProtocolMode(mode string) bool {
	switch mode {
	case upstreamProtocolModeOpenAIH2, upstreamProtocolModeOpenAIH1, upstreamProtocolModeOpenAIH1Fallback:
		return true
	}
	return false
}

func isOpenAITLSHTTP1Mode(mode string) bool {
	return mode == upstreamProtocolModeOpenAIH1 || mode == upstreamProtocolModeOpenAIH1Fallback
}

// tlsFingerprintPoolKeySuffix makes the window policy part of the pool key so
// toggling extra.tls_fingerprint_h2_windows rebuilds the client. The default
// policy adds nothing, keeping existing pool keys stable.
func tlsFingerprintPoolKeySuffix(profile *tlsfingerprint.Profile) string {
	if profile.UsesGoDefaultHTTP2Windows() {
		return ":tls|h2win:" + tlsfingerprint.HTTP2WindowsGoDefault
	}
	return ":tls"
}
