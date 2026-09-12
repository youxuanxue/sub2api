//go:build unit

package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type userLookupStub struct {
	user *service.User
	err  error
}

func (s userLookupStub) GetByID(context.Context, int64) (*service.User, error) { return s.user, s.err }

type sessionMinterStub struct {
	family string
	calls  int
}

func (s *sessionMinterStub) GenerateEdgeAdminSessionTokenPair(_ context.Context, _ *service.User, family string) (*service.TokenPair, error) {
	s.family = family
	s.calls++
	return &service.TokenPair{AccessToken: "edge-only-access", RefreshToken: "edge-only-refresh", ExpiresIn: 1800}, nil
}
func handoffRequest(t *testing.T, f gin.HandlerFunc, body any, origin string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Origin", origin)
	f(c)
	return w
}
func TestEdgeAdminSession_LegacyAlwaysGone(t *testing.T) {
	h := NewEdgeAdminSessionHandler(nil, nil, nil)
	w := handoffRequest(t, h.Mint, nil, "")
	require.Equal(t, http.StatusGone, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.NotContains(t, w.Body.String(), "access_token")
}
func TestEdgeAdminHandoff_ExchangeSecurity(t *testing.T) {
	const edge = "https://edge.example"
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	cfg := config.EdgeHandoffConfig{Version: 1, Issuer: "https://prod.example", Signers: map[string]config.EdgeHandoffSigner{"test": {Origin: edge, KeyID: "k1", Seed: base64.RawURLEncoding.EncodeToString(key.Seed())}}, Receiver: &config.EdgeHandoffReceiver{Origin: edge, Issuer: "https://prod.example", AdminUserID: 9, PublicKeys: map[string]string{"k1": base64.RawURLEncoding.EncodeToString(pub)}}}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	file := filepath.Join(t.TempDir(), "trust.json")
	require.NoError(t, os.WriteFile(file, raw, 0600))
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	owner, err := service.NewEdgeAdminHandoff(&config.Config{EdgeHandoffFile: file}, repository.NewEdgeAdminHandoffCache(client))
	require.NoError(t, err)
	for _, tc := range []struct {
		name, role, status, origin string
		wrongProof                 bool
		want                       int
	}{
		{"success", service.RoleAdmin, service.StatusActive, edge, false, 200},
		{"role revoked", service.RoleUser, service.StatusActive, edge, false, 403},
		{"disabled", service.RoleAdmin, service.StatusDisabled, edge, false, 403},
		{"foreign origin", service.RoleAdmin, service.StatusActive, "https://prod.example", false, 403},
		{"wrong proof", service.RoleAdmin, service.StatusActive, edge, true, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verifier := strings.Repeat("A", 43)
			attemptBytes := make([]byte, 32)
			_, err := rand.Read(attemptBytes)
			require.NoError(t, err)
			attempt := base64.RawURLEncoding.EncodeToString(attemptBytes)
			envelope, err := owner.Sign("test", edge, 7, service.EdgeHandoffRequest{Attempt: attempt, Challenge: service.EdgeHandoffDigest(verifier)})
			require.NoError(t, err)
			minter := &sessionMinterStub{}
			h := NewEdgeAdminSessionHandler(owner, userLookupStub{user: &service.User{ID: 9, Role: tc.role, Status: tc.status}}, minter)
			minted := handoffRequest(t, h.MintCode, envelope, "")
			require.Equal(t, 200, minted.Code)
			require.NotContains(t, minted.Body.String(), "token")
			var env struct {
				Data service.EdgeHandoffCode `json:"data"`
			}
			require.NoError(t, json.Unmarshal(minted.Body.Bytes(), &env))
			input := service.EdgeHandoffExchange{Code: env.Data.Code, Verifier: verifier, Attempt: attempt}
			if tc.wrongProof {
				input.Verifier = strings.Repeat("B", 42) + "A"
			}
			result := handoffRequest(t, h.Exchange, input, tc.origin)
			require.Equal(t, tc.want, result.Code)
			if tc.want == 200 {
				require.Contains(t, result.Body.String(), "edge-only-refresh")
				require.True(t, strings.HasPrefix(minter.family, "edge-handoff-"))
				require.Equal(t, 403, handoffRequest(t, h.Exchange, input, edge).Code)
			} else {
				require.Zero(t, minter.calls)
				require.NotContains(t, result.Body.String(), "edge-only")
			}
			if tc.wrongProof {
				input.Verifier = verifier
				require.Equal(t, 200, handoffRequest(t, h.Exchange, input, edge).Code)
			}
			require.Equal(t, 403, handoffRequest(t, h.MintCode, envelope, "").Code, "signed attempt cannot mint another code")
		})
	}
}
