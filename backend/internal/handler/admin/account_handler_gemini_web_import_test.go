package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
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
	cookies := normalizeGeminiWebCookies([]map[string]any{{
		"name": "SID", "value": "secret", "domain": " .GOOGLE.COM ",
	}})
	require.Equal(t, ".google.com", cookies[0]["domain"])
	require.Equal(t, "/", cookies[0]["path"])
}

func TestGeminiWebRuntimeVersionHandlesJSONNumbers(t *testing.T) {
	version, err := geminiWebRuntimeVersion(map[string]any{
		"runtime": map[string]any{"version": float64(7)},
	})
	require.NoError(t, err)
	require.EqualValues(t, 7, version)
	_, err = geminiWebRuntimeVersion(nil)
	require.Error(t, err)
}

func TestGeminiWebCookieScopeMatchesPythonOwner(t *testing.T) {
	owner, err := os.ReadFile("../../../../ops/gemini-web/session_contract.py")
	require.NoError(t, err)
	domains := regexp.MustCompile(`'([a-z0-9.-]+)'`).FindAllStringSubmatch(string(owner), -1)
	actual := make([]string, 0, len(geminiWebAllowedCookieDomains))
	for domain := range geminiWebAllowedCookieDomains {
		actual = append(actual, domain)
	}
	expected := make([]string, 0, len(domains))
	for _, domain := range domains {
		expected = append(expected, domain[1])
		bundle := validGeminiWebImportBundle()
		bundle.Cookies[0]["domain"] = "." + domain[1]
		require.NoError(t, validateGeminiWebSessionImport(bundle))
	}
	require.ElementsMatch(t, expected, actual, "Go is a checked projection of session_contract.py")
}

type geminiImportAdminStub struct {
	service.AdminService
	account         *service.Account
	importErr       error
	calls           int
	expectedVersion int64
	runtime         map[string]any
}

func (s *geminiImportAdminStub) GetAccount(context.Context, int64) (*service.Account, error) {
	return s.account, nil
}
func (s *geminiImportAdminStub) ImportGeminiWebSession(_ context.Context, _ int64, version int64, runtime map[string]any) (*service.Account, error) {
	s.calls++
	s.expectedVersion, s.runtime = version, runtime
	return s.account, s.importErr
}
func geminiImportAccount() *service.Account {
	return &service.Account{ID: 28, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "PRIVATE_KEY", "gemini_web": map[string]any{
			"runtime": map[string]any{"version": float64(7), "cookies": "OLD_SECRET"},
			"lease":   map[string]any{"owner": "worker"},
		}}}
}

