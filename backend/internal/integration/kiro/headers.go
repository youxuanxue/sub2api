package kiro

import (
	"net/http"

	tkkiro "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

type kiroHeaderValues struct {
	UserAgent    string
	AmzUserAgent string
	Host         string
}

func buildKiroHeaderValues(host string) kiroHeaderValues {
	return kiroHeaderValues{
		UserAgent:    tkkiro.KiroCLIUserAgent,
		AmzUserAgent: tkkiro.KiroCLIAmzUserAgent,
		Host:         host,
	}
}

func applyKiroBaseHeaders(req *http.Request, account *Account, values kiroHeaderValues) {
	if account != nil && account.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+account.AccessToken)
	}
	req.Header.Set("User-Agent", values.UserAgent)
	req.Header.Set("x-amz-user-agent", values.AmzUserAgent)
	req.Header.Set("x-amzn-codewhisperer-optout", "false")
	if values.Host != "" {
		req.Host = values.Host
	}
}
