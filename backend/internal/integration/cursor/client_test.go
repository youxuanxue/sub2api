package cursor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAuthorizationSharedStoreAndSingleUse(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer func() { _ = rdb.Close() }()
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("{\"exp\":%d}", time.Now().Add(time.Hour).Unix()))) + ".signature"
	pending := true
	do := func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "api2.cursor.sh", req.URL.Host)
		response := `{"models":[{"name":"composer-2.5","variants":[{"parameterValues":[],"isDefaultNonMaxConfig":true,"legacySlug":"composer-2.5"}]}]}`
		if req.URL.Path == "/auth/poll" {
			if pending {
				return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			response = fmt.Sprintf(`{"accessToken":%q,"refreshToken":"private-refresh"}`, token)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response))}, nil
	}
	first, second := NewClient(rdb, do), NewClient(rdb, do)
	session, err := first.Start(ctx, "admin:1")
	require.NoError(t, err)
	_, err = second.Status(ctx, "admin:2", session.ID)
	require.Error(t, err)
	status, err := second.Status(ctx, "admin:1", session.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", status.State)
	pending = false
	status, err = second.Status(ctx, "admin:1", session.ID)
	require.NoError(t, err)
	require.Equal(t, "authorized", status.State)
	raw, err := json.Marshal(status)
	require.NoError(t, err)
	require.NotContains(t, string(raw), token)
	require.NotContains(t, string(raw), "private-refresh")
	claim, err := first.Claim(ctx, "admin:1", session.ID)
	require.NoError(t, err)
	require.Equal(t, token, claim.APIKey)
	_, err = second.Claim(ctx, "admin:1", session.ID)
	require.Error(t, err)
	require.Error(t, second.Cancel(ctx, "admin:1", session.ID))
	require.Error(t, second.Settle(ctx, "admin:1", session.ID, "wrong", false))
	require.NoError(t, first.Settle(ctx, "admin:1", session.ID, claim.Claim, false))
	claim, err = second.Claim(ctx, "admin:1", session.ID)
	require.NoError(t, err)
	require.NoError(t, second.Settle(ctx, "admin:1", session.ID, claim.Claim, true))
	_, err = first.Claim(ctx, "admin:1", session.ID)
	require.Error(t, err)
}
func TestAuthorizationCancellationCannotBeResurrectedAndTTLDoesNotSlide(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer func() { _ = rdb.Close() }()
	client := NewClient(rdb, http.DefaultClient.Do)
	auth, err := client.Start(ctx, "admin:1")
	require.NoError(t, err)
	session, old, err := client.load(ctx, "admin:1", auth.ID)
	require.NoError(t, err)
	server.FastForward(time.Minute)
	require.NoError(t, client.replace(ctx, auth.ID, old, &session))
	key, err := authKey(auth.ID)
	require.NoError(t, err)
	require.Equal(t, authorizationTTL-time.Minute, server.TTL(key))
	require.NoError(t, client.Cancel(ctx, "admin:1", auth.ID))
	require.Error(t, client.replace(ctx, auth.ID, old, &session))
	require.False(t, server.Exists(key))
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
