//go:build unit

package service

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUS049_GlobalGatewayFailoverPolicyMatrix(t *testing.T) {
	tests := []struct {
		name    string
		profile gatewayFailoverProfile
		status  int
		want    bool
	}{
		{"generic auth", gatewayFailoverProfileGeneric, 401, true},
		{"generic payment", gatewayFailoverProfileGeneric, 402, true},
		{"generic forbidden", gatewayFailoverProfileGeneric, 403, true},
		{"generic dependency", gatewayFailoverProfileGeneric, 424, true},
		{"generic rate limit", gatewayFailoverProfileGeneric, 429, true},
		{"generic overloaded", gatewayFailoverProfileGeneric, 529, true},
		{"generic server", gatewayFailoverProfileGeneric, 500, true},
		{"generic client", gatewayFailoverProfileGeneric, 400, false},
		{"openai method account mismatch", gatewayFailoverProfileOpenAI, 405, true},
		{"grok preserves openai method behavior", gatewayFailoverProfileGrok, 405, true},
		{"google auth", gatewayFailoverProfileGoogle, 401, true},
		{"google forbidden", gatewayFailoverProfileGoogle, 403, true},
		{"google rate limit", gatewayFailoverProfileGoogle, 429, true},
		{"google overloaded", gatewayFailoverProfileGoogle, 529, true},
		{"google server", gatewayFailoverProfileGoogle, 500, true},
		{"google payment remains terminal", gatewayFailoverProfileGoogle, 402, false},
		{"newapi auth", gatewayFailoverProfileNewAPIBridge, 401, true},
		{"newapi payment", gatewayFailoverProfileNewAPIBridge, 402, true},
		{"newapi rate limit", gatewayFailoverProfileNewAPIBridge, 429, true},
		{"newapi bad gateway", gatewayFailoverProfileNewAPIBridge, 502, true},
		{"newapi unavailable", gatewayFailoverProfileNewAPIBridge, 503, true},
		{"newapi gateway timeout", gatewayFailoverProfileNewAPIBridge, 504, true},
		{"newapi generic server remains terminal", gatewayFailoverProfileNewAPIBridge, 500, false},
		{"newapi forbidden without standing signal", gatewayFailoverProfileNewAPIBridge, 403, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyGatewayFailover(gatewayFailoverObservation{
				Profile:    tc.profile,
				StatusCode: tc.status,
			}).RetryNextAccount
			require.Equal(t, tc.want, got)
		})
	}
}

func TestUS049_SemanticOverridesStatusAndFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		obs  gatewayFailoverObservation
		want bool
	}{
		{
			name: "shared fault suppresses failover status",
			obs: gatewayFailoverObservation{
				Profile: gatewayFailoverProfileOpenAI, Semantic: gatewayFailureSemanticSharedFault, StatusCode: 503,
			},
			want: false,
		},
		{
			name: "account signal enables terminal status",
			obs: gatewayFailoverObservation{
				Profile: gatewayFailoverProfileNewAPIBridge, Semantic: gatewayFailureSemanticAccountFault, StatusCode: 400,
			},
			want: true,
		},
		{
			name: "transient signal enables terminal status",
			obs: gatewayFailoverObservation{
				Profile: gatewayFailoverProfileOpenAI, Semantic: gatewayFailureSemanticTransientFault, StatusCode: 400,
			},
			want: true,
		},
		{
			name: "unknown profile fails closed even with transient fact",
			obs: gatewayFailoverObservation{
				Profile: gatewayFailoverProfileUnknown, Semantic: gatewayFailureSemanticTransientFault, StatusCode: 503,
			},
			want: false,
		},
		{
			name: "unknown semantic fails closed",
			obs: gatewayFailoverObservation{
				Profile: gatewayFailoverProfileGeneric, Semantic: gatewayFailureSemantic(255), StatusCode: 503,
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, classifyGatewayFailover(tc.obs).RetryNextAccount)
		})
	}
}

