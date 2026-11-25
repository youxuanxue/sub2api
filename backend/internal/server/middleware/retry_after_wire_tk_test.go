//go:build unit

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRequestLoggerRetryAfterOnWire(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		header, want string
		body         bool
		writeHeader  bool
	}{
		{"429 with JSON", 429, "", "60", true, false},
		{"empty 429", 429, "", "60", false, false},
		{"WriteHeader 429", 429, "", "60", false, true},
		{"known reset", 429, "17", "17", true, false},
		{"normal reply", 200, "", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(RequestLogger())
			r.GET("/", func(c *gin.Context) {
				if tc.header != "" {
					c.Header("Retry-After", tc.header)
				}
				switch {
				case tc.writeHeader:
					c.Writer.WriteHeader(tc.status)
				case tc.body:
					c.JSON(tc.status, gin.H{"status": tc.status})
				default:
					c.Status(tc.status)
				}
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
			require.Equal(t, tc.status, w.Result().StatusCode)
			require.Equal(t, tc.want, w.Result().Header.Get("Retry-After"))
		})
	}
}
