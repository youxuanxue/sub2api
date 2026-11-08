package service

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// tlsALPNExtensionID is the TLS application_layer_protocol_negotiation extension.
const tlsALPNExtensionID uint16 = 16

type openAIWSTLSProfileCtxKey struct{}

// withOpenAIWSTLSProfile attaches the account's TLS profile to a WS dial. nil
// keeps the stock coder/websocket crypto/tls path.
func withOpenAIWSTLSProfile(ctx context.Context, profile *tlsfingerprint.Profile) context.Context {
	if profile == nil {
		return ctx
	}
	return context.WithValue(ctx, openAIWSTLSProfileCtxKey{}, profile)
}

func openAIWSTLSProfileFromContext(ctx context.Context) *tlsfingerprint.Profile {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(openAIWSTLSProfileCtxKey{}).(*tlsfingerprint.Profile)
	return p
}

// openAIWSHandshakeTLSProfile derives the WebSocket ClientHello from the HTTPS
// profile. Real Codex CLI (tokio-tungstenite + rustls) sends the identical hello
// minus the ALPN extension when opening wss://chatgpt.com/backend-api/codex/responses
// (JA4 t13d101000_… vs HTTPS t13d1011h2_…), so the upgrade stays HTTP/1.1.
func openAIWSHandshakeTLSProfile(profile *tlsfingerprint.Profile) *tlsfingerprint.Profile {
	if profile == nil {
		return nil
	}
	out := *profile
	out.ALPNProtocols = nil
	if len(profile.Extensions) > 0 {
		out.Extensions = make([]uint16, 0, len(profile.Extensions))
		for _, id := range profile.Extensions {
			if id != tlsALPNExtensionID {
				out.Extensions = append(out.Extensions, id)
			}
		}
	} else {
		// Empty Extensions means the dialer's Node.js default order, which
		// always includes ALPN; WS must never advertise h2, so pin http/1.1.
		out.ALPNProtocols = []string{"http/1.1"}
	}
	return &out
}

var openAIWSTLSClients sync.Map // key string → *http.Client

func openAIWSTLSClientKey(proxy string, profile *tlsfingerprint.Profile) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%+v", proxy, *profile)))
	return hex.EncodeToString(sum[:16])
}

// tkOpenAIWSTLSHTTPClient returns an HTTP/1.1-only client whose TLS handshake
// uses the WS-derived uTLS profile, routed through the account proxy exactly
// like the HTTP fingerprint transport (direct / HTTP CONNECT / SOCKS5).
// ok=false means "keep the existing client" (no profile, or a proxy scheme the
// fingerprint dialers cannot tunnel, e.g. https://).
func (d *coderOpenAIWSClientDialer) tkOpenAIWSTLSHTTPClient(ctx context.Context, proxyURL string) (*http.Client, bool, error) {
	base := openAIWSTLSProfileFromContext(ctx)
	if base == nil {
		return nil, false, nil
	}
	profile := openAIWSHandshakeTLSProfile(base)
	proxy := strings.TrimSpace(proxyURL)
	key := openAIWSTLSClientKey(proxy, profile)
	if v, ok := openAIWSTLSClients.Load(key); ok {
		if cached, ok := v.(*http.Client); ok {
			return cached, true, nil
		}
	}
	transport := &http.Transport{
		MaxIdleConns:        openAIWSProxyTransportMaxIdleConns,
		MaxIdleConnsPerHost: openAIWSProxyTransportMaxIdleConnsPerHost,
		IdleConnTimeout:     openAIWSProxyTransportIdleConnTimeout,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   false,
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	if proxy == "" {
		transport.DialTLSContext = tlsfingerprint.NewDialer(profile, nil).DialTLSContext
	} else {
		parsed, err := url.Parse(proxy)
		if err != nil {
			return nil, false, fmt.Errorf("invalid proxy url: %w", err)
		}
		switch strings.ToLower(parsed.Scheme) {
		case "socks5", "socks5h":
			transport.DialTLSContext = tlsfingerprint.NewSOCKS5ProxyDialer(profile, parsed).DialTLSContext
		case "http":
			transport.DialTLSContext = tlsfingerprint.NewHTTPProxyDialer(profile, parsed).DialTLSContext
		default:
			slog.Debug("openai_ws_tls_fingerprint_proxy_scheme_unsupported", "scheme", parsed.Scheme)
			return nil, false, nil
		}
	}
	client := &http.Client{Transport: transport}
	if actual, loaded := openAIWSTLSClients.LoadOrStore(key, client); loaded {
		if cached, ok := actual.(*http.Client); ok {
			return cached, true, nil
		}
	}
	return client, true, nil
}

func (p *openAIWSConnPool) withTLSProfile(ctx context.Context, account *Account) context.Context {
	ctx = withOpenAIWSDialAccount(ctx, account)
	if p == nil || p.tlsProfileFor == nil || account == nil {
		return ctx
	}
	return withOpenAIWSTLSProfile(ctx, p.tlsProfileFor(account))
}

type openAIWSDialAccountCtxKey struct{}

// withOpenAIWSDialAccount tags a WS dial with its account so the dialer can
// report the used TLS identity for flagged and non-flagged accounts alike.
func withOpenAIWSDialAccount(ctx context.Context, account *Account) context.Context {
	if ctx == nil || account == nil || account.ID <= 0 {
		return ctx
	}
	return context.WithValue(ctx, openAIWSDialAccountCtxKey{}, account.ID)
}

// withOpenAIWSDialIdentity attaches both the account and its TLS profile.
func withOpenAIWSDialIdentity(ctx context.Context, account *Account, profile *tlsfingerprint.Profile) context.Context {
	return withOpenAIWSTLSProfile(withOpenAIWSDialAccount(ctx, account), profile)
}

// tkRecordOpenAIWSTLSIdentity reports the TLS identity of a successful WS
// upgrade. The profile counts as used only when the dial went through the uTLS
// client from tkOpenAIWSTLSHTTPClient (the only WS transport with a custom
// DialTLSContext); every other path, including https:// proxies, is crypto/tls.
func tkRecordOpenAIWSTLSIdentity(ctx context.Context, proxyURL string, client *http.Client, resp *http.Response) {
	if ctx == nil || resp == nil {
		return
	}
	accountID, _ := ctx.Value(openAIWSDialAccountCtxKey{}).(int64)
	if accountID <= 0 {
		return
	}
	used := ""
	if profile := openAIWSTLSProfileFromContext(ctx); profile != nil && client != nil {
		if tr, ok := client.Transport.(*http.Transport); ok && tr.DialTLSContext != nil {
			used = profile.Name
			if used == "" {
				used = "unnamed"
			}
		}
	}
	RecordUpstreamTLSIdentity(ctx, UpstreamTLSIdentityFromResponse(accountID, UpstreamTLSIdentityTransportWSS, used, proxyURL, resp))
}
