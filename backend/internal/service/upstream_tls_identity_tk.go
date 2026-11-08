package service

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// Upstream TLS identity evidence, observable in prod without Debug logging.
//
// Each successful upstream HTTPS response and WSS upgrade reports which TLS
// stack actually dialled the connection (uTLS profile name or crypto/tls) and
// what was negotiated (HTTP proto + ALPN). Emission is rate-limited per
// identity key (account, transport, stack, profile, proto, ALPN, host, proxy),
// so volume is bounded by distinct keys, not by request rate. Each emission
// carries the count of identical requests suppressed since the previous one.
// The "audit." component makes OpsSystemLogSink persist this Info event into
// ops_system_logs (account_id column + extra JSONB).
const (
	UpstreamTLSIdentityComponent = "audit.tls_identity"
	UpstreamTLSIdentityMessage   = "upstream_tls_identity"

	UpstreamTLSIdentityTransportHTTPS = "https"
	UpstreamTLSIdentityTransportWSS   = "wss"

	// UpstreamTLSStackUTLS: handshake built from TLSProfile by the uTLS dialer.
	UpstreamTLSStackUTLS = "utls"
	// UpstreamTLSStackStdlib: Go crypto/tls; TLSProfile is always empty.
	UpstreamTLSStackStdlib = "crypto/tls"
	// UpstreamTLSStackNone: the connection had no TLS state (plain HTTP).
	UpstreamTLSStackNone = "none"

	upstreamTLSIdentityLogInterval = 10 * time.Minute
	upstreamTLSIdentityMaxKeys     = 8192
)

// UpstreamTLSIdentity is the TLS identity actually used by one upstream
// request. Build it with UpstreamTLSIdentityFromResponse so the stack reflects
// the negotiated connection rather than account configuration.
type UpstreamTLSIdentity struct {
	AccountID  int64
	Transport  string
	TLSStack   string
	TLSProfile string
	HTTPProto  string
	ALPN       string
	Host       string
	ProxyHost  string
}

// UpstreamTLSIdentityFromResponse derives the used identity. usedProfile is the
// uTLS profile name of the dialer that built the connection, or "" when the
// transport used crypto/tls.
func UpstreamTLSIdentityFromResponse(accountID int64, transport, usedProfile, proxyURL string, resp *http.Response) UpstreamTLSIdentity {
	id := UpstreamTLSIdentity{
		AccountID: accountID,
		Transport: transport,
		ProxyHost: upstreamTLSIdentityProxyHost(proxyURL),
	}
	if resp == nil {
		return id
	}
	id.HTTPProto = resp.Proto
	if resp.Request != nil && resp.Request.URL != nil {
		id.Host = resp.Request.URL.Host // host[:port]; never path/query
	}
	usedProfile = strings.TrimSpace(usedProfile)
	switch {
	case resp.TLS == nil:
		id.TLSStack = UpstreamTLSStackNone
	case usedProfile != "":
		id.TLSStack, id.TLSProfile = UpstreamTLSStackUTLS, usedProfile
		id.ALPN = resp.TLS.NegotiatedProtocol
	default:
		id.TLSStack = UpstreamTLSStackStdlib
		id.ALPN = resp.TLS.NegotiatedProtocol
	}
	return id
}

type upstreamTLSIdentityLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	maxKeys  int
	now      func() time.Time
	entries  map[UpstreamTLSIdentity]*upstreamTLSIdentityEntry
}

type upstreamTLSIdentityEntry struct {
	lastEmit   time.Time
	suppressed int64
}

func newUpstreamTLSIdentityLimiter(interval time.Duration, maxKeys int, now func() time.Time) *upstreamTLSIdentityLimiter {
	return &upstreamTLSIdentityLimiter{
		interval: interval,
		maxKeys:  maxKeys,
		now:      now,
		entries:  make(map[UpstreamTLSIdentity]*upstreamTLSIdentityEntry),
	}
}

// admit reports whether id should be emitted now and how many identical
// requests were suppressed since the previous emission.
func (l *upstreamTLSIdentityLimiter) admit(id UpstreamTLSIdentity) (bool, int64) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[id]; ok {
		if now.Sub(e.lastEmit) < l.interval {
			e.suppressed++
			return false, 0
		}
		suppressed := e.suppressed
		e.lastEmit, e.suppressed = now, 0
		return true, suppressed
	}
	if len(l.entries) >= l.maxKeys {
		for k, e := range l.entries {
			if now.Sub(e.lastEmit) >= l.interval {
				delete(l.entries, k)
			}
		}
		if len(l.entries) >= l.maxKeys {
			// Bounded memory beats exact dedup: a reset only re-emits once per key.
			l.entries = make(map[UpstreamTLSIdentity]*upstreamTLSIdentityEntry)
		}
	}
	l.entries[id] = &upstreamTLSIdentityEntry{lastEmit: now}
	return true, 0
}

var defaultUpstreamTLSIdentityLimiter = newUpstreamTLSIdentityLimiter(
	upstreamTLSIdentityLogInterval, upstreamTLSIdentityMaxKeys, time.Now)

// RecordUpstreamTLSIdentity emits the identity at most once per key per interval.
func RecordUpstreamTLSIdentity(ctx context.Context, id UpstreamTLSIdentity) {
	recordUpstreamTLSIdentity(ctx, defaultUpstreamTLSIdentityLimiter, id)
}

func recordUpstreamTLSIdentity(ctx context.Context, limiter *upstreamTLSIdentityLimiter, id UpstreamTLSIdentity) {
	if limiter == nil || id.AccountID <= 0 || id.TLSStack == "" {
		return
	}
	ok, suppressed := limiter.admit(id)
	if !ok {
		return
	}
	logger.FromContext(ctx).Info(UpstreamTLSIdentityMessage,
		zap.String("component", UpstreamTLSIdentityComponent),
		zap.Int64("account_id", id.AccountID),
		zap.String("transport", id.Transport),
		zap.String("tls_stack", id.TLSStack),
		zap.String("tls_profile", id.TLSProfile),
		zap.String("http_proto", id.HTTPProto),
		zap.String("alpn", id.ALPN),
		zap.String("upstream_host", id.Host),
		zap.String("proxy_host", id.ProxyHost),
		zap.Int64("suppressed_since_last", suppressed),
		zap.Int64("log_interval_sec", int64(limiter.interval/time.Second)),
	)
}

// upstreamTLSIdentityProxyHost keeps host[:port] only; userinfo, path and
// query (which can carry credentials) are dropped.
func upstreamTLSIdentityProxyHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}
