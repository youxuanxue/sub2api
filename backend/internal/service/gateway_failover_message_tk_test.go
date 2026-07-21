//go:build unit

package service

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGatewayFailoverClientMessageDoesNotClaimEveryAccountWasAttempted(t *testing.T) {
	setClaudeStatusForTest(t, ClaudeStatusSnapshot{Status: "operational", FetchedAt: time.Now()})
	message := GatewayFailoverClientMessage(502)

	require.Equal(t, gatewayFailoverClientMessage, message)
	require.NotContains(t, strings.ToLower(message), "all available accounts exhausted")
}

func TestIsGatewayFailoverMessageSupportsRollingVersionCompatibility(t *testing.T) {
	newBody := []byte(`{"error":{"type":"server_error","message":"Upstream request could not be completed"}}`)
	legacyBody := []byte(`{"error":{"type":"server_error","message":"All available accounts exhausted"}}`)

	require.True(t, IsGatewayFailoverMessage("", newBody))
	require.True(t, IsGatewayFailoverMessage("", legacyBody))
	require.True(t, IsGatewayFailoverMessage("upstream request could not be completed", nil))
	require.True(t, IsGatewayFailoverMessage("ALL AVAILABLE ACCOUNTS EXHAUSTED", nil))
}

func TestIsGatewayFailoverMessageAcceptsControlledIncidentSuffix(t *testing.T) {
	setClaudeStatusForTest(t, ClaudeStatusSnapshot{
		IsIncident: true,
		Status:     "partial_outage",
		FetchedAt:  time.Now(),
	})
	message := GatewayFailoverClientMessage(502)

	require.Contains(t, message, claudeStatusPageURL)
	require.True(t, IsGatewayFailoverMessage(message, nil))
}

func TestIsGatewayFailoverMessageRejectsSubstringAndProviderErrors(t *testing.T) {
	require.False(t, IsGatewayFailoverMessage(
		"provider says: Upstream request could not be completed",
		nil,
	))
	require.False(t, IsGatewayFailoverMessage(
		"",
		[]byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`),
	))
	require.False(t, IsGatewayFailoverMessage("", []byte(`<html>502 Bad Gateway</html>`)))
}
