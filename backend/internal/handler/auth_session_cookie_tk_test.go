package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegistrableParentDomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		host string
		want string
	}{
		{host: "tokenkey.dev", want: "tokenkey.dev"},
		{host: "api.tokenkey.dev", want: "tokenkey.dev"},
		{host: "edge-uk.tokenkey.dev", want: "tokenkey.dev"},
		{host: "localhost", want: ""},
		{host: "127.0.0.1", want: ""},
		{host: "internal", want: ""},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.host, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, registrableParentDomain(tc.host))
		})
	}
}

func TestSetTkRefreshSessionCookieUsesParentDomainFromFrontendURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := &AuthHandler{
		cfg: &config.Config{
			Server: config.ServerConfig{
				FrontendURL: "https://tokenkey.dev",
			},
			JWT: config.JWTConfig{
				RefreshTokenExpireDays: 7,
			},
		},
	}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "https://api.tokenkey.dev/api/v1/auth/login", nil)
	ginCtx.Request.Header.Set("X-Forwarded-Proto", "https")

	handler.setTkRefreshSessionCookie(ginCtx, "refresh-token-value")

	cookie := findCookie(recorder.Result().Cookies(), tkRefreshSessionCookieName)
	require.NotNil(t, cookie)
	require.Equal(t, "tokenkey.dev", cookie.Domain)
	require.Equal(t, tkRefreshSessionCookiePath, cookie.Path)
	require.True(t, cookie.HttpOnly)
	require.True(t, cookie.Secure)
	require.Equal(t, http.SameSiteLaxMode, cookie.SameSite)

	decoded, err := decodeCookieValue(cookie.Value)
	require.NoError(t, err)
	require.Equal(t, "refresh-token-value", decoded)
}

func TestClearTkRefreshSessionCookieClearsParentDomainCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := &AuthHandler{
		cfg: &config.Config{
			Server: config.ServerConfig{
				FrontendURL: "https://tokenkey.dev",
			},
		},
	}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "https://tokenkey.dev/api/v1/auth/logout", nil)
	ginCtx.Request.Header.Set("X-Forwarded-Proto", "https")

	handler.clearTkRefreshSessionCookie(ginCtx)

	cookie := findCookie(recorder.Result().Cookies(), tkRefreshSessionCookieName)
	require.NotNil(t, cookie)
	require.Equal(t, "tokenkey.dev", cookie.Domain)
	require.Equal(t, -1, cookie.MaxAge)
	require.Equal(t, "", cookie.Value)
}

func TestResolveRefreshTokenForRequestPrefersBodyOverCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := &AuthHandler{
		cfg: &config.Config{
			Server: config.ServerConfig{
				FrontendURL: "https://tokenkey.dev",
			},
		},
	}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "https://tokenkey.dev/api/v1/auth/refresh", nil)
	ginCtx.Request.Header.Set("X-Forwarded-Proto", "https")
	ginCtx.Request.AddCookie(&http.Cookie{
		Name:  tkRefreshSessionCookieName,
		Value: encodeCookieValue("cookie-token"),
		Path:  tkRefreshSessionCookiePath,
	})

	token, err := handler.resolveRefreshTokenForRequest(ginCtx, "body-token")
	require.NoError(t, err)
	require.Equal(t, "body-token", token)
}

func TestResolveRefreshTokenForRequestFallsBackToCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := &AuthHandler{
		cfg: &config.Config{
			Server: config.ServerConfig{
				FrontendURL: "https://tokenkey.dev",
			},
		},
	}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "https://tokenkey.dev/api/v1/auth/refresh", nil)
	ginCtx.Request.Header.Set("X-Forwarded-Proto", "https")
	ginCtx.Request.AddCookie(&http.Cookie{
		Name:  tkRefreshSessionCookieName,
		Value: encodeCookieValue("cookie-token"),
		Path:  tkRefreshSessionCookiePath,
	})

	token, err := handler.resolveRefreshTokenForRequest(ginCtx, "")
	require.NoError(t, err)
	require.Equal(t, "cookie-token", token)
}
