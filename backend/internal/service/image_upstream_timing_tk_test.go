package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type imageTimingScriptedBody struct {
	now     *time.Time
	chunks  []string
	delays  []time.Duration
	failure error
	closed  bool
}

func (b *imageTimingScriptedBody) Read(p []byte) (int, error) {
	if len(b.chunks) == 0 {
		if b.failure != nil {
			return 0, b.failure
		}
		return 0, io.EOF
	}
	*b.now = b.now.Add(b.delays[0])
	b.delays = b.delays[1:]
	n := copy(p, b.chunks[0])
	b.chunks = b.chunks[1:]
	return n, nil
}
func (b *imageTimingScriptedBody) Close() error { b.closed = true; return nil }

func TestImageTimingNonstreamObservesArrivalBeforeBufferedParsing(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	now := time.Unix(100, 0)
	timing := &imageUpstreamTiming{now: func() time.Time { return now }, start: now, fields: map[string]int64{}, log: zap.New(core)}
	chunks := []string{
		"data: {\"type\":\"response.created\"}\r\n\r\n",
		"event: response.image_generation_call.in_progress\ndata: {}\n\n",
		"data: {\"type\":\"response.image_generation_call.generating\"}\n\n",
		"data: {\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"private-image\"}\n",
		"\n",
		"data: {\"type\":\"response.image_generation_call.completed\"}\n\n",
		"data: {\"type\":\"response.completed\"}\n\n",
	}
	source := &imageTimingScriptedBody{now: &now, chunks: append([]string{}, chunks...), delays: []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second, 5 * time.Second, 6 * time.Second, 7 * time.Second}}
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: source}
	timing.response(resp, nil)
	// The same ReadAll used by the non-streaming image path must observe events
	// on receipt, not stamp every event with the final 28-second parse time.
	var out strings.Builder
	buffer := make([]byte, 8192)
	for {
		n, err := resp.Body.Read(buffer)
		_, writeErr := out.Write(buffer[:n])
		require.NoError(t, writeErr)
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			break
		}
	}
	require.Equal(t, strings.Join(chunks, ""), out.String())
	require.NoError(t, resp.Body.Close())
	require.True(t, source.closed)
	fields := logs.All()[0].ContextMap()
	require.Equal(t, int64(1000), fields["image_response_created_ms"])
	require.Equal(t, int64(3000), fields["image_tool_started_ms"])
	require.Equal(t, int64(6000), fields["image_generating_ms"])
	require.Equal(t, int64(15000), fields["image_first_partial_ms"], "timestamp must wait for frame delimiter")
	require.Equal(t, int64(21000), fields["image_tool_completed_ms"])
	require.Equal(t, int64(28000), fields["image_response_completed_ms"])
	require.Equal(t, int64(28000), fields["image_body_read_ms"])
	require.Equal(t, int64(len(out.String())), fields["image_upstream_body_bytes"])
	require.Equal(t, "eof", fields["image_body_end_reason"])
	require.Len(t, logs.All(), 1, "Close after EOF must not emit a duplicate attempt")
	require.NotContains(t, fields, "image_tls_done_ms")
	require.NotContains(t, logs.All()[0].ContextMap(), "private-image")
}

func TestImageTimingSSEBoundedAndSplitFrames(t *testing.T) {
	p := imageTimingSSE{}
	got := map[string]int{}
	mark := func(k string) { got[k]++ }
	frame := "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"" + strings.Repeat("x", 100000) + "\"}\r\n\r\n"
	for _, b := range []byte(frame) {
		p.feed([]byte{b}, mark)
		require.LessOrEqual(t, len(p.prefix), imageTimingPrefixLimit)
	}
	require.Equal(t, map[string]int{"image_first_event_ms": 1, "image_result_ms": 1}, got)
	require.True(t, p.truncated)
	p.feed([]byte("data: {\"type\":\"response.failed\"}\n\n"), mark)
	require.Equal(t, 1, got["image_upstream_failed_ms"], "large frame must not swallow subsequent events")
	p.feed([]byte("data: {\"type\":\"response.image_generation_call.generating\"}"), mark)
	require.Zero(t, got["image_generating_ms"], "incomplete frames must not invent completion timestamps")
}

