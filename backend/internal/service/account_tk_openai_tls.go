package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// CanonicalCodexCLITLSProfileName is the seeded TLS template captured from the
// real Codex CLI (Rust reqwest + rustls). It pairs with the codex_* User-Agent /
// originator that OpenAI OAuth egress already sends. Source of truth:
// deploy/aws/stage0/tk_canonical_codex_cli.json → migration tk_109.
const CanonicalCodexCLITLSProfileName = "tk_canonical_codex_cli"

// isOpenAIOAuthTLSFingerprintEnabled is opt-in (default OFF) for OpenAI
// OAuth / SetupToken accounts: only extra.enable_tls_fingerprint=true turns it
// on. Unlike Kiro/Antigravity, chatgpt.com is Cloudflare-fronted and the
// fleet has a long history on Go crypto/tls, so rollout is per account.
// Profile is resolved by name in ResolveTLSProfile; an unseeded template
// yields nil → plain TLS (never the Node.js/Claude Code default).
func (a *Account) isOpenAIOAuthTLSFingerprintEnabled() bool {
	if a == nil || !a.IsOpenAIOAuthLike() || a.Extra == nil {
		return false
	}
	enabled, ok := a.Extra["enable_tls_fingerprint"].(bool)
	return ok && enabled
}

// openAIOAuthTLSFingerprintH2Windows selects the HTTP/2 flow-control windows
// for the Codex TLS-fingerprint transport. Default (missing/unknown value) is
// the Codex CLI windows for HTTP/2 fingerprint parity. extra
// tls_fingerprint_h2_windows="go_default" opts out: one pooled connection
// multiplexes every concurrent user stream of the account, and Codex's ~5 MiB
// connection window (sized for a single-user CLI) can stall all of them.
func (a *Account) openAIOAuthTLSFingerprintH2Windows() string {
	if a == nil || a.Extra == nil {
		return tlsfingerprint.HTTP2WindowsProfile
	}
	raw, _ := a.Extra["tls_fingerprint_h2_windows"].(string)
	if strings.EqualFold(strings.TrimSpace(raw), tlsfingerprint.HTTP2WindowsGoDefault) {
		return tlsfingerprint.HTTP2WindowsGoDefault
	}
	return tlsfingerprint.HTTP2WindowsProfile
}
