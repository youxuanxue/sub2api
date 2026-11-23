package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveCORSAllowedOriginsTK_EnvWins(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://a.example, https://b.example")
	got := resolveCORSAllowedOriginsTK([]string{"https://yaml.example"}, "https://tokenkey.dev")
	require.Equal(t, []string{"https://a.example", "https://b.example"}, got)
}

func TestResolveCORSAllowedOriginsTK_EmptyEnvFallsThroughToDerive(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	got := resolveCORSAllowedOriginsTK(nil, "https://tokenkey.dev")
	require.Equal(t, []string{"https://tokenkey.dev", "https://callmodel.io"}, got)
}

func TestResolveCORSAllowedOriginsTK_YamlWhenEnvUnset(t *testing.T) {
	got := resolveCORSAllowedOriginsTK([]string{" https://keep.example ", "", "https://keep.example"}, "https://tokenkey.dev")
	require.Equal(t, []string{"https://keep.example"}, got)
}

func TestResolveCORSAllowedOriginsTK_DeriveFromFrontendAndDomains(t *testing.T) {
	t.Setenv("SITE_DOMAIN", "tokenkey.dev")
	t.Setenv("GLOBAL_SITE_DOMAIN", "callmodel.io")
	got := resolveCORSAllowedOriginsTK(nil, "https://tokenkey.dev/")
	require.Equal(t, []string{
		"https://tokenkey.dev",
		"https://callmodel.io",
	}, got)
}

func TestResolveCORSAllowedOriginsTK_DeriveFrontendOnlyAddsCompanion(t *testing.T) {
	got := resolveCORSAllowedOriginsTK(nil, "https://tokenkey.dev")
	require.Equal(t, []string{"https://tokenkey.dev", "https://callmodel.io"}, got)
}

func TestResolveCORSAllowedOriginsTK_EmptyStaysEmptyWithoutFrontend(t *testing.T) {
	got := resolveCORSAllowedOriginsTK(nil, "")
	require.Empty(t, got)
}

func TestResolveCORSAllowedOriginsTK_SkipLocalhostSiteDomain(t *testing.T) {
	t.Setenv("SITE_DOMAIN", "localhost")
	got := resolveCORSAllowedOriginsTK(nil, "")
	require.Empty(t, got)
}

func TestOriginFromAbsoluteURL(t *testing.T) {
	require.Equal(t, "https://tokenkey.dev", originFromAbsoluteURL("https://tokenkey.dev/app"))
	require.Equal(t, "http://localhost:5173", originFromAbsoluteURL("http://localhost:5173/"))
	require.Equal(t, "", originFromAbsoluteURL("not-a-url"))
	require.Equal(t, "", originFromAbsoluteURL(""))
}
