package kiro

import (
	"net/url"
	"strings"
)

// regionFromKiroProfileArn returns the data-plane region encoded in a valid
// CodeWhisperer profile ARN. Account.Region is deliberately not used for OAuth
// data-plane routing: it is the OIDC authentication region.
func regionFromKiroProfileArn(profileArn string) string {
	parts := strings.SplitN(strings.TrimSpace(profileArn), ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[1] != "aws" ||
		parts[2] != "codewhisperer" || !kiroRegionPattern.MatchString(parts[3]) ||
		!strings.HasPrefix(parts[5], "profile/") || strings.TrimPrefix(parts[5], "profile/") == "" {
		return ""
	}
	return parts[3]
}

func isKiroAPIKeyAccount(account *Account) bool {
	if account == nil {
		return false
	}
	method := strings.ToLower(strings.TrimSpace(account.AuthMethod))
	return method == "api_key" || method == "apikey"
}

func kiroDataRegion(account *Account, explicitProfileArn string) string {
	if region := regionFromKiroProfileArn(explicitProfileArn); region != "" {
		return region
	}
	if account != nil {
		if region := regionFromKiroProfileArn(account.ProfileArn); region != "" {
			return region
		}
		// API-key credentials use Region for the CLI data plane. OAuth Region
		// remains OIDC-only until a profile ARN identifies the data plane.
		if isKiroAPIKeyAccount(account) && kiroRegionPattern.MatchString(account.Region) {
			return strings.TrimSpace(strings.ToLower(account.Region))
		}
	}
	return "us-east-1"
}

// regionalizeKiroURL maps the us-east-1 placeholders used by the endpoint
// catalog to the profile's regional Amazon Q data-plane host. The Kiro runtime
// host is currently only declared for us-east-1; non-US profiles use q.<region>.
func regionalizeKiroURL(rawURL string, account *Account, explicitProfileArn string) string {
	region := strings.TrimSpace(strings.ToLower(kiroDataRegion(account, explicitProfileArn)))
	if region == "" || region == "us-east-1" || !kiroRegionPattern.MatchString(region) {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return rawURL
	}
	switch parsed.Host {
	case "runtime.us-east-1.kiro.dev", "q.us-east-1.amazonaws.com", "codewhisperer.us-east-1.amazonaws.com":
		parsed.Host = "q." + region + ".amazonaws.com"
		return parsed.String()
	default:
		return rawURL
	}
}
