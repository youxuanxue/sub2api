//go:build unit

package service

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func codexCLIProfileRow() *model.TLSFingerprintProfile {
	return &model.TLSFingerprintProfile{
		ID:                  9109,
		Name:                CanonicalCodexCLITLSProfileName,
		ShuffleExtensions:   true,
		CipherSuites:        []uint16{4866, 4865, 4867, 49196, 49195, 52393, 49200, 49199, 52392, 255},
		Curves:              []uint16{4588, 29, 23, 24},
		PointFormats:        []uint16{0},
		SignatureAlgorithms: []uint16{1283, 1027, 1539, 2055, 2054, 2053, 2052, 1537, 1281, 1025, 2308, 2309, 2310},
		ALPNProtocols:       []string{"h2", "http/1.1"},
		SupportedVersions:   []uint16{772, 771},
		KeyShareGroups:      []uint16{4588, 29},
		PSKModes:            []uint16{1},
		Extensions:          []uint16{0, 5, 10, 11, 13, 16, 23, 35, 43, 45, 51},
	}
}

func TestOpenAIOAuthTLS_OptInGate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		acc  *Account
		want bool
	}{
		{"oauth default off", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, false},
		{"oauth explicit true", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": true}}, true},
		{"oauth explicit false", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": false}}, false},
		{"oauth non-bool ignored", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": "true"}}, false},
		{"setup-token explicit true", &Account{Platform: PlatformOpenAI, Type: AccountTypeSetupToken, Extra: map[string]any{"enable_tls_fingerprint": true}}, true},
		{"apikey never", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"enable_tls_fingerprint": true}}, false},
		{"grok oauth never", &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": true}}, false},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, tc.acc.IsTLSFingerprintEnabled(), tc.name)
	}
	// Existing gates are untouched.
	require.True(t, (&Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth}).IsTLSFingerprintEnabled())
	require.True(t, (&Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": true}}).IsTLSFingerprintEnabled())
}

func TestResolveTLSProfile_OpenAIOAuthByNameNeverNodeDefault(t *testing.T) {
	t.Parallel()
	on := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": true}}

	require.Nil(t, newTLSSvcWithProfiles().ResolveTLSProfile(on), "unseeded codex profile must fall back to crypto/tls, never the Node.js default")

	svc := newTLSSvcWithProfiles(codexCLIProfileRow(), &model.TLSFingerprintProfile{Name: "custom"}) // cache ids: 1=codex, 2=custom
	got := svc.ResolveTLSProfile(on)
	require.NotNil(t, got)
	require.Equal(t, CanonicalCodexCLITLSProfileName, got.Name)
	require.True(t, got.ShuffleExtensions)

	bound := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": true, "tls_fingerprint_profile_id": float64(2)}}
	require.Equal(t, "custom", svc.ResolveTLSProfile(bound).Name, "explicit binding wins")

	require.Nil(t, svc.ResolveTLSProfile(&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}))

	gw := &OpenAIGatewayService{}
	require.Nil(t, gw.resolveOpenAITLSProfile(on), "unwired service keeps crypto/tls")
	gw.SetTLSFingerprintProfileService(svc)
	require.Equal(t, CanonicalCodexCLITLSProfileName, gw.resolveOpenAITLSProfile(on).Name)
	require.Nil(t, gw.resolveOpenAITLSProfile(&Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": true}}),
		"OpenAI gateway must not apply Anthropic TLS identities")
}

func TestOpenAIWSHandshakeTLSProfileDropsALPN(t *testing.T) {
	t.Parallel()
	base := codexCLIProfileRow().ToTLSProfile()
	ws := openAIWSHandshakeTLSProfile(base)
	require.NotContains(t, ws.Extensions, tlsALPNExtensionID)
	require.Equal(t, []uint16{0, 5, 10, 11, 13, 23, 35, 43, 45, 51}, ws.Extensions)
	require.Empty(t, ws.ALPNProtocols)
	require.Equal(t, base.CipherSuites, ws.CipherSuites)
	require.True(t, slices.Contains(base.Extensions, tlsALPNExtensionID), "base profile must not be mutated")
	require.Equal(t, []string{"h2", "http/1.1"}, base.ALPNProtocols)

	// Default extension order (no explicit list) still includes ALPN → pin http/1.1.
	def := openAIWSHandshakeTLSProfile(&tlsfingerprint.Profile{Name: "x"})
	require.Equal(t, []string{"http/1.1"}, def.ALPNProtocols)
	require.Nil(t, openAIWSHandshakeTLSProfile(nil))
}

func TestOpenAIWSTLSHTTPClientSelection(t *testing.T) {
	t.Parallel()
	d := &coderOpenAIWSClientDialer{proxyClients: map[string]*openAIWSProxyClientEntry{}}
	_, ok, err := d.tkOpenAIWSTLSHTTPClient(context.Background(), "")
	require.NoError(t, err)
	require.False(t, ok, "no profile → stock dialer")

	ctx := withOpenAIWSTLSProfile(context.Background(), codexCLIProfileRow().ToTLSProfile())
	for _, proxy := range []string{"", "http://user:pass@127.0.0.1:3128", "socks5://127.0.0.1:1080"} {
		c, ok, err := d.tkOpenAIWSTLSHTTPClient(ctx, proxy)
		require.NoError(t, err, proxy)
		require.True(t, ok, proxy)
		tr := c.Transport.(*http.Transport)
		require.NotNil(t, tr.DialTLSContext, proxy)
		require.False(t, tr.ForceAttemptHTTP2, "WS upgrade must stay HTTP/1.1")
		require.NotNil(t, tr.TLSNextProto)
		again, _, _ := d.tkOpenAIWSTLSHTTPClient(ctx, proxy)
		require.Same(t, c, again, "client cached per proxy+profile")
	}
	_, ok, err = d.tkOpenAIWSTLSHTTPClient(ctx, "https://proxy.example:443")
	require.NoError(t, err)
	require.False(t, ok, "HTTPS proxy cannot be tunnelled by the fingerprint dialer → keep existing path")

	pool := &openAIWSConnPool{}
	require.Nil(t, openAIWSTLSProfileFromContext(pool.withTLSProfile(context.Background(), &Account{})))
	pool.tlsProfileFor = func(*Account) *tlsfingerprint.Profile { return codexCLIProfileRow().ToTLSProfile() }
	require.NotNil(t, openAIWSTLSProfileFromContext(pool.withTLSProfile(context.Background(), &Account{})))
}
