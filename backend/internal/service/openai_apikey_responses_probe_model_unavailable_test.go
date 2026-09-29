package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// A 400/404 that only says the probe model does not exist says nothing about
// the /v1/responses endpoint: the verdict must stay inconclusive and the
// account capability must not be overwritten with "unsupported".
func TestResponsesProbeModelUnavailableIsInconclusive(t *testing.T) {
	for _, body := range []string{
		`{"error":{"type":"model_not_found","message":"Model codex-auto-review is not supported by any configured account in this group"}}`,
		`{"error":{"code":"model_not_available"}}`,
		`{"error":{"message":"The model missing does not exist"}}`,
	} {
		require.False(t, responsesProbeVerdictIsConclusive(404, []byte(body)))
		require.True(t, decideResponsesProbeSupport(false, 404, []byte(body)))
		account := &Account{ID: 96, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "https://upstream.example"}}
		upstream := &httpUpstreamRecorder{responses: []*http.Response{protocolProbeHTTPResponse(404, body)}}
		svc := protocolRequestBuilderTestService(upstream)
		observation, observed := svc.probeOpenAIAPIKeyResponsesSupport(context.Background(), account)
		require.True(t, observed)
		require.Equal(t, ProtocolProbeModelSpecific, observation.verdict)
		require.Len(t, upstream.bodies, 1)

	}
	// A plain 404/405 still means the endpoint does not exist.
	require.True(t, responsesProbeVerdictIsConclusive(404, []byte("404 page not found")))
	require.False(t, decideResponsesProbeSupport(false, 404, []byte("404 page not found")))
	require.False(t, decideResponsesProbeSupport(false, 405, nil))
}

func TestSelectResponsesProbeModelPrefersGeneralTextModel(t *testing.T) {
	account := &Account{Credentials: map[string]any{"model_mapping": map[string]any{
		"codex-auto-review": "codex-auto-review", "gpt-image-2": "gpt-image-2", "gpt-5.5": "gpt-5.5",
	}}}
	require.Equal(t, "gpt-5.5", selectProtocolProbeModel(account))
}
