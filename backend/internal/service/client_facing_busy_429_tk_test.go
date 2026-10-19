package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTkClientFacingBusy429Message_StableCopy(t *testing.T) {
	require.Equal(t, "The service is temporarily busy. Please try again later.", TkClientFacingBusy429Message)
	require.True(t, TkIsClientFacingBusy429Message(TkClientFacingBusy429Message))
	require.False(t, TkIsClientFacingBusy429Message("No available accounts"))
}

func TestTkMapUpstreamStatusToClient429(t *testing.T) {
	status, errType, msg := TkMapUpstreamStatusToClient429()
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, "rate_limit_error", errType)
	require.Equal(t, TkClientFacingBusy429Message, msg)
}

func TestTkBodyLooksLikeGatewayCapacityBusy429(t *testing.T) {
	t.Run("legacy no-available body", func(t *testing.T) {
		body := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
		require.True(t, TkBodyLooksLikeGatewayCapacityBusy429("", body))
	})
	t.Run("current busy + api_error is capacity", func(t *testing.T) {
		body := []byte(`{"type":"error","error":{"type":"api_error","message":"` + TkClientFacingBusy429Message + `"}}`)
		require.True(t, TkBodyLooksLikeGatewayCapacityBusy429("", body))
	})
	t.Run("current busy + rate_limit_error is NOT capacity", func(t *testing.T) {
		body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"` + TkClientFacingBusy429Message + `"}}`)
		require.False(t, TkBodyLooksLikeGatewayCapacityBusy429("", body))
	})
	t.Run("current busy + rate_limit_error + no_available_accounts code IS capacity", func(t *testing.T) {
		body := []byte(`{"error":{"type":"rate_limit_error","code":"no_available_accounts","message":"` + TkClientFacingBusy429Message + `"}}`)
		require.True(t, TkBodyLooksLikeGatewayCapacityBusy429("", body))
	})
	t.Run("legacy upstream-limit rewrite is NOT capacity", func(t *testing.T) {
		body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Upstream rate limit exceeded, please retry later"}}`)
		require.False(t, TkBodyLooksLikeGatewayCapacityBusy429("", body))
	})
	t.Run("genuine anthropic rpm is NOT capacity", func(t *testing.T) {
		body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`)
		require.False(t, TkBodyLooksLikeGatewayCapacityBusy429("", body))
	})
}

func TestTkLooksLikeLegacyCapacityAndUpstreamLimit(t *testing.T) {
	require.True(t, TkLooksLikeLegacyCapacityClientMessage("No available accounts for this request. Please retry."))
	require.True(t, TkLooksLikeLegacyCapacityClientMessage("All available accounts are currently rate-limited. Please retry later."))
	require.True(t, TkLooksLikeLegacyUpstreamLimitClientMessage("Upstream rate limit exceeded, please retry later"))
	require.True(t, TkLooksLikeLegacyUpstreamLimitClientMessage("Kiro upstream quota exhausted, please retry later"))
	require.False(t, TkLooksLikeLegacyCapacityClientMessage(TkClientFacingBusy429Message))
	require.False(t, TkLooksLikeLegacyUpstreamLimitClientMessage(TkClientFacingBusy429Message))
}

func TestTkSanitizeBusy429WireMessage(t *testing.T) {
	public, sanitized := TkSanitizeBusy429WireMessage(http.StatusTooManyRequests, "No available accounts: no available accounts")
	require.True(t, sanitized)
	require.Equal(t, TkClientFacingBusy429Message, public)

	public, sanitized = TkSanitizeBusy429WireMessage(http.StatusTooManyRequests, "Upstream rate limit exceeded, please retry later")
	require.True(t, sanitized)
	require.Equal(t, TkClientFacingBusy429Message, public)

	public, sanitized = TkSanitizeBusy429WireMessage(http.StatusTooManyRequests, TkClientFacingBusy429Message)
	require.False(t, sanitized)
	require.Equal(t, TkClientFacingBusy429Message, public)

	public, sanitized = TkSanitizeBusy429WireMessage(http.StatusTooManyRequests, "Too many pending requests, please retry later")
	require.False(t, sanitized)
	require.Equal(t, "Too many pending requests, please retry later", public)

	public, sanitized = TkSanitizeBusy429WireMessage(http.StatusBadGateway, "No available accounts")
	require.False(t, sanitized)
	require.Equal(t, "No available accounts", public)
}
