//go:build unit

package antigravity

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFetchAvailableModelsAtBindsServingEndpoint(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusTemporaryRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			client := &Client{httpClient: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, DailyBaseURL()+"/v1internal:fetchAvailableModels", req.URL.String())
				require.Equal(t, "Bearer token", req.Header.Get("Authorization"))
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{ProdBaseURL() + "/v1internal:fetchAvailableModels"}}, Body: io.NopCloser(strings.NewReader(`{"models":{"gemini-4-argon-disabled":{}}}`)), Request: req}, nil
			})}}
			result, _, err := client.FetchAvailableModelsAt(context.Background(), "token", "project", defaultFetchAvailableModelsBodyLimit, DailyBaseURL())
			require.Equal(t, 1, calls, "must neither fallback nor redirect to prod")
			if status == http.StatusOK {
				require.NoError(t, err)
				require.Contains(t, result.Models, "gemini-4-argon-disabled")
				require.NotContains(t, result.Models, "gemini-4-argon-high")
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestFetchAvailableModelsAtRejectsForeignEndpointBeforeAuth(t *testing.T) {
	calls := 0
	client := &Client{httpClient: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) { calls++; return nil, nil })}}
	_, _, err := client.FetchAvailableModelsAt(context.Background(), "token", "project", defaultFetchAvailableModelsBodyLimit, "https://untrusted.example")
	require.Error(t, err)
	require.Zero(t, calls)
}
