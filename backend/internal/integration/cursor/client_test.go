package cursor

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientAuthorizationOwnerAndErrors(t *testing.T) {
	secret := strings.Repeat("s", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, secret, r.Header.Get(SecretHeader))
		require.Equal(t, "admin:7", r.Header.Get(TenantHeader))
		if r.Method == http.MethodGet {
			w.WriteHeader(403)
			_, _ = w.Write([]byte("secret-user-key"))
			return
		}
		_ = json.NewEncoder(w).Encode(Authorization{ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", State: "pending"})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, secret)
	require.NoError(t, err)
	auth, err := client.Start(context.Background(), "admin:7")
	require.NoError(t, err)
	_, err = client.Status(context.Background(), "admin:7", auth.ID)
	require.ErrorContains(t, err, "HTTP 403")
	require.NotContains(t, err.Error(), "secret-user-key")
	_, err = client.Status(context.Background(), "admin:7", "../../escape")
	require.Error(t, err)
}

func TestClientRejectsRedirectAndUnsafeConfig(t *testing.T) {
	for _, url := range []string{"", "file:///tmp/socket", "https://user:pass@host", "https://host?key=x"} {
		_, err := NewClient(url, strings.Repeat("s", 32))
		require.Error(t, err)
	}
	_, err := NewClient("http://127.0.0.1", "short")
	require.Error(t, err)
	redirected := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer dest.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL, http.StatusTemporaryRedirect)
	}))
	defer src.Close()
	client, err := NewClient(src.URL, strings.Repeat("s", 32))
	require.NoError(t, err)
	_, err = client.Start(context.Background(), "admin:1")
	require.ErrorContains(t, err, "307")
	require.False(t, redirected)
}

func TestDefaultParametersDisablesFastOnlyForExistingVariant(t *testing.T) {
	fast := []Parameter{{ID: "effort", Value: "high"}, {ID: "fast", Value: "true"}}
	regular := []Parameter{{ID: "fast", Value: "false"}, {ID: "effort", Value: "high"}}
	model := Model{Variants: []Variant{{Params: fast, IsDefault: true}, {Params: regular}}}
	require.True(t, sameParameters(regular, DefaultParameters(model)))
	require.Equal(t, "true", fast[1].Value)
	model.Variants = model.Variants[:1]
	require.Equal(t, fast, DefaultParameters(model))
}
