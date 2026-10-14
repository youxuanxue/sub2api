package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type imageTimedBody struct {
	now     *time.Time
	reads   int
	closed  bool
	failure error
}

func (b *imageTimedBody) Read(p []byte) (int, error) {
	b.reads++
	if b.reads > 1 {
		return 0, io.EOF
	}
	*b.now = b.now.Add(17 * time.Second)
	return copy(p, "image-payload"), b.failure
}
func (b *imageTimedBody) Close() error { b.closed = true; return nil }

func TestImageIngressTimingAttributesBodyWaitAndPreservesReadError(t *testing.T) {
	for name, failure := range map[string]error{"complete": nil, "partial_error": io.ErrUnexpectedEOF} {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(100, 0)
			body := &imageTimedBody{now: &now, failure: failure}
			req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", nil)
			req.Body, req.ContentLength = body, -1
			req = WithImageIngressTiming(req, now)
			timing := imageIngressTimingFrom(req.Context())
			timing.now = func() time.Time { return now }
			require.Zero(t, body.reads, "instrumentation must not eagerly read uploads")
			finishAuth := BeginImageIngressPhase(req.Context(), ImageIngressKeyAuth)
			now = now.Add(3 * time.Millisecond)
			finishAuth()
			raw, err := io.ReadAll(req.Body)
			require.Equal(t, []byte("image-payload"), raw)
			require.True(t, errors.Is(err, failure))
			finishCandidates := BeginImageIngressPhase(req.Context(), ImageIngressCandidates)
			now = now.Add(11 * time.Millisecond)
			finishCandidates()
			finishBind := BeginImageIngressPhase(req.Context(), ImageIngressBind)
			now = now.Add(2 * time.Millisecond)
			finishBind()
			FinishImageIngress(req.Context())
			expected := map[string]int64{
				"image_ingress_ms": 17016, "image_ingress_key_auth_ms": 3,
				"image_ingress_candidates_ms": 11, "image_ingress_bind_ms": 2,
				"image_request_body_read_ms": 17000, "image_request_body_bytes": 13,
				"image_request_content_length": -1,
			}
			require.Equal(t, expected, ImageIngressTimingFields(req.Context()))
			// Upstream work and later retries must not inflate entry-stage timings.
			now = now.Add(time.Minute)
			finishAuth()
			finishCandidates = BeginImageIngressPhase(req.Context(), ImageIngressCandidates)
			now = now.Add(time.Second)
			finishCandidates()
			FinishImageIngress(req.Context())
			require.Equal(t, expected, ImageIngressTimingFields(req.Context()))
			require.NoError(t, req.Body.Close())
			require.True(t, body.closed)
		})
	}
}

func TestImageIngressTimingSkipsOtherEndpointsAndDoesNotDoubleWrap(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/images/generations/async", "/health"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("secret"))
		require.Same(t, req, WithImageIngressTiming(req, time.Now()))
		BeginImageIngressPhase(req.Context(), ImageIngressKeyAuth)()
		FinishImageIngress(req.Context())
		require.Nil(t, ImageIngressTimingFields(req.Context()))
	}
	for _, path := range []string{"/v1/images/edits", "/v1/images/generations", "/images/edits", "/images/generations"} {
		req := WithImageIngressTiming(httptest.NewRequest(http.MethodPost, path, strings.NewReader("payload")), time.Now())
		require.Same(t, req, WithImageIngressTiming(req, time.Now()))
		raw, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Equal(t, "payload", string(raw))
		fields := ImageIngressTimingFields(req.Context())
		require.Equal(t, int64(7), fields["image_request_body_bytes"])
		require.NotContains(t, fields, "image_ingress_ms", "routing has not run")
		require.NotContains(t, fields, "image_ingress_candidates_ms", "missing is not observed zero")
	}
	require.Nil(t, ImageIngressTimingFields(context.Background()))
}
