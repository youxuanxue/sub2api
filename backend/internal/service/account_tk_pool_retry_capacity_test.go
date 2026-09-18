//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTkIsAccountCapacityFailure(t *testing.T) {
	noAvail := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
	overloaded := []byte(`{"error":{"type":"server_error","message":"The server is overloaded"}}`)
	generic503 := []byte(`{"type":"error","error":{"type":"api_error","message":"upstream hiccup"}}`)

	anthropic := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	openai := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	antigravity := &Account{
		Platform: PlatformAntigravity,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "k",
			"base_url": "https://api-us1.tokenkey.dev",
		},
	}

	require.True(t, tkIsAccountCapacityFailure(anthropic, http.StatusServiceUnavailable, "", noAvail))
	require.True(t, tkIsAccountCapacityFailure(anthropic, http.StatusTooManyRequests, "", noAvail))
	require.False(t, tkIsAccountCapacityFailure(anthropic, http.StatusServiceUnavailable, "", generic503))

	require.True(t, tkIsAccountCapacityFailure(openai, http.StatusServiceUnavailable, "", overloaded))
	require.False(t, tkIsAccountCapacityFailure(openai, http.StatusBadGateway, "", overloaded))

	require.True(t, tkIsAccountCapacityFailure(antigravity, http.StatusServiceUnavailable, "", noAvail))
	require.False(t, tkIsAccountCapacityFailure(antigravity, http.StatusServiceUnavailable, "", generic503))

	require.False(t, tkIsAccountCapacityFailure(nil, http.StatusServiceUnavailable, "", noAvail))
}

func TestTkRetryableOnSameAccount_AccountCapacitySwitches(t *testing.T) {
	poolAnthropic := &Account{
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"pool_mode": true},
	}
	poolOpenAI := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"pool_mode": true, "api_key": "k", "base_url": "https://api-us1.tokenkey.dev"},
	}
	noAvail := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
	overloaded := []byte(`{"error":{"type":"server_error","message":"The server is overloaded"}}`)

	resp := func(status int, body []byte) *http.Response {
		return &http.Response{StatusCode: status, Header: http.Header{}}
	}

	require.False(t, tkRetryableOnSameAccount(poolAnthropic, resp(http.StatusServiceUnavailable, noAvail), noAvail))
	require.False(t, tkRetryableOnSameAccount(poolAnthropic, resp(http.StatusTooManyRequests, noAvail), noAvail))
	require.False(t, tkRetryableOnSameAccount(poolOpenAI, resp(http.StatusServiceUnavailable, overloaded), overloaded))
}
