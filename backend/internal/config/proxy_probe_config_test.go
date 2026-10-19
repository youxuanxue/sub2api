//go:build unit

package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeProxyProbeURLs(t *testing.T) {
	t.Parallel()

	got, err := normalizeProxyProbeURLs([]ProbeURLConfig{
		{URL: " https://chatgpt.com/cdn-cgi/trace ", Parser: " CHATGPT-TRACE "},
		{URL: "https://api64.ipify.org?format=json", Parser: "ipify"},
	})
	require.NoError(t, err)
	require.Equal(t, []ProbeURLConfig{
		{URL: "https://chatgpt.com/cdn-cgi/trace", Parser: "chatgpt-trace"},
		{URL: "https://api64.ipify.org?format=json", Parser: "ipify"},
	}, got)
}

func TestNormalizeProxyProbeURLsRejectsInvalidEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		target  ProbeURLConfig
		wantErr string
	}{
		{name: "missing URL", target: ProbeURLConfig{Parser: "ipify"}, wantErr: "url is required"},
		{name: "missing parser", target: ProbeURLConfig{URL: "https://example.com"}, wantErr: "parser is required"},
		{name: "unknown parser", target: ProbeURLConfig{URL: "https://example.com", Parser: "ip_api"}, wantErr: "unsupported parser"},
		{name: "relative URL", target: ProbeURLConfig{URL: "/cdn-cgi/trace", Parser: "chatgpt-trace"}, wantErr: "invalid url"},
		{name: "unsupported scheme", target: ProbeURLConfig{URL: "ftp://example.com/file", Parser: "ipify"}, wantErr: "scheme must be http or https"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := normalizeProxyProbeURLs([]ProbeURLConfig{tt.target})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestNormalizeProxyProbeURLsAcceptsDomesticParsers(t *testing.T) {
	t.Parallel()
	got, err := normalizeProxyProbeURLs([]ProbeURLConfig{
		{URL: "https://myip.ipip.net/json", Parser: "ipip-json"},
		{URL: "http://ip9.com.cn/get", Parser: "ip9"},
		{URL: "https://r.inews.qq.com/api/ip2city", Parser: "qq-ip2city"},
	})
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, "ipip-json", got[0].Parser)
	require.Equal(t, "ip9", got[1].Parser)
	require.Equal(t, "qq-ip2city", got[2].Parser)
}

func TestNormalizeProxyProbePreset(t *testing.T) {
	t.Parallel()
	got, err := normalizeProxyProbePreset(" Domestic ")
	require.NoError(t, err)
	require.Equal(t, "domestic", got)

	_, err = normalizeProxyProbePreset("cn")
	require.ErrorContains(t, err, "must be empty, default, or domestic")
}