func TestUS049_RuntimeFailoverChokePointUsesGlobalPolicy(t *testing.T) {
	require.False(t, (*UpstreamFailoverError)(nil).ShouldRetryNextAccount())
	require.True(t, (&UpstreamFailoverError{}).ShouldRetryNextAccount(), "legacy zero value must keep retrying")
	require.True(t, (&UpstreamFailoverError{NextAccountAction: NextAccountRetry}).ShouldRetryNextAccount())
	require.False(t, (&UpstreamFailoverError{NextAccountAction: NextAccountStop}).ShouldRetryNextAccount())
	require.False(t, (&UpstreamFailoverError{NextAccountAction: NextAccountAction(255)}).ShouldRetryNextAccount())

	retryErr := applyGatewayFailoverSemantic(
		&UpstreamFailoverError{StatusCode: http.StatusBadRequest},
		gatewayFailoverProfileGrok,
		gatewayFailureSemanticAccountFault,
	)
	require.Equal(t, NextAccountRetry, retryErr.NextAccountAction)
	require.True(t, retryErr.ShouldRetryNextAccount())

	stopErr := applyGatewayFailoverSemantic(
		&UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable},
		gatewayFailoverProfileOpenAI,
		gatewayFailureSemanticSharedFault,
	)
	require.Equal(t, NextAccountStop, stopErr.NextAccountAction)
	require.False(t, stopErr.ShouldRetryNextAccount())

	unknownErr := applyGatewayFailoverSemantic(
		&UpstreamFailoverError{},
		gatewayFailoverProfileUnknown,
		gatewayFailureSemanticAccountFault,
	)
	require.Equal(t, NextAccountStop, unknownErr.NextAccountAction)
	require.False(t, unknownErr.ShouldRetryNextAccount())
}

func TestUS049_OpenAIPassthroughAccountMatrix(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		accountType string
		configured  bool
		want        bool
	}{
		{"oauth rate limit", 429, AccountTypeOAuth, false, true},
		{"oauth overloaded", 529, AccountTypeOAuth, false, true},
		{"oauth generic server terminal", 500, AccountTypeOAuth, false, false},
		{"apikey internal server", 500, AccountTypeAPIKey, false, true},
		{"apikey cloudflare edge", 522, AccountTypeAPIKey, false, true},
		{"apikey unlisted server", 501, AccountTypeAPIKey, false, false},
		{"configured pool status", http.StatusTeapot, AccountTypeOAuth, true, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyGatewayFailover(gatewayFailoverObservation{
				Profile:                gatewayFailoverProfileOpenAIPassthrough,
				StatusCode:             tc.status,
				AccountType:            tc.accountType,
				AccountConfiguredRetry: tc.configured,
			}).RetryNextAccount
			require.Equal(t, tc.want, got)
		})
	}
}

func TestUS049_AdaptersDelegateWithoutBehaviorDrift(t *testing.T) {
	require.True(t, (&GatewayService{}).shouldFailoverUpstreamError(http.StatusFailedDependency))
	require.True(t, (&OpenAIGatewayService{}).shouldFailoverUpstreamError(http.StatusMethodNotAllowed))
	require.True(t, (&GeminiMessagesCompatService{}).shouldFailoverGeminiUpstreamError(http.StatusForbidden))
	require.True(t, (&AntigravityGatewayService{}).shouldFailoverUpstreamError(http.StatusForbidden))

	contextWindow := []byte(`{"error":{"message":"input exceeds the context window"}}`)
	require.False(t, (&OpenAIGatewayService{}).shouldFailoverOpenAIUpstreamResponse(502, "", contextWindow))
	require.False(t, openAIStreamErrorEventShouldFailover([]byte(`{"type":"error","error":{"message":"unknown client error"}}`), "unknown client error"))
	require.True(t, openAIStreamFailedEventShouldFailover([]byte(`{"type":"response.failed"}`), ""))

	contentPolicy := []byte(`{"error":{"code":"new_sensitive","message":"text is sensitive"}}`)
	require.False(t, (&OpenAIGatewayService{}).shouldFailoverGrokUpstreamError(http.StatusForbidden, contentPolicy))
	freeUsage := []byte(`{"error":{"code":"subscription:free-usage-exhausted"}}`)
	require.True(t, (&OpenAIGatewayService{}).shouldFailoverGrokUpstreamError(http.StatusBadRequest, freeUsage))
	bodyOnlyRateLimit := []byte(`{"error":{"message":"rate limit exceeded"}}`)
	require.False(t, (&OpenAIGatewayService{}).shouldFailoverGrokUpstreamError(http.StatusBadRequest, bodyOnlyRateLimit))

	require.Equal(t, gatewayFailureSemanticAccountFault,
		gateway400FailureSemantic([]byte(`{"error":{"message":"anthropic-beta header requires beta"}}`)))
	require.Equal(t, gatewayFailureSemanticSharedFault,
		gateway400FailureSemantic([]byte(`{"error":{"message":"invalid request"}}`)))
	googleSemantic := googleGatewayFailureSemantic(http.StatusBadRequest, "invalid project resource name")
	require.Equal(t, gatewayFailureSemanticAccountFault, googleSemantic)
	require.True(t, classifyGatewayFailover(gatewayFailoverObservation{
		Profile: gatewayFailoverProfileGoogle, Semantic: googleSemantic, StatusCode: http.StatusBadRequest,
	}).RetryNextAccount)

	require.True(t, (&OpenAIGatewayService{}).shouldFailoverLiveCreateError(errors.New("transport failed")))
}
