package config

import (
	"net/url"
	"os"
	"strings"
)

// resolveCORSAllowedOriginsTK finalizes cors.allowed_origins for TokenKey
// dual-facade browser probes (Quickstart / Studio).
//
// Precedence:
//  1. Non-empty CORS_ALLOWED_ORIGINS env (comma-separated) — explicit ops override
//  2. Non-empty configured yaml / unmarshal result
//  3. Derive from SERVER_FRONTEND_URL + SITE_DOMAIN + GLOBAL_SITE_DOMAIN
//
// Image-only rolling deploys do not rewrite host config.yaml. Empty
// cors.allowed_origins previously made OPTIONS preflight 403 and browsers
// report "Failed to fetch" / CORS error from apex → api.*. Deriving SPA
// origins when a frontend URL (or stage0 site domains) is already known
// restores the intended allowlist without a host config rewrite.
//
// Dual-facade companion: tokenkey.dev ↔ callmodel.io share one API kernel
// (docs/approved/design-callmodel-product-facade.md). When one apex is
// derived, allow the other so either human face can probe the gateway.
func resolveCORSAllowedOriginsTK(configured []string, frontendURL string) []string {
	// Non-empty CORS_ALLOWED_ORIGINS wins. An empty value (common with Compose
	// `${CORS_ALLOWED_ORIGINS:-}`) must not pin deny — fall through to yaml/derive.
	if env := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS")); env != "" {
		return uniqueOrigins(normalizeStringSlice(strings.Split(env, ",")))
	}
	if normalized := normalizeStringSlice(configured); len(normalized) > 0 {
		return uniqueOrigins(normalized)
	}

	var out []string
	if origin := originFromAbsoluteURL(frontendURL); origin != "" {
		out = append(out, origin)
	}
	for _, key := range []string{"SITE_DOMAIN", "GLOBAL_SITE_DOMAIN"} {
		if origin := httpsOriginFromHostEnv(os.Getenv(key)); origin != "" {
			out = append(out, origin)
		}
	}
	out = appendDualFacadeCompanions(out)
	return uniqueOrigins(out)
}

func originFromAbsoluteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	return scheme + "://" + u.Host
}

func httpsOriginFromHostEnv(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		return originFromAbsoluteURL(raw)
	}
	host := strings.TrimSuffix(raw, "/")
	if host == "" || strings.ContainsAny(host, "/?#") {
		return ""
	}
	// Local bootstrap uses SITE_DOMAIN=localhost; browsers talk http://localhost:5173
	// via the example allowlist, not https://localhost.
	if strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1" {
		return ""
	}
	return "https://" + host
}

func appendDualFacadeCompanions(origins []string) []string {
	needTokenKey := false
	needCallModel := false
	for _, origin := range origins {
		switch strings.ToLower(origin) {
		case "https://tokenkey.dev":
			needCallModel = true
		case "https://callmodel.io":
			needTokenKey = true
		}
	}
	if needCallModel {
		origins = append(origins, "https://callmodel.io")
	}
	if needTokenKey {
		origins = append(origins, "https://tokenkey.dev")
	}
	return origins
}

func uniqueOrigins(values []string) []string {
	if len(values) == 0 {
		return values
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		key := strings.ToLower(v)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, v)
	}
	return out
}
