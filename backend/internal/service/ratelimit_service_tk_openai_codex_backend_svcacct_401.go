package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// TK (prod P0 2026-09-25): ChatGPT Codex backend outage echoed a shared
// service-account key in 401 bodies:
//
//	Incorrect API key provided: sk-svcacct…fvMA
//	url: https://chatgpt.com/backend-api/codex/responses
//
// Evidence (openai/codex#48235/#48237/#48306 + OpenAI status incident
// 01M3DCNWMW57HYK8FJ5FBFPA39): ChatGPT OAuth bearer / chatgpt_account_check /
// oauth/token refresh all stayed healthy while /codex/responses returned 401
// with the SAME sk-svcacct…fvMA suffix across unrelated users and OSes. The
// rejected key is the backend's internal service credential, NOT the edge
// account's access_token or a user-configured OPENAI_API_KEY.
//
// TokenKey previously treated "401 while access_token still solidly valid" as
// grant revocation (oauth_401_valid_token_revoked → SetError). That mass-
// disabled OpenAI OAuth pools during the outage and required manual re-auth
// after upstream recovered. This companion distinguishes the backend echo.
const (
	tkOpenAICodexBackendSvcacct401IncorrectKey = "incorrect api key provided"
	// OpenAI masks the middle of the key in error text ("sk-svcac***…***fvMA");
	// "sk-svcac" is the stable prefix shared by sk-svcacct- and the masked form.
	tkOpenAICodexBackendSvcacct401KeyPrefix = "sk-svcac"
)

// tkIsOpenAICodexBackendSvcacct401 reports whether a 401 is the Codex-backend
// service-account echo rather than a per-account grant revocation.
//
// When true:
//  1. HandleUpstreamError must NOT SetError / temp-unschedulable the OAuth
//     account (otherwise a fleet-wide backend outage mass-disables the pool),
//  2. force-refresh must NOT run (refresh succeeds but /responses still 401 —
//     see openai/codex#48303 refresh storms),
//  3. failover must treat it as SharedFault (rotating accounts cannot help).
func tkIsOpenAICodexBackendSvcacct401(statusCode int, body []byte, upstreamMsg ...string) bool {
	if statusCode != 401 {
		return false
	}
	hay := tkOpenAICodexBackendSvcacct401Haystack(body, upstreamMsg...)
	if hay == "" {
		return false
	}
	return strings.Contains(hay, tkOpenAICodexBackendSvcacct401IncorrectKey) &&
		strings.Contains(hay, tkOpenAICodexBackendSvcacct401KeyPrefix)
}

func tkOpenAICodexBackendSvcacct401Haystack(body []byte, upstreamMsg ...string) string {
	parts := make([]string, 0, 4)
	for _, msg := range upstreamMsg {
		if trimmed := strings.TrimSpace(msg); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if len(body) > 0 {
		if msg := strings.TrimSpace(extractUpstreamErrorMessage(body)); msg != "" {
			parts = append(parts, msg)
		}
		if errMsg := strings.TrimSpace(gjson.GetBytes(body, "error.message").String()); errMsg != "" {
			parts = append(parts, errMsg)
		}
		if detail := strings.TrimSpace(gjson.GetBytes(body, "detail").String()); detail != "" {
			parts = append(parts, detail)
		}
		// Plain-text upstream bodies (no JSON envelope) still carry the echo.
		if len(parts) == 0 {
			parts = append(parts, string(body))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}
