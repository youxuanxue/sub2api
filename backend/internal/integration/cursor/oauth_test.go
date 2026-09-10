package cursor

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOAuthPKCE(t *testing.T) {
	login, err := NewOAuthLogin()
	require.NoError(t, err)
	parsed, err := url.Parse(login.URL)
	require.NoError(t, err)
	require.Equal(t, "cursor.com", parsed.Host)
	require.Equal(t, "https", parsed.Scheme)
	require.Equal(t, login.ID, parsed.Query().Get("uuid"))
	require.NotContains(t, login.URL, login.Verifier)
	sum := sha256.Sum256([]byte(login.Verifier))
	require.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), parsed.Query().Get("challenge"))
}

func TestOAuthPollPendingAndSecretSerialization(t *testing.T) {
	login, err := NewOAuthLogin()
	require.NoError(t, err)
	credential, err := PollOAuth(context.Background(), login, func(req *http.Request) (*http.Response, error) {
		require.Equal(t, login.Verifier, req.URL.Query().Get("verifier"))
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	require.NoError(t, err)
	require.Nil(t, credential)
	raw, err := json.Marshal(OAuthCredentials{AccessToken: "sensitive-access", RefreshToken: "sensitive-refresh", ExpiresAt: time.Now()})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "sensitive")
}

func TestOAuthModelsRetainParametersWithoutFallback(t *testing.T) {
	models, err := OAuthModels(context.Background(), "test-token", func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"models":[{"name":"default"},{"name":"grok-4.6","variants":[{"parameterValues":[{"id":"effort","value":"high"},{"id":"fast","value":"false"}],"isDefaultNonMaxConfig":true}]}]}`))}, nil
	})
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, "grok-4.6", models[0].ID)
	require.Equal(t, []Parameter{{ID: "effort", Value: "high"}, {ID: "fast", Value: "false"}}, DefaultParameters(models[0]))
}

func TestAgentVariantUsesExactCatalogWireName(t *testing.T) {
	model := Model{ID: "grok-4.6", Variants: []Variant{{
		Params:     []Parameter{{ID: "effort", Value: "high"}, {ID: "fast", Value: "false"}},
		LegacySlug: "cursor-grok-4.6-high",
	}}}
	wire, err := AgentVariantWireModel(model, []Parameter{{ID: "fast", Value: "false"}, {ID: "effort", Value: "high"}})
	require.NoError(t, err)
	require.Equal(t, "cursor-grok-4.6-high", wire)
	_, err = AgentVariantWireModel(model, []Parameter{{ID: "effort", Value: "low"}})
	require.Error(t, err, "unknown variants cannot be silently substituted")
}
