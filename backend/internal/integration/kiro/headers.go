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

func buildStreamingHeaderValues(account *Account, host string) kiroHeaderValues {
	return buildKiroHeaderValues(account, host, "codewhispererstreaming", tkkiro.StreamingSDKVersion, "m/E")
}

func buildRuntimeHeaderValues(account *Account, host string) kiroHeaderValues {
	return buildKiroHeaderValues(account, host, "codewhispererruntime", tkkiro.RuntimeSDKVersion, "m/N,E")
}

func buildKiroHeaderValues(account *Account, host, apiName, sdkVersion, mode string) kiroHeaderValues {
	identity := tkkiro.ResolveClientIdentity()
	machineID := ""
	if account != nil {
		machineID = account.MachineId
	}

	return kiroHeaderValues{
		UserAgent:    tkkiro.BuildUserAgent(identity, apiName, sdkVersion, mode, machineID),
		AmzUserAgent: tkkiro.BuildAmzUserAgent(identity, sdkVersion, machineID),
		Host:         host,
	}
}

func applyKiroBaseHeaders(req *http.Request, account *Account, values kiroHeaderValues) {
	if account != nil && account.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+account.AccessToken)
	}
	req.Header.Set("User-Agent", values.UserAgent)
	req.Header.Set("x-amz-user-agent", values.AmzUserAgent)
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	if values.Host != "" {
		req.Host = values.Host
	}
}
