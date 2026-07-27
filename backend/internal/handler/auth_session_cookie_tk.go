package handler

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	tkRefreshSessionCookieName = "tk_refresh"
	tkRefreshSessionCookiePath = "/api/v1/auth"
)

func tkRefreshSessionCookieMaxAgeSec(h *AuthHandler) int {
	if h == nil || h.cfg == nil {
		return 7 * 24 * 60 * 60
	}
	days := h.cfg.JWT.RefreshTokenExpireDays
	if days <= 0 {
		days = 7
	}
	return days * 24 * 60 * 60
}

func registrableParentDomain(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || host == "localhost" || net.ParseIP(host) != nil {
		return ""
	}

	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return ""
	}
	if len(parts) == 2 {
		return host
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

func hostFromAbsoluteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(u.Hostname()))
}

func requestHost(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(c.Request.Host))
	if idx := strings.Index(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	return host
}

func (h *AuthHandler) tkRefreshSessionCookieDomain(c *gin.Context) string {
	host := hostFromAbsoluteURL(h.tkFrontendBaseURL())
	if host == "" && c != nil {
		host = requestHost(c)
	}
	return registrableParentDomain(host)
}

func (h *AuthHandler) tkFrontendBaseURL() string {
	if h == nil || h.cfg == nil {
		return ""
	}
	return strings.TrimSpace(h.cfg.Server.FrontendURL)
}

func (h *AuthHandler) setTkRefreshSessionCookie(c *gin.Context, refreshToken string) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" || c == nil {
		return
	}

	secure := isRequestHTTPS(c)
	cookie := &http.Cookie{
		Name:     tkRefreshSessionCookieName,
		Value:    encodeCookieValue(refreshToken),
		Path:     tkRefreshSessionCookiePath,
		MaxAge:   tkRefreshSessionCookieMaxAgeSec(h),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	if domain := h.tkRefreshSessionCookieDomain(c); domain != "" {
		cookie.Domain = domain
	}
	http.SetCookie(c.Writer, cookie)
}

func clearTkRefreshSessionCookie(c *gin.Context, domain string) {
	if c == nil {
		return
	}

	secure := isRequestHTTPS(c)
	cookie := &http.Cookie{
		Name:     tkRefreshSessionCookieName,
		Value:    "",
		Path:     tkRefreshSessionCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	domain = strings.TrimSpace(domain)
	if domain != "" {
		cookie.Domain = domain
	}
	http.SetCookie(c.Writer, cookie)
}

func (h *AuthHandler) clearTkRefreshSessionCookie(c *gin.Context) {
	clearTkRefreshSessionCookie(c, h.tkRefreshSessionCookieDomain(c))
}

func readTkRefreshSessionCookie(c *gin.Context) (string, error) {
	return readCookieDecoded(c, tkRefreshSessionCookieName)
}

func (h *AuthHandler) resolveRefreshTokenForRequest(c *gin.Context, bodyToken string) (string, error) {
	bodyToken = strings.TrimSpace(bodyToken)
	if bodyToken != "" {
		return bodyToken, nil
	}
	return readTkRefreshSessionCookie(c)
}
