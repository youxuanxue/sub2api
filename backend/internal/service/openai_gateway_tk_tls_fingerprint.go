package service

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// SetTLSFingerprintProfileService injects the shared TLS profile cache. Wired in
// handler.ProvideProtocolRoutedOpenAIGatewayHandler (setter keeps the upstream
// NewOpenAIGatewayService signature untouched).
func (s *OpenAIGatewayService) SetTLSFingerprintProfileService(svc *TLSFingerprintProfileService) {
	if s == nil {
		return
	}
	s.tlsFPProfileService = svc
}

// resolveOpenAITLSProfile is the single OpenAI-gateway owner for the outbound
// TLS identity. Only OpenAI OAuth/SetupToken accounts with
// extra.enable_tls_fingerprint=true get a profile (tk_canonical_codex_cli unless
// an explicit tls_fingerprint_profile_id is bound); everything else returns nil
// so DoWithTLS / the WS dialer keep the existing crypto/tls path.
func (s *OpenAIGatewayService) resolveOpenAITLSProfile(account *Account) *tlsfingerprint.Profile {
	if s == nil || s.tlsFPProfileService == nil || account == nil || !account.IsOpenAIOAuthLike() {
		return nil
	}
	return s.tlsFPProfileService.ResolveTLSProfile(account)
}

// doOpenAIUpstreamWithAccountTLS sends through DoWithTLS only when the account
// opted into the Codex TLS identity; otherwise it is exactly the historical
// httpUpstream.Do call (so non-opted accounts and HTTPUpstream doubles see no
// behavior change).
func (s *OpenAIGatewayService) doOpenAIUpstreamWithAccountTLS(req *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	if profile := s.resolveOpenAITLSProfile(account); profile != nil {
		return s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, withOpenAITLSHTTP2Windows(profile, account))
	}
	return s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
}

// withOpenAITLSHTTP2Windows returns a profile copy carrying the account's
// HTTP/2 window policy; the repository keys and builds the transport from it.
func withOpenAITLSHTTP2Windows(profile *tlsfingerprint.Profile, account *Account) *tlsfingerprint.Profile {
	if profile == nil {
		return nil
	}
	out := *profile
	out.HTTP2Windows = account.openAIOAuthTLSFingerprintH2Windows()
	return &out
}