func TestImageTimingTraceAndErrorsPreserveTransportSemantics(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	f := &imageForwardTiming{started: time.Now(), log: zap.New(core)}
	req := httptest.NewRequest(http.MethodPost, "https://example.test/secret?token=secret", strings.NewReader("secret-body"))
	req = req.WithContext(context.WithValue(req.Context(), imageForwardTimingKey{}, f))
	req, timing := observeImageUpstream(req)
	now := time.Unix(200, 0)
	timing.start = now
	timing.now = func() time.Time { return now }
	trace := httptrace.ContextClientTrace(req.Context())
	now = now.Add(12 * time.Millisecond)
	trace.GotConn(httptrace.GotConnInfo{Reused: true})
	now = now.Add(20 * time.Millisecond)
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	now = now.Add(500 * time.Millisecond)
	trace.GotFirstResponseByte()
	source := &imageTimingScriptedBody{now: &now, chunks: []string{"partial"}, delays: []time.Duration{time.Second}, failure: io.ErrUnexpectedEOF}
	resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: source}
	timing.response(resp, nil)
	payload, err := io.ReadAll(resp.Body)
	require.Equal(t, "partial", string(payload))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.NoError(t, resp.Body.Close())
	fields := logs.All()[0].ContextMap()
	require.Equal(t, int64(12), fields["image_got_conn_ms"])
	require.Equal(t, int64(32), fields["image_request_written_ms"])
	require.Equal(t, int64(532), fields["image_first_response_byte_ms"])
	require.Equal(t, true, fields["image_connection_reused"])
	require.Equal(t, "read_error", fields["image_body_end_reason"])
	require.NotContains(t, fields, "image_tool_completed_ms")
	_, retry := observeImageUpstream(req)
	retry.response(nil, errors.New("secret transport error"))
	require.Equal(t, int64(2), logs.All()[1].ContextMap()["image_attempt_index"])
	require.Equal(t, "request_error", logs.All()[1].ContextMap()["image_body_end_reason"])
	require.NotContains(t, logs.All()[1].ContextMap(), "error")
	plain := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	untouched, none := observeImageUpstream(plain)
	require.Same(t, plain, untouched)
	require.Nil(t, none)
	none.response(nil, nil)
}

func TestImageTimingForwardRetryAndKeepaliveWriter(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	c, rec := newOpenAIImagesTestContext(t, nil)
	c.Request = c.Request.WithContext(logger.IntoContext(c.Request.Context(), zap.New(core).With(zap.String("request_id", "inbound-test"))))
	stop := StartOpenAIImagesJSONKeepalive(c, time.Hour)
	defer stop()
	original := c.Writer
	for i := 1; i <= 2; i++ {
		ctx, finish := beginImageForwardTiming(context.Background(), c, newOpenAIImagesAPIKeyAccount())
		req := httptest.NewRequest(http.MethodPost, "https://example.test/images", nil).WithContext(ctx)
		_, timing := observeImageUpstream(req)
		if i == 1 {
			timing.response(nil, errors.New("failed"))
			require.True(t, openAIImagesJSONKeepaliveFromContext(c).beat())
			require.Equal(t, -1, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))
		} else {
			resp := openAIImagesJSONResponse()
			timing.response(resp, nil)
			_, err := io.Copy(io.Discard, resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			_, err = c.Writer.WriteString("result")
			require.NoError(t, err)
			c.Writer.Flush()
		}
		finish(i == 1, i-1)
		require.Same(t, original, c.Writer)
	}
	require.Equal(t, " \nresult", rec.Body.String())
	summaries := logs.FilterMessage("image.forward_timing").All()
	require.Equal(t, int64(1), summaries[0].ContextMap()["image_forward_index"])
	require.Equal(t, true, summaries[0].ContextMap()["image_forward_failed"])
	require.Equal(t, int64(0), summaries[0].ContextMap()["image_downstream_bytes"], "keepalives must not count as image output")
	require.Equal(t, int64(2), summaries[1].ContextMap()["image_forward_index"])
	require.Equal(t, int64(6), summaries[1].ContextMap()["image_downstream_bytes"])
	require.Equal(t, "inbound-test", summaries[1].ContextMap()["request_id"])
	require.Equal(t, 6, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))
}

func TestImageTimingRealHTTPRecordsTLSUploadAndSSEWithoutChangingBody(t *testing.T) {
	payload := "data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.image_generation_call.generating\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "input-image" {
			w.WriteHeader(400)
			return
		}
		// Match live Codex: SSE frames without a Content-Type response header.
		w.Header()["Content-Type"] = nil
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
			return
		}
		if _, err := io.WriteString(w, payload); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	core, logs := observer.New(zap.InfoLevel)
	f := &imageForwardTiming{started: time.Now(), log: zap.New(core)}
	req, err := http.NewRequestWithContext(context.WithValue(context.Background(), imageForwardTimingKey{}, f), http.MethodPost, server.URL, strings.NewReader("input-image"))
	require.NoError(t, err)
	req, timing := observeImageUpstream(req)
	resp, err := server.Client().Do(req)
	timing.response(resp, err)
	require.NoError(t, err)
	require.Empty(t, resp.Header.Get("Content-Type"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, payload, string(body))
	fields := logs.All()[0].ContextMap()
	for _, pair := range [][2]string{{"image_tls_start_ms", "image_tls_done_ms"}, {"image_got_conn_ms", "image_request_written_ms"}, {"image_request_written_ms", "image_first_response_byte_ms"}, {"image_response_created_ms", "image_response_completed_ms"}} {
		require.LessOrEqual(t, fields[pair[0]], fields[pair[1]])
	}
	require.Equal(t, int64(len(payload)), fields["image_upstream_body_bytes"])
	require.Equal(t, false, fields["image_connection_reused"])
	require.NotContains(t, fields, "image_tool_started_ms", "missing upstream milestones stay absent")
}

