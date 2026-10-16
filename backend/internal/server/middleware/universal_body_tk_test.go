//go:build unit

package middleware

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type universalFailingBody struct{ err error }

func (b universalFailingBody) Read([]byte) (int, error) { return 0, b.err }

func TestMaybeResolveUniversal_RejectsCorruptCompressedBodyBeforeAdmission(t *testing.T) {
	c, recorder := newTestCtx(http.MethodPost, "/v1/responses", "invalid gzip data")
	c.Request.Header.Set("Content-Encoding", "gzip")
	key := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeUniversal}
	span := &stubSpanLister{groups: []service.Group{activeGroup(2, service.PlatformOpenAI)}}
	require.True(t, MaybeResolveUniversal(c, key, service.NewUniversalRoutingResolver(span)))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, span.calls)
	require.Nil(t, key.GroupID)
	remaining, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	require.Equal(t, "invalid gzip data", string(remaining))
}

func TestMaybeResolveUniversal_RejectsIncompleteBody(t *testing.T) {
	const body = `{"model":"gpt-5.6-sol","input":"hello"}`
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages", "/v1/images/edits", "/v1beta/models/gemini-test:generateContent"} {
		for _, failure := range []string{"oversize", "interrupted"} {
			for _, preRead := range []string{"none", "model_peek", "provider_rewrite"} {
				t.Run(path+"/"+failure+"/"+preRead, func(t *testing.T) {
					c, recorder := newTestCtx(http.MethodPost, path, "")
					c.Request.ContentLength = -1
					var expectedErr error = io.ErrUnexpectedEOF
					status := http.StatusBadRequest
					if failure == "oversize" {
						// The retained prefix is valid JSON; truncation must still reject.
						c.Request.Body = http.MaxBytesReader(c.Writer, io.NopCloser(strings.NewReader(body+strings.Repeat(" ", 64))), int64(len(body)))
						status = http.StatusRequestEntityTooLarge
					} else {
						c.Request.Body = io.NopCloser(io.MultiReader(strings.NewReader(body), universalFailingBody{expectedErr}))
					}
					key := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeUniversal}
					switch preRead {
					case "model_peek":
						_ = peekModelFromJSONBody(c)
					case "provider_rewrite":
						MaybeRewriteOpenRouterProviderChatBody(c, key, &service.SettingService{})
					}
					span := &stubSpanLister{groups: []service.Group{activeGroup(2, service.PlatformOpenAI)}}
					resolver := service.NewUniversalRoutingResolver(span)
					require.True(t, MaybeResolveUniversal(c, key, resolver))
					require.True(t, c.IsAborted())
					require.Equal(t, status, recorder.Code)
					var response struct {
						Error struct {
							Message string `json:"message"`
						} `json:"error"`
					}
					require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
					message := "Failed to read request body"
					if failure == "oversize" {
						message = fmt.Sprintf("Request body too large (stage=read, limit_bytes=%d)", len(body))
					}
					require.Equal(t, message, response.Error.Message)
					require.Zero(t, span.calls, "failed bodies must not reach authorization candidate lookup")
					require.Nil(t, key.GroupID, "failed bodies must not bind a billing origin")
					_, err := io.ReadAll(c.Request.Body)
					if failure == "oversize" {
						var tooLarge *http.MaxBytesError
						require.ErrorAs(t, err, &tooLarge)
						require.EqualValues(t, len(body), tooLarge.Limit)
					} else {
						require.ErrorIs(t, err, expectedErr)
					}
				})
			}
		}
	}
}

func TestUniversalBodyPeek_PreservesCompletePayload(t *testing.T) {
	const payload = `{"model":"gpt-5.6-sol","input":"hello"}`
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err := writer.Write([]byte(payload))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	for _, encoding := range []string{"identity", "gzip"} {
		t.Run(encoding, func(t *testing.T) {
			raw := []byte(payload)
			if encoding == "gzip" {
				raw = compressed.Bytes()
			}
			c, _ := newTestCtx(http.MethodPost, "/v1/responses", string(raw))
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, int64(len(raw)))
			c.Request.Header.Set("Content-Encoding", encoding)
			beforeLength := c.Request.ContentLength
			beforeHeaders := c.Request.Header.Clone()
			for i := 0; i < 3; i++ {
				require.Equal(t, "gpt-5.6-sol", peekModelFromJSONBody(c))
			}
			got, err := io.ReadAll(c.Request.Body)
			require.NoError(t, err)
			require.Equal(t, raw, got)
			require.Equal(t, beforeHeaders, c.Request.Header)
			require.Equal(t, beforeLength, c.Request.ContentLength)
		})
	}
}

