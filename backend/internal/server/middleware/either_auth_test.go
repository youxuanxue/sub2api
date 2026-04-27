//go:build unit

package middleware

// Issue #63 — POST /api/v1/users/me/qa/export must accept BOTH user-scope
// JWT and user-scope API key. EitherAuth dispatches by Authorization
// header shape and writes the same AuthSubject{UserID} into context
// regardless of branch, so the QA handler downstream can read
// `WHERE user_id = subject.UserID` without caring how the caller
// authenticated.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// reuse stubJWTUserRepo from jwt_auth_test.go (same package).

func newEitherAuthTestEnv(
	t *testing.T,
	users map[int64]*service.User,
	apiKeys map[string]*service.APIKey,
) (*gin.Engine, *service.AuthService) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		RunMode: config.RunModeSimple,
	}
	cfg.JWT.Secret = "test-jwt-secret-32bytes-long!!!"
	cfg.JWT.AccessTokenExpireMinutes = 60

	userRepo := &stubJWTUserRepo{users: users}
	authSvc := service.NewAuthService(nil, userRepo, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil)
	userSvc := service.NewUserService(userRepo, nil, nil, nil)
	jwtMW := NewJWTAuthMiddleware(authSvc, userSvc)

	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(_ context.Context, key string) (*service.APIKey, error) {
			ak, ok := apiKeys[key]
			if !ok {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *ak
			return &clone, nil
		},
	}
	apiKeySvc := service.NewAPIKeyService(apiKeyRepo, userRepo, nil, nil, nil, nil, cfg)
	apiKeyMW := NewAPIKeyAuthMiddleware(apiKeySvc, nil, cfg)

	mw := NewEitherAuthMiddleware(jwtMW, apiKeyMW)

	r := gin.New()
	r.Use(gin.HandlerFunc(mw))
	r.POST("/protected", func(c *gin.Context) {
		subject, ok := GetAuthSubjectFromContext(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"err": "no subject"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"user_id": subject.UserID})
	})

	return r, authSvc
}

func TestEitherAuth_LooksLikeJWT(t *testing.T) {
	cases := []struct {
		token string
		want  bool
	}{
		{"eyJhbGciOiJIUzI1NiJ9.eyJ1c2VyX2lkIjoxfQ.signature", true},
		{"eyJhbGciOiJIUzI1NiJ9.eyJ1c2VyX2lkIjoxfQ", false},
		{"sk-1234567890abcdef", false},
		{"sk-eyJhbCmiOiJIuzI1NiJ9", false},
		{"", false},
		{"eyJ.x.y.z", false},
		{"eyJ.x.y", true},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			require.Equal(t, tc.want, looksLikeJWT(tc.token))
		})
	}
}

func TestUS063_EitherAuth_AcceptsValidJWT(t *testing.T) {
	user := &service.User{
		ID:           7,
		Email:        "dev@example.com",
		Role:         "user",
		Status:       service.StatusActive,
		Concurrency:  4,
		TokenVersion: 1,
	}
	router, authSvc := newEitherAuthTestEnv(t,
		map[int64]*service.User{7: user},
		map[string]*service.APIKey{},
	)

	token, err := authSvc.GenerateToken(user)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "JWT branch must accept a valid JWT")

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, float64(7), body["user_id"])
}

func TestUS063_EitherAuth_AcceptsValidAPIKey(t *testing.T) {
	user := &service.User{
		ID:          42,
		Email:       "m0@example.com",
		Role:        "user",
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 5,
	}
	apiKey := &service.APIKey{
		ID:     900,
		UserID: user.ID,
		Key:    "sk-m0-pipeline-key-1234567890abcdef",
		Status: service.StatusActive,
		User:   user,
	}
	router, _ := newEitherAuthTestEnv(t,
		map[int64]*service.User{42: user},
		map[string]*service.APIKey{apiKey.Key: apiKey},
	)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "API-key branch must accept a valid sk- token (issue #63)")

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, float64(42), body["user_id"], "AuthSubject.UserID must come from the API key's owning user")
}

func TestUS063_EitherAuth_AcceptsAPIKeyViaXAPIKeyHeader(t *testing.T) {
	user := &service.User{
		ID:          43,
		Email:       "header@example.com",
		Role:        "user",
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 5,
	}
	apiKey := &service.APIKey{
		ID:     901,
		UserID: user.ID,
		Key:    "sk-header-only-1234567890abcdef",
		Status: service.StatusActive,
		User:   user,
	}
	router, _ := newEitherAuthTestEnv(t,
		map[int64]*service.User{43: user},
		map[string]*service.APIKey{apiKey.Key: apiKey},
	)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/protected", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code,
		"missing Authorization header but valid x-api-key must hit the API-key branch")
}

func TestUS063_EitherAuth_RejectsBogusBearerToken(t *testing.T) {
	router, _ := newEitherAuthTestEnv(t,
		map[int64]*service.User{},
		map[string]*service.APIKey{},
	)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/protected", nil)
	req.Header.Set("Authorization", "Bearer sk-not-a-real-key")
	router.ServeHTTP(w, req)

	// Routed to API-key branch (not JWT shape), branch returns 401 on lookup.
	require.Equal(t, http.StatusUnauthorized, w.Code)
	var body ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "INVALID_API_KEY", body.Code,
		"non-JWT bearer must be tried as an API key, not silently accepted")
}

func TestUS063_EitherAuth_RejectsTamperedJWT(t *testing.T) {
	router, _ := newEitherAuthTestEnv(t,
		map[int64]*service.User{},
		map[string]*service.APIKey{},
	)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/protected", nil)
	// Looks like a JWT (eyJ + 2 dots) → routes to JWT branch, which rejects.
	req.Header.Set("Authorization", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJ1c2VyX2lkIjoxfQ.invalid_sig")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	var body ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "INVALID_TOKEN", body.Code)
}

func TestUS063_EitherAuth_RejectsMissingCredentials(t *testing.T) {
	router, _ := newEitherAuthTestEnv(t,
		map[int64]*service.User{},
		map[string]*service.APIKey{},
	)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/protected", nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code,
		"no Authorization and no x-api-key header must 401")
}

// Defense in depth: the JWT branch must reject an API key shaped like
// `sk-...` even if the caller mistakenly puts it through the JWT branch.
// (The dispatcher routes to API-key in that case, but if it ever
// regressed and routed to JWT, we want a hard 401, not a panic.)
func TestUS063_EitherAuth_DispatcherDoesNotPanicOnUnknownShape(t *testing.T) {
	router, _ := newEitherAuthTestEnv(t,
		map[int64]*service.User{},
		map[string]*service.APIKey{},
	)

	// Empty Bearer token — not a JWT shape, routed to API-key branch.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/protected", nil)
	req.Header.Set("Authorization", "Bearer ")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}
