package repository

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// recordUpstreamTLSIdentity reports the TLS identity actually used by a
// successful upstream response. A configured profile only counts as used when
// the cached client's transport dials TLS through the uTLS fingerprint dialer:
// buildUpstreamTransportWithTLSFingerprint silently falls back to crypto/tls
// for https:// and unknown proxy schemes, and that fallback must be visible.
func recordUpstreamTLSIdentity(resp *http.Response, entry *upstreamClientEntry, accountID int64, proxyURL string, profile *tlsfingerprint.Profile) {
	if resp == nil || resp.Request == nil {
		return
	}
	used := ""
	if profile != nil && upstreamEntryUsesTLSFingerprint(entry) {
		used = profile.Name
		if used == "" {
			used = "unnamed"
		}
	}
	id := service.UpstreamTLSIdentityFromResponse(accountID, service.UpstreamTLSIdentityTransportHTTPS, used, proxyURL, resp)
	service.RecordUpstreamTLSIdentity(resp.Request.Context(), id)
}

func upstreamEntryUsesTLSFingerprint(entry *upstreamClientEntry) bool {
	if entry == nil || entry.client == nil {
		return false
	}
	tr, ok := entry.client.Transport.(*http.Transport)
	return ok && tr.DialTLSContext != nil
}
