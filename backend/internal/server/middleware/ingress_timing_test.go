//go:build unit

package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

type slowIngressBody struct {
	io.Reader
	delay         time.Duration
	reads, closes int
}

func (b *slowIngressBody) Read(p []byte) (int, error) {
	b.reads++
	if b.delay > 0 {
		time.Sleep(b.delay)
		b.delay = 0
	}
	return b.Reader.Read(p)
}
func (b *slowIngressBody) Close() error { b.closes++; return nil }

func ingressFieldsMap(c *gin.Context) map[string]any {
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range ingressTimingFields(c) {
		f.AddTo(enc)
	}
	return enc.Fields
}

func TestIngressTiming_ObservesWireReadOnceAcrossBodyReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := &slowIngressBody{Reader: strings.NewReader(`{"model":"fixture","messages":[]}`), delay: 25 * time.Millisecond}
	router := gin.New()
	router.Use(Logger())
	var fields map[string]any
	router.POST("/v1/messages", func(c *gin.Context) {
		finish := observeIngressRouting(c)
		first, err := readAndRestoreUniversalBody(c)
		require.NoError(t, err)
		second, err := readAndRestoreUniversalBody(c)
		require.NoError(t, err)
		require.Equal(t, first, second)
		replay, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, first, replay)
		finish()
		// Response generation must not contaminate original upload read time.
		before := ingressFieldsMap(c)
		time.Sleep(25 * time.Millisecond)
		fields = ingressFieldsMap(c)
		require.Equal(t, before["ingress_body_read_ms"], fields["ingress_body_read_ms"])
		require.NoError(t, c.Request.Body.Close())
		c.Data(http.StatusOK, "application/json", replay)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Body = body
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code)
	require.JSONEq(t, `{"model":"fixture","messages":[]}`, rec.Body.String())
	require.EqualValues(t, rec.Body.Len(), fields["ingress_body_bytes"])
	require.EqualValues(t, body.reads, fields["ingress_body_read_calls"])
	require.GreaterOrEqual(t, fields["ingress_body_read_ms"].(int64), int64(25))
	require.GreaterOrEqual(t, fields["ingress_routing_ms"].(int64), fields["ingress_body_read_ms"].(int64))
	require.Equal(t, 1, body.closes)
	require.NotContains(t, fields, "body")
}

func TestIngressTiming_RejectedAuthenticationDoesNotReadBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := &slowIngressBody{Reader: strings.NewReader("unread private body")}
	router := gin.New()
	router.Use(Logger())
	var fields map[string]any
	router.POST("/v1/messages", func(c *gin.Context) { fields = ingressFieldsMap(c); c.AbortWithStatus(401) })
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Body = body
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, 401, rec.Code)
	require.Zero(t, body.reads)
	require.EqualValues(t, 0, fields["ingress_body_bytes"])
	require.NotContains(t, fields, "ingress_body_first_read_ms")
	require.NotContains(t, fields, "ingress_routing_ms")
}

type partialIngressErrorBody struct{}

func (partialIngressErrorBody) Read(p []byte) (int, error) { return copy(p, "ab"), io.ErrUnexpectedEOF }
func (partialIngressErrorBody) Close() error               { return nil }

func TestIngressTiming_PreservesPartialReadError(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Body = partialIngressErrorBody{}
	observeIngress(c, time.Now())
	raw, err := io.ReadAll(c.Request.Body)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Equal(t, "ab", string(raw))
	require.EqualValues(t, 2, ingressFieldsMap(c)["ingress_body_bytes"])
}
