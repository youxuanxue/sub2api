package admin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func validGeminiWebImportBundle() geminiWebSessionImportRequest {
	return geminiWebSessionImportRequest{
		Format:    geminiWebSessionImportFormat,
		UserAgent: "Mozilla/5.0",
		Cookies: []map[string]any{
			{"name": "SID", "value": "secret", "domain": ".google.com"},
			{"name": "G_AUTHUSER_H", "value": "0", "domain": "gemini.google.com"},
		},
	}
}

func TestValidateGeminiWebSessionImportAcceptsExportedCookieDomains(t *testing.T) {
	require.NoError(t, validateGeminiWebSessionImport(validGeminiWebImportBundle()))
}

func TestValidateGeminiWebSessionImportRejectsExternalDomain(t *testing.T) {
	bundle := validGeminiWebImportBundle()
	bundle.Cookies[0]["domain"] = "evil.example"
	require.EqualError(t, validateGeminiWebSessionImport(bundle), "gemini Web session contains a cookie from an unsupported domain")
}

func TestValidateGeminiWebSessionImportRejectsMalformedBundle(t *testing.T) {
	bundle := validGeminiWebImportBundle()
	bundle.Format = "other"
	require.Error(t, validateGeminiWebSessionImport(bundle))

	bundle = validGeminiWebImportBundle()
	bundle.Cookies = nil
	require.Error(t, validateGeminiWebSessionImport(bundle))
}

func TestValidateGeminiWebSessionImportRejectsWorkerIncompatibleCookies(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"empty value", ""},
		{"string expires", "123"},
		{"array path", []any{}},
		{"string secure", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := validGeminiWebImportBundle()
			bundle.Cookies[0][map[string]string{
				"empty value": "value", "string expires": "expires",
				"array path": "path", "string secure": "secure",
			}[tc.name]] = tc.value
			require.Error(t, validateGeminiWebSessionImport(bundle))
		})
	}
}

func TestNormalizeGeminiWebCookiesCanonicalizesDomainAndPath(t *testing.T) {
	cookies, err := normalizeGeminiWebCookies([]map[string]any{{
		"name": "SID", "value": "secret", "domain": " .GOOGLE.COM ",
	}})
	require.NoError(t, err)
	require.Equal(t, ".google.com", cookies[0]["domain"])
	require.Equal(t, "/", cookies[0]["path"])
}

func TestCloneGeminiWebCredentialsKeepsAPIKeyAndInitializesWeb(t *testing.T) {
	credentials, err := cloneGeminiWebCredentials(map[string]any{"api_key": "keep-me"})
	require.NoError(t, err)
	require.Equal(t, "keep-me", credentials["api_key"])
	require.NotNil(t, credentials["gemini_web"])
}

func TestGeminiWebRuntimeVersionHandlesJSONNumbers(t *testing.T) {
	require.EqualValues(t, 7, geminiWebRuntimeVersion(map[string]any{
		"runtime": map[string]any{"version": float64(7)},
	}))
	require.EqualValues(t, 0, geminiWebRuntimeVersion(nil))
}