func TestUniversalBodyPeek_PreservesFailureForDirectConsumer(t *testing.T) {
	readErr := errors.New("source read failed")
	c, _ := newTestCtx(http.MethodPost, "/v1/responses", "")
	c.Request.Body = io.NopCloser(io.MultiReader(strings.NewReader(`{"model":"gpt-5.6-sol"}`), universalFailingBody{readErr}))
	key := &service.APIKey{RoutingMode: service.RoutingModeDirect}
	MaybeRewriteOpenRouterProviderChatBody(c, key, &service.SettingService{})
	_, err := io.ReadAll(c.Request.Body)
	require.ErrorIs(t, err, readErr)
}

// The same route limit must reach candidate preparation and the direct reader.
// The 100 MB fixture is a gateway byte-boundary check, not a supplier media probe.
func TestUniversalBodyConfiguredLimitParity(t *testing.T) {
	const limit = 100_000_000
	prefix := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"data:video/mp4;base64,`)
	suffix := []byte(`"}}]}]}`)
	for _, size := range []int{65 << 20, limit, limit + 1} {
		payload := bytes.Repeat([]byte("A"), size)
		copy(payload, prefix)
		copy(payload[len(payload)-len(suffix):], suffix)
		for _, mode := range []string{service.RoutingModeDirect, service.RoutingModeUniversal} {
			t.Run(fmt.Sprintf("%s/%d", mode, size), func(t *testing.T) {
				span := &stubSpanLister{groups: []service.Group{activeGroup(20, service.PlatformOpenAI)}}
				key := &service.APIKey{ID: 1, UserID: 1, RoutingMode: mode}
				router := gin.New()
				router.Use(RequestBodyLimit(limit))
				consumed := false
				router.POST("/v1/chat/completions", func(c *gin.Context) {
					if MaybeResolveUniversal(c, key, service.NewUniversalRoutingResolver(span)) {
						return
					}
					// A conflicting fallback must not overwrite the actual route limit.
					got, err := pkghttputil.ReadLenientJSONRequestBodyWithPrealloc(c.Request, 64<<20)
					if size > limit {
						var maxErr *http.MaxBytesError
						require.ErrorAs(t, err, &maxErr)
						require.EqualValues(t, limit, maxErr.Limit)
						c.AbortWithStatus(http.StatusRequestEntityTooLarge)
						return
					}
					require.NoError(t, err)
					require.True(t, bytes.Equal(payload, got), "media bytes must survive routing unchanged")
					consumed = true
					c.Status(http.StatusNoContent)
				})
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload)))
				if size > limit {
					require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
					require.False(t, consumed)
					require.Zero(t, span.calls)
					if mode == service.RoutingModeUniversal {
						require.Contains(t, rec.Body.String(), "stage=read, limit_bytes=100000000")
					}
				} else {
					require.Equal(t, http.StatusNoContent, rec.Code)
					require.True(t, consumed)
				}
			})
		}
	}
}

func TestUniversalBodyNormalizationExpansionLimit(t *testing.T) {
	body := []byte("{\"model\":\"gpt-5\",\"messages\":[{\"role\":\"user\",\"content\":\"a\nb\"}]}")
	span := &stubSpanLister{groups: []service.Group{activeGroup(20, service.PlatformOpenAI)}}
	key := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeUniversal}
	router := gin.New()
	router.Use(RequestBodyLimit(int64(len(body))))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		require.True(t, MaybeResolveUniversal(c, key, service.NewUniversalRoutingResolver(span)))
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Contains(t, rec.Body.String(), fmt.Sprintf("stage=normalize, limit_bytes=%d", len(body)))
	require.Zero(t, span.calls)
}
