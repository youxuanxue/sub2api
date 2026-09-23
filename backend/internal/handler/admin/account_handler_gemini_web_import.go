package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
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
	if !service.CanImportGeminiWebSession(account) {
		response.BadRequest(c, "Import requires a bound local Gemini Web Worker account; relay accounts cannot hold browser sessions")
		return
	}

	web, _ := account.Credentials["gemini_web"].(map[string]any)
	currentVersion, err := geminiWebRuntimeVersion(web)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	nextVersion := currentVersion + 1
	cookies := normalizeGeminiWebCookies(bundle.Cookies)
	runtime := map[string]any{
		"version":    nextVersion,
		"user_agent": strings.TrimSpace(bundle.UserAgent),
		"cookies":    cookies,
		"state": map[string]any{
			"blocked": false, "generation_pending": false,
			"last_refresh": float64(0), "cooldown_until": float64(0),
		},
	}
	importer, ok := h.adminService.(interface {
		ImportGeminiWebSession(context.Context, int64, int64, map[string]any) (*service.Account, error)
	})
	if !ok {
		response.Error(c, http.StatusInternalServerError, "Gemini Web session import is unavailable")
		return
	}
	updated, err := importer.ImportGeminiWebSession(c.Request.Context(), accountID, currentVersion, runtime)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"account": h.buildAccountResponseWithRuntime(c.Request.Context(), updated),
		"session": gin.H{
			"cookie_count":    len(bundle.Cookies),
			"cookie_domains":  geminiWebCookieDomains(cookies),
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
	for _, cookie := range bundle.Cookies {
		name, nameOK := cookie["name"].(string)
		value, valueOK := cookie["value"].(string)
		domain, domainOK := cookie["domain"].(string)
		if !nameOK || strings.TrimSpace(name) == "" || len([]rune(name)) > geminiWebSessionImportMaxCookieName {
			return errors.New("gemini Web session cookie name is invalid")
		}
		if !valueOK || strings.TrimSpace(value) == "" || len([]byte(value)) > geminiWebSessionImportMaxCookieValue {
			return errors.New("gemini Web session cookie value is invalid")
		}
		if !domainOK {
			return errors.New("gemini Web session cookie domain is invalid")
		}
		domain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(domain), "."))
		if _, ok := geminiWebAllowedCookieDomains[domain]; !ok {
			return errors.New("gemini Web session contains a cookie from an unsupported domain")
		}
		if expires, ok := cookie["expires"]; ok {
			number, numberOK := expires.(float64)
			// CDP uses -1 for session cookies and fractional epoch seconds.
			if !numberOK || math.IsNaN(number) || math.IsInf(number, 0) || number < -1 || number > 253402300799 {
				return errors.New("gemini Web session cookie expiration is invalid")
			}
		}
		if path, ok := cookie["path"]; ok {
			if pathValue, pathOK := path.(string); !pathOK || pathValue == "" {
				return errors.New("gemini Web session cookie path is invalid")
			}
		}
		if secure, ok := cookie["secure"]; ok {
			if _, secureOK := secure.(bool); !secureOK {
				return errors.New("gemini Web session cookie secure flag is invalid")
			}
		}
	}
	return nil
}

func normalizeGeminiWebCookies(cookies []map[string]any) []map[string]any {
	normalized := make([]map[string]any, 0, len(cookies))
	for _, cookie := range cookies {
		copy := make(map[string]any, len(cookie))
		for key, value := range cookie {
			copy[key] = value
		}
		if domain, ok := copy["domain"].(string); ok {
			copy["domain"] = strings.ToLower(strings.TrimSpace(domain))
		}
		if _, ok := copy["path"]; !ok {
			copy["path"] = "/"
		}
		normalized = append(normalized, copy)
	}
	return normalized
}

func geminiWebRuntimeVersion(web map[string]any) (int64, error) {
	runtime, _ := web["runtime"].(map[string]any)
	var version int64 = -1
	switch value := runtime["version"].(type) {
	case float64:
		if value >= 0 && value == float64(int64(value)) {
			version = int64(value)
		}
	case int:
		version = int64(value)
	case int64:
		version = value
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			version = parsed
		}
	}
	if version < 0 || version >= 1<<53-1 {
		return 0, errors.New("invalid Gemini Web runtime version")
	}
	return version, nil
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
