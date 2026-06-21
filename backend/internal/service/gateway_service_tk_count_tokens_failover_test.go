//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// The count_tokens failover-error builder must use the SAME anthropic-non-
// authoritative-429 carve-out as the main /v1/messages path, so a header-less
// dead-edge capacity 429 on a pool_mode stub does NOT trigger in-place same-account
// retries (which held the dead stub's slot). An authoritative window-limit 429
// (header-bearing) still keeps same-account retry.
func TestCountTokensFailoverError_CarveOutWired(t *testing.T) {
	gs := &GatewayService{}
	poolStub := &Account{
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"pool_mode": true},
	}
	body := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)

	resp := func(status int, h http.Header) *http.Response {
		if h == nil {
			h = http.Header{}
		}
		return &http.Response{StatusCode: status, Header: h}
	}

	t.Run("header-less 429 => RetryableOnSameAccount=false (switch immediately)", func(t *testing.T) {
		fe := gs.tkCountTokensFailoverError(poolStub, resp(http.StatusTooManyRequests, nil), body)
		require.NotNil(t, fe, "429 is failover-eligible")
		require.False(t, fe.RetryableOnSameAccount, "header-less dead-edge 429 must not burn same-account retries on count_tokens")
	})

	t.Run("authoritative 429 (header-bearing) => RetryableOnSameAccount=true", func(t *testing.T) {
		h := http.Header{"Anthropic-Ratelimit-Unified-5h-Reset": {"9999999999"}}
		fe := gs.tkCountTokensFailoverError(poolStub, resp(http.StatusTooManyRequests, h), body)
		require.NotNil(t, fe)
		require.True(t, fe.RetryableOnSameAccount, "a real relayed window-limit 429 still keeps in-pool same-account retry")
	})

	t.Run("non-failover status => nil", func(t *testing.T) {
		require.Nil(t, gs.tkCountTokensFailoverError(poolStub, resp(http.StatusOK, nil), body))
	})
}
