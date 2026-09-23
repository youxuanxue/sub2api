package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	geminiWebSessionImportFormat         = "tokenkey-gemini-web-session-v1"
	geminiWebSessionImportMaxBody        = 2 << 20
	geminiWebSessionImportMaxCookies     = 500
	geminiWebSessionImportMaxUserAgent   = 1024
	geminiWebSessionImportMaxCookieName  = 256
	geminiWebSessionImportMaxCookieValue = 128 << 10
)

var geminiWebAllowedCookieDomains = map[string]struct{}{
	"google.com": {}, "gemini.google.com": {}, "accounts.google.com": {},
	"lh3.google.com": {}, "lh3.googleusercontent.com": {},
	"work.fife.usercontent.google.com": {},
}

type geminiWebSessionImportRequest struct {
	Format    string           `json:"format"`
	UserAgent string           `json:"user_agent"`
	Cookies   []map[string]any `json:"cookies"`
}

// ImportGeminiWebSession imports a browser-exported session while preserving
// the account's API key and scheduling state. The repository's Gemini Web
// version/lease CAS guard remains the final concurrency check.
func (h *AccountHandler) ImportGeminiWebSession(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, geminiWebSessionImportMaxBody)
	var bundle geminiWebSessionImportRequest
	decoder := json.NewDecoder(c.Request.Body)
	if err := decoder.Decode(&bundle); err != nil {
		response.BadRequest(c, "Invalid Gemini Web session file")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		response.BadRequest(c, "Invalid Gemini Web session file")
		return
	}
	if err := validateGeminiWebSessionImport(bundle); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if account == nil || account.Platform != service.PlatformGemini || account.Type != service.AccountTypeAPIKey {
		response.BadRequest(c, "Gemini Web session import requires a Gemini API key account")
		return
	}

	credentials, err := cloneGeminiWebCredentials(account.Credentials)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to prepare account credentials")
		return
	}
	web, _ := credentials["gemini_web"].(map[string]any)
	currentVersion := geminiWebRuntimeVersion(web)
	nextVersion := currentVersion + 1
	web["runtime"] = map[string]any{
		"version":    nextVersion,
		"user_agent": strings.TrimSpace(bundle.UserAgent),
		"cookies":    bundle.Cookies,
		"state": map[string]any{
			"blocked": false, "generation_pending": false,
			"last_refresh": float64(0), "cooldown_until": float64(0),
		},
	}
	delete(web, "lease")
	credentials["gemini_web"] = web

	updated, err := h.adminService.UpdateAccount(c.Request.Context(), accountID, &service.UpdateAccountInput{
		Credentials: credentials,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"account": h.buildAccountResponseWithRuntime(c.Request.Context(), updated),
		"session": gin.H{
			"cookie_count":    len(bundle.Cookies),
			"cookie_domains":  geminiWebCookieDomains(bundle.Cookies),
			"runtime_version": nextVersion,
		},
	})
}

func validateGeminiWebSessionImport(bundle geminiWebSessionImportRequest) error {
	if bundle.Format != geminiWebSessionImportFormat {
		return errors.New("unsupported Gemini Web session file format")
	}
	ua := strings.TrimSpace(bundle.UserAgent)
	if ua == "" || len([]rune(ua)) > geminiWebSessionImportMaxUserAgent {
		return errors.New("gemini Web session User-Agent is required and must be at most 1024 characters")
	}
	if len(bundle.Cookies) == 0 || len(bundle.Cookies) > geminiWebSessionImportMaxCookies {
		return errors.New("gemini Web session must contain between 1 and 500 cookies")
	}
	for i, cookie := range bundle.Cookies {
		name, nameOK := cookie["name"].(string)
		value, valueOK := cookie["value"].(string)
		domain, domainOK := cookie["domain"].(string)
		if !nameOK || strings.TrimSpace(name) == "" || len([]rune(name)) > geminiWebSessionImportMaxCookieName {
			return errors.New("gemini Web session cookie name is invalid")
		}
		if !valueOK || len([]byte(value)) > geminiWebSessionImportMaxCookieValue {
			return errors.New("gemini Web session cookie value is invalid")
		}
		if !domainOK {
			return errors.New("gemini Web session cookie domain is invalid")
		}
		domain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(domain), "."))
		if _, ok := geminiWebAllowedCookieDomains[domain]; !ok {
			return errors.New("gemini Web session contains a cookie from an unsupported domain")
		}
		if i >= geminiWebSessionImportMaxCookies {
			return errors.New("too many cookies")
		}
	}
	return nil
}

func cloneGeminiWebCredentials(credentials map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(credentials)
	if err != nil {
		return nil, err
	}
	cloned := map[string]any{}
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return nil, err
	}
	web, _ := cloned["gemini_web"].(map[string]any)
	if web == nil {
		web = map[string]any{}
		cloned["gemini_web"] = web
	}
	return cloned, nil
}

func geminiWebRuntimeVersion(web map[string]any) int64 {
	runtime, _ := web["runtime"].(map[string]any)
	if runtime == nil {
		return 0
	}
	switch value := runtime["version"].(type) {
	case float64:
		if value >= 0 && value == float64(int64(value)) {
			return int64(value)
		}
	case int:
		if value >= 0 {
			return int64(value)
		}
	case int64:
		if value >= 0 {
			return value
		}
	case json.Number:
		parsed, _ := value.Int64()
		if parsed >= 0 {
			return parsed
		}
	}
	return 0
}

func geminiWebCookieDomains(cookies []map[string]any) []string {
	seen := make(map[string]struct{}, len(cookies))
	for _, cookie := range cookies {
		if domain, ok := cookie["domain"].(string); ok {
			seen[strings.ToLower(strings.TrimSpace(domain))] = struct{}{}
		}
	}
	domains := make([]string, 0, len(seen))
	for domain := range seen {
		domains = append(domains, domain)
	}
	// Keep the summary deterministic without exposing cookie contents.
	sort.Strings(domains)
	return domains
}
