package cursor

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOAuthRefreshLiveChainAndInference(t *testing.T) {
	path := os.Getenv("TOKENKEY_CURSOR_REFRESH_EVIDENCE_FILE")
	if path == "" {
		t.Skip("opt-in real Cursor renewal verification")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var input struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.Unmarshal(raw, &input))
	require.NotEmpty(t, input.AccessToken)
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	token := input.AccessToken
	for round := 1; round <= 2; round++ {
		result, err := RefreshOAuth(ctx, token, client.Do)
		require.NoError(t, err)
		token = result.RefreshToken
		t.Logf("refresh round=%d expires_at=%s", round, result.ExpiresAt.Format(time.RFC3339))
	}
	models, err := OAuthModels(ctx, token, client.Do)
	require.NoError(t, err)
	var model Model
	for _, candidate := range models {
		if candidate.ID == "composer-2.5" {
			model = candidate
			break
		}
	}
	require.NotEmpty(t, model.ID)
	params := DefaultParameters(model)
	wire, err := AgentVariantWireModel(model, params)
	require.NoError(t, err)
	result, err := RunAgent(ctx, token, AgentRequest{Model: wire, Parameters: params, Messages: []AgentMessage{{Role: "user", Text: "Reply exactly CURSOR_REFRESH_OK."}}}, client.Do, nil)
	require.NoError(t, err)
	require.Contains(t, strings.TrimSpace(result.Text), "CURSOR_REFRESH_OK")
	t.Logf("renewed token native inference passed; catalog_fixed_models=%d", len(models))
}