func TestGeminiWebImportHandlerInitializesCopiedWorker(t *testing.T) {
	stub := &geminiImportAdminStub{account: &service.Account{ID: 28, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "PRIVATE_KEY", "gemini_web": map[string]any{}}, Schedulable: false}}
	body, err := json.Marshal(validGeminiWebImportBundle())
	require.NoError(t, err)
	w := runGeminiImportHandler(t, stub, string(body))
	require.Equal(t, http.StatusOK, w.Code)
	require.EqualValues(t, -1, stub.expectedVersion)
	require.EqualValues(t, 1, stub.runtime["version"])
	require.False(t, stub.account.Schedulable)
	require.Equal(t, map[string]any{}, stub.account.Credentials["gemini_web"], "handler does not mutate its input")
	require.Contains(t, w.Body.String(), `"mode":"initialized"`)
	require.Contains(t, w.Body.String(), `"runtime_version":1`)
	stub.importErr = infraerrors.New(http.StatusConflict, "GEMINI_WEB_SESSION_CONFLICT", "Session changed")
	w = runGeminiImportHandler(t, stub, string(body))
	require.Equal(t, http.StatusConflict, w.Code)
	require.NotContains(t, w.Body.String(), "cookie_count")
}
func runGeminiImportHandler(t *testing.T, stub *geminiImportAdminStub, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := gin.New()
	h := &AccountHandler{adminService: stub}
	r.POST("/accounts/:id/import", h.ImportGeminiWebSession)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/accounts/28/import", strings.NewReader(body)))
	return w
}
func TestGeminiWebImportHandlerSuccessAndConflict(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "conflict"}[conflict], func(t *testing.T) {
			stub := &geminiImportAdminStub{account: geminiImportAccount()}
			before, err := json.Marshal(stub.account.Credentials)
			require.NoError(t, err)
			if conflict {
				stub.importErr = infraerrors.New(http.StatusConflict, "GEMINI_WEB_SESSION_CONFLICT", "Session changed")
			}
			bundle := validGeminiWebImportBundle()
			bundle.Cookies[0]["domain"] = " .GOOGLE.COM "
			bundle.Cookies[0]["expires"] = float64(-1)
			body, err := json.Marshal(bundle)
			require.NoError(t, err)
			w := runGeminiImportHandler(t, stub, string(body))
			require.Equal(t, 1, stub.calls)
			require.EqualValues(t, 7, stub.expectedVersion)
			after, err := json.Marshal(stub.account.Credentials)
			require.NoError(t, err)
			require.True(t, bytes.Equal(before, after), "handler must not mutate loaded runtime/lease")
			require.NotContains(t, w.Body.String(), "PRIVATE_KEY")
			require.NotContains(t, w.Body.String(), "OLD_SECRET")
			require.NotContains(t, w.Body.String(), "secret")
			if conflict {
				require.Equal(t, http.StatusConflict, w.Code)
				require.NotContains(t, w.Body.String(), "cookie_count")
			} else {
				require.Equal(t, http.StatusOK, w.Code)
				require.Contains(t, w.Body.String(), `"mode":"replaced"`)
				require.Contains(t, w.Body.String(), `"runtime_version":8`)
				require.Contains(t, w.Body.String(), `"cookie_count":2`)
				cookies, ok := stub.runtime["cookies"].([]map[string]any)
				require.True(t, ok)
				require.Equal(t, ".google.com", cookies[0]["domain"])
				require.Equal(t, float64(-1), cookies[0]["expires"])
			}
		})
	}
}
func TestGeminiWebImportHandlerRejectsInvalidInputAndNonWorker(t *testing.T) {
	valid, err := json.Marshal(validGeminiWebImportBundle())
	require.NoError(t, err)
	for _, body := range []string{`{"cookies":PRIVATE_COOKIE`, string(valid) + ` {}`, strings.Repeat(" ", geminiWebSessionImportMaxBody) + string(valid), `null`} {
		stub := &geminiImportAdminStub{account: geminiImportAccount()}
		w := runGeminiImportHandler(t, stub, body)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Zero(t, stub.calls)
		require.NotContains(t, w.Body.String(), "PRIVATE_COOKIE")
	}
	for _, kind := range []string{"plain", "relay", "relay-extra", "oauth", "unbound-enabled", "null-runtime", "bad-version"} {
		t.Run(kind, func(t *testing.T) {
			a := geminiImportAccount()
			switch kind {
			case "plain":
				a.Credentials = nil
			case "relay":
				a.Credentials[service.GeminiWebRelayCredentialKey] = true
			case "relay-extra":
				a.Extra = map[string]any{"relay_kind": "gemini_web"}
			case "oauth":
				a.Type = service.AccountTypeOAuth
			case "unbound-enabled":
				a.Credentials["gemini_web"] = map[string]any{}
				a.Schedulable = true
			case "null-runtime":
				a.Credentials["gemini_web"] = map[string]any{"runtime": nil}
			case "bad-version":
				a.Credentials["gemini_web"] = map[string]any{"runtime": map[string]any{"version": "broken"}}
			}
			stub := &geminiImportAdminStub{account: a}
			w := runGeminiImportHandler(t, stub, string(valid))
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Zero(t, stub.calls)
		})
	}
}