func TestImageTimingParsesSSERegardlessOfContentType(t *testing.T) {
	for _, contentType := range []string{"", "application/json", "text/event-stream"} {
		t.Run(contentType, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			now := time.Unix(300, 0)
			timing := &imageUpstreamTiming{now: func() time.Time { return now }, start: now, fields: map[string]int64{}, log: zap.New(core)}
			chunks := []string{
				"data: {\"type\":\"response.created\"}\n\n",
				"data: {\"type\":\"response.image_generation_call.generating\"}\n\n",
				"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\"}}\n\n",
				"data: {\"type\":\"response.completed\"}\n\n",
			}
			source := &imageTimingScriptedBody{now: &now, chunks: append([]string{}, chunks...), delays: []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, time.Second}}
			resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: source}
			if contentType != "" {
				resp.Header.Set("Content-Type", contentType)
			}
			timing.response(resp, nil)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, strings.Join(chunks, ""), string(body))
			require.Equal(t, contentType, resp.Header.Get("Content-Type"))
			fields := logs.All()[0].ContextMap()
			require.Equal(t, int64(1000), fields["image_response_created_ms"])
			require.Equal(t, int64(3000), fields["image_generating_ms"])
			require.Equal(t, int64(8000), fields["image_result_ms"])
			require.Equal(t, int64(9000), fields["image_response_completed_ms"])
		})
	}
}

func TestImageTimingJSONDoesNotInventSSEMilestones(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	now := time.Unix(400, 0)
	timing := &imageUpstreamTiming{now: func() time.Time { return now }, start: now, fields: map[string]int64{}, log: zap.New(core)}
	body := `{"type":"response.completed","data":[{"b64_json":"aGVsbG8=","revised_prompt":"data: {\"type\":\"response.created\"}\n\nevent: response.completed"}]}`
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	timing.response(resp, nil)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, body, string(got))
	fields := logs.All()[0].ContextMap()
	require.Equal(t, int64(len(body)), fields["image_upstream_body_bytes"])
	require.Equal(t, "eof", fields["image_body_end_reason"])
	require.NotContains(t, fields, "image_first_event_ms")
	require.NotContains(t, fields, "image_response_created_ms")
	require.NotContains(t, fields, "image_response_completed_ms")
}

func TestForwardImagesEmitsTimingWithoutChangingResult(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	body := []byte(`{"model":"gpt-image-2","prompt":"private prompt"}`)
	c, rec := newOpenAIImagesTestContext(t, body)
	c.Request = c.Request.WithContext(logger.IntoContext(c.Request.Context(), zap.New(core).With(zap.String("request_id", "test-forward"))))
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.UserID, int64(61)))
	upstream := &httpUpstreamRecorder{resp: openAIImagesJSONResponse()}
	svc := newOpenAIImagesTestService(upstream)
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	result, err := svc.ForwardImages(c.Request.Context(), c, newOpenAIImagesAPIKeyAccount(), body, parsed, "")
	require.NoError(t, err)
	require.Equal(t, 1, result.ImageCount)
	require.Contains(t, rec.Body.String(), `"b64_json":"aGVsbG8="`)
	upstreamFields := logs.FilterMessage("image.upstream_timing").All()[0].ContextMap()
	require.Equal(t, "test-forward", upstreamFields["request_id"])
	require.Equal(t, int64(61), upstreamFields["user_id"])
	require.Equal(t, "eof", upstreamFields["image_body_end_reason"])
	require.Equal(t, int64(1), upstreamFields["image_attempt_index"])
	forwardFields := logs.FilterMessage("image.forward_timing").All()[0].ContextMap()
	require.Equal(t, false, forwardFields["image_forward_failed"])
	require.Equal(t, int64(1), forwardFields["image_count"])
	require.Equal(t, int64(rec.Body.Len()), forwardFields["image_downstream_bytes"])
	require.NotContains(t, forwardFields, "prompt")
}
