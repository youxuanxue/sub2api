//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestEdgeAdminHandoffForwardPinnedWithoutMirrorKeyOrRedirect(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		t.Run(map[bool]string{false: "code only", true: "redirect blocked"}[redirect], func(t *testing.T) {
			owner, _ := handoffTestOwner(t)
			signer := owner.cfg.Signers["e1"]
			signer.Origin = "https://api-e1.tokenkey.dev"
			owner.cfg.Signers["e1"] = signer
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				require.Equal(t, "api-e1.tokenkey.dev", r.Host)
				require.Empty(t, r.Header.Get("x-api-key"))
				require.Empty(t, r.Header.Get("Authorization"))
				require.Equal(t, "/api/v1/edge/admin-handoff/mint", r.URL.Path)
				if redirect {
					w.Header().Set("Location", "https://api-e1.tokenkey.dev/redirected")
					w.WriteHeader(307)
					return
				}
				var envelope EdgeHandoffDelegation
				require.NoError(t, json.NewDecoder(r.Body).Decode(&envelope))
				raw, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
				require.NoError(t, err)
				var claims EdgeHandoffClaims
				require.NoError(t, json.Unmarshal(raw, &claims))
				require.Equal(t, int64(17), claims.Initiator)
				require.Equal(t, signer.Origin, claims.Audience)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": EdgeHandoffCode{Code: strings.Repeat("A", 43), Attempt: claims.Attempt}}))
			}))
			defer server.Close()
			stub := mirrorStub(1, signer.Origin, "must-not-forward")
			stub.Platform = PlatformOpenAI
			agg := NewEdgeAccountsAggregator(&platformAwareEdgeStore{byPlatform: map[string][]Account{PlatformOpenAI: {stub}}}, nil)
			agg.handoff = owner
			transport := server.Client().Transport.(*http.Transport).Clone()
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
			transport.TLSClientConfig.ServerName = "example.com" // httptest certificate; target URL remains pinned.
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			defer transport.CloseIdleConnections()
			agg.handoffHTTP.Transport = transport
			target, err := agg.HandoffTarget(context.Background(), "e1")
			require.NoError(t, err)
			require.True(t, target.Enabled)
			require.Equal(t, signer.Origin+"/admin/edge-handoff", target.URL)
			session, err := agg.MintAdminSession(context.Background(), "e1", 17, EdgeHandoffRequest{Challenge: EdgeHandoffDigest("v"), Attempt: EdgeHandoffDigest("a")})
			if redirect {
				require.Error(t, err)
				require.Nil(t, session)
			} else {
				require.NoError(t, err)
				require.Equal(t, strings.Repeat("A", 43), session.Code)
			}
			require.Equal(t, int32(1), calls.Load())
		})
	}
}
