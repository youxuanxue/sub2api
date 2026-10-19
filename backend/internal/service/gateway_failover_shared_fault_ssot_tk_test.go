//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayCompatSharedFaultCatalog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		message string
		body    string
		want    bool
	}{
		{
			name:    "usage policy phrase",
			message: "Your request was rejected as a result of our safety system / usage policy",
			want:    true,
		},
		{
			name: "cyber policy envelope",
			body: `{"error":{"code":"cyber_policy","message":"Request blocked by content policy."}}`,
			want: true,
		},
		{
			name: "context window",
			body: `{"error":{"message":"input exceeds the context window"}}`,
			want: true,
		},
		{
			name:    "policy marker wrapped as 500-shaped message",
			message: "content access forbidden by policy",
			want:    true,
		},
		{
			name:    "opaque internal error is not shared",
			message: "internal error",
			want:    false,
		},
		{
			name:    "bad_response_body html parse is not shared",
			message: "invalid character '<' looking for beginning of value",
			body:    `{"error":{"code":"bad_response_body","message":"invalid character '<' looking for beginning of value","type":"bad_response_body"}}`,
			want:    false,
		},
		{
			name:    "supplier unavailable phrase is not shared",
			message: "没有可用账号，请稍后重试",
			want:    false,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, gatewayCompatSharedFault(tc.message, []byte(tc.body)))
		})
	}
}

func TestGatewayCompatSharedFaultStopsNewAPI500Matrix(t *testing.T) {
	t.Parallel()
	require.True(t, classifyGatewayFailover(gatewayFailoverObservation{
		Profile: gatewayFailoverProfileNewAPIBridge, StatusCode: http.StatusInternalServerError,
	}).RetryNextAccount, "unclassified NewAPI 500 must failover")
	require.False(t, classifyGatewayFailover(gatewayFailoverObservation{
		Profile:    gatewayFailoverProfileNewAPIBridge,
		Semantic:   gatewayFailureSemanticSharedFault,
		StatusCode: http.StatusInternalServerError,
	}).RetryNextAccount, "SharedFault must suppress NewAPI 500 failover")
}
