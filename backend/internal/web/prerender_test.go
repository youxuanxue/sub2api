package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsCrawler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ua   string
		want bool
	}{
		{name: "empty", ua: "", want: false},
		{name: "browser", ua: "Mozilla/5.0 Chrome/120", want: false},
		{name: "googlebot", ua: "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", want: true},
		{name: "telegram", ua: "TelegramBot (like TwitterBot)", want: true},
		{name: "slack", ua: "Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isCrawler(tt.ua))
		})
	}
}

func TestPrerenderMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		path       string
		ua         string
		wantStatus int
		wantHeader string
		wantBody   string
	}{
		{
			name:       "normal user falls through",
			path:       "/home",
			ua:         "Mozilla/5.0",
			wantStatus: http.StatusOK,
		},
		{
			name:       "googlebot home prerender",
			path:       "/home",
			ua:         "Googlebot",
			wantStatus: http.StatusOK,
			wantHeader: "1",
			wantBody:   "TokenKey - AI API Gateway",
		},
		{
			name:       "googlebot pricing prerender",
			path:       "/pricing",
			ua:         "Googlebot",
			wantStatus: http.StatusOK,
			wantHeader: "1",
			wantBody:   "TokenKey 定价",
		},
		{
			name:       "googlebot quickstart prerender",
			path:       "/quickstart",
			ua:         "TelegramBot",
			wantStatus: http.StatusOK,
			wantHeader: "1",
			wantBody:   "Quick Start",
		},
		{
			name:       "crawler unknown route falls through",
			path:       "/dashboard",
			ua:         "Googlebot",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(PrerenderMiddleware())
			router.GET("/*path", func(c *gin.Context) {
				c.String(http.StatusOK, "spa")
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.ua != "" {
				req.Header.Set("User-Agent", tt.ua)
			}
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantHeader != "" {
				assert.Equal(t, tt.wantHeader, w.Header().Get("X-Prerender"))
			} else {
				assert.Empty(t, w.Header().Get("X-Prerender"))
			}
			if tt.wantBody != "" {
				assert.Contains(t, w.Body.String(), tt.wantBody)
			} else {
				assert.Equal(t, "spa", w.Body.String())
			}
		})
	}
}

func TestPrerenderMiddlewareRootPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(PrerenderMiddleware())
	router.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, "spa")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("User-Agent", "Googlebot")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "1", w.Header().Get("X-Prerender"))
	assert.Contains(t, w.Body.String(), "TokenKey - AI API Gateway")
}
