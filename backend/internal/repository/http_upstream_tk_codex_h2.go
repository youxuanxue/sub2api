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
// Go's http2 client derives MAX_HEADER_LIST_SIZE from MaxResponseHeaderBytes
// (Go adds 320 bytes of per-field overhead). The constants below copy only
// MAX_FRAME_SIZE and MAX_HEADER_LIST_SIZE. INITIAL_WINDOW_SIZE and the
// connection WINDOW_UPDATE stay at Go's defaults: those Codex values are the
// single-user ~5 MiB connection window. Pseudo-header order stays Go's
// a,m,p,s (codex: m,s,a,p); net/http exposes no knob for it.
const (
	codexH2MaxFrameSize                = 16384
	codexH2MaxResponseHeaderBytesForGo = 16384 - 320
)

// applyTLSProfileHTTP2Settings keeps the Codex CLI frame size and header-list
// size on the Codex TLS profile. Flow-control windows stay at Go's defaults
// (4 MiB stream / 1 GiB connection). The captured Codex windows are 2 MiB
// stream / ~5 MiB connection, sized for one CLI user; a pooled gateway
// connection multiplexes the account's concurrent streams, and that ~5 MiB
// connection window lets a few slow readers stall the rest (peer RST
// INTERNAL_ERROR). Frame size and header-list size do not throttle throughput.
// Other profile names keep Go's HTTP/2 settings entirely.
func applyTLSProfileHTTP2Settings(transport *http.Transport, profile *tlsfingerprint.Profile) {
	if transport == nil || profile == nil || profile.Name != service.CanonicalCodexCLITLSProfileName {
		return
	}
	if transport.HTTP2 == nil {
		transport.HTTP2 = &http.HTTP2Config{}
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

// tlsFingerprintPoolKeySuffix marks fingerprint transports in the client pool.
// Window size is not part of the key: the Codex profile always uses Go's
// flow-control windows.
func tlsFingerprintPoolKeySuffix(*tlsfingerprint.Profile) string {
	return ":tls"
}
