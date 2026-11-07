package repository

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
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
	tkCanonicalCodexCLITLSProfileName  = "tk_canonical_codex_cli"
	codexH2InitialStreamWindow         = 2097152
	codexH2ConnectionWindow            = 5177345
	codexH2MaxFrameSize                = 16384
	codexH2MaxResponseHeaderBytesForGo = 16384 - 320
)

// applyTLSProfileHTTP2Settings aligns the HTTP/2 preface with the client whose
// TLS identity the profile mimics. Only the Codex CLI profile is tuned today;
// other profiles keep Go defaults.
func applyTLSProfileHTTP2Settings(transport *http.Transport, profile *tlsfingerprint.Profile) {
	if transport == nil || profile == nil || profile.Name != tkCanonicalCodexCLITLSProfileName {
		return
	}
	if transport.HTTP2 == nil {
		transport.HTTP2 = &http.HTTP2Config{}
	}
	transport.HTTP2.MaxReceiveBufferPerStream = codexH2InitialStreamWindow
	transport.HTTP2.MaxReceiveBufferPerConnection = codexH2ConnectionWindow
	transport.HTTP2.MaxReadFrameSize = codexH2MaxFrameSize
	transport.MaxResponseHeaderBytes = codexH2MaxResponseHeaderBytesForGo
}
