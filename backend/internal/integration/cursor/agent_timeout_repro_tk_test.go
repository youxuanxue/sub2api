package cursor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

func TestRunAgentFrameIdleTimeout(t *testing.T) {
	input := AgentRequest{Model: "composer-2.5", Messages: []AgentMessage{{Role: "user", Text: "hang"}}}
	prev := agentStreamIdleTimeoutNS.Swap(int64(200 * time.Millisecond))
	t.Cleanup(func() { agentStreamIdleTimeoutNS.Store(prev) })

	t.Run("immediate_closed_pipe_still_wraps_as_stream_interrupted", func(t *testing.T) {
		_, err := RunAgent(context.Background(), "test-token", input, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: io.NopCloser(&errBody{err: io.ErrClosedPipe})}, nil
		}, nil)
		require.ErrorContains(t, err, "cursor stream interrupted: io: read/write on closed pipe")
		require.ErrorIs(t, err, io.ErrClosedPipe)
		require.False(t, errors.Is(err, errAgentStreamIdle))
	})

	t.Run("hanging_body_returns_stream_idle_timeout", func(t *testing.T) {
		started := time.Now()
		_, err := RunAgent(context.Background(), "test-token", input, func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: io.NopCloser(&ctxWaitBody{ctx: req.Context()})}, nil
		}, nil)
		elapsed := time.Since(started)
		t.Logf("elapsed=%s err=%v", elapsed.Round(time.Millisecond), err)
		require.GreaterOrEqual(t, elapsed, 180*time.Millisecond)
		require.Less(t, elapsed, 2*time.Second)
		require.ErrorIs(t, err, errAgentStreamIdle)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Contains(t, err.Error(), "stream idle timeout")
		require.NotContains(t, err.Error(), "cursor stream interrupted")
	})

	t.Run("real_h2_silence_returns_stream_idle_timeout", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/connect+proto")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
		})
		protocols := new(http.Protocols)
		protocols.SetUnencryptedHTTP2(true)
		protocols.SetHTTP1(false)
		server := &http.Server{Handler: handler, Protocols: protocols}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer func() { _ = ln.Close() }()
		go func() { _ = server.Serve(ln) }()
		defer func() { _ = server.Close() }()

		transport := &http.Transport{
			ForceAttemptHTTP2: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, ln.Addr().String())
			},
		}
		transport.Protocols = new(http.Protocols)
		transport.Protocols.SetUnencryptedHTTP2(true)
		transport.Protocols.SetHTTP1(false)
		_, err = http2.ConfigureTransports(transport)
		require.NoError(t, err)
		client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		defer transport.CloseIdleConnections()

		started := time.Now()
		_, runErr := RunAgent(context.Background(), "test-token", input, func(req *http.Request) (*http.Response, error) {
			local, err := http.NewRequestWithContext(req.Context(), req.Method, "http://127.0.0.1/agent.v1.AgentService/Run", req.Body)
			if err != nil {
				return nil, err
			}
			local.Header = req.Header.Clone()
			local.ContentLength = req.ContentLength
			return client.Do(local)
		}, nil)
		elapsed := time.Since(started)
		t.Logf("elapsed=%s err=%v", elapsed.Round(time.Millisecond), runErr)
		require.GreaterOrEqual(t, elapsed, 180*time.Millisecond)
		require.Less(t, elapsed, 2*time.Second)
		require.ErrorIs(t, runErr, errAgentStreamIdle)
		require.ErrorIs(t, runErr, context.DeadlineExceeded)
		require.Contains(t, fmt.Sprint(runErr), "stream idle timeout")
		require.NotContains(t, fmt.Sprint(runErr), "cursor stream interrupted")
	})

	t.Run("live_frames_reset_idle_budget", func(t *testing.T) {
		// Emit a valid incomplete stream of frames spaced under the idle budget
		// for longer than one idle window, then close — proves wall-clock > idle
		// is allowed when frames keep arriving (EOF still fails for missing usage).
		pr, pw := io.Pipe()
		go func() {
			defer func() { _ = pw.Close() }()
			for i := 0; i < 4; i++ {
				time.Sleep(80 * time.Millisecond)
				_ = writeAgentFrame(pw, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "tick"}}})
			}
		}()
		started := time.Now()
		_, err := RunAgent(context.Background(), "test-token", input, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: pr}, nil
		}, nil)
		elapsed := time.Since(started)
		require.GreaterOrEqual(t, elapsed, 300*time.Millisecond, "must outlive a single idle window via frame resets")
		require.Error(t, err)
		require.False(t, errors.Is(err, errAgentStreamIdle), "live frames must not trip idle; got %v", err)
		require.ErrorContains(t, err, "interrupted")
	})

	t.Run("hanging_do_returns_stream_idle_timeout", func(t *testing.T) {
		// Regression: idle must cover do() itself. A transport that never returns
		// (e.g. duplex deadlock waiting on request body EOF) previously held the
		// goroutine forever once the wall-clock 2m WithTimeout was removed.
		started := time.Now()
		_, err := RunAgent(context.Background(), "test-token", input, func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}, nil)
		elapsed := time.Since(started)
		t.Logf("elapsed=%s err=%v", elapsed.Round(time.Millisecond), err)
		require.GreaterOrEqual(t, elapsed, 180*time.Millisecond)
		require.Less(t, elapsed, 2*time.Second)
		require.ErrorIs(t, err, errAgentStreamIdle)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Contains(t, err.Error(), "stream idle timeout")
	})

	for _, tc := range []struct {
		name      string
		afterIdle bool
	}{
		{name: "ready_do_response_is_closed"},
		{name: "late_do_response_is_closed_after_idle_timeout", afterIdle: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Equal wall-clock sleeps do not guarantee that do() is ready when
			// the idle timer fires. Order the late response after cancellation
			// and verify body reclamation on both sides of the deadline.
			bodyClosed := make(chan struct{})
			_, err := RunAgent(context.Background(), "test-token", input, func(req *http.Request) (*http.Response, error) {
				if tc.afterIdle {
					<-req.Context().Done()
				}
				return &http.Response{
					StatusCode: 200,
					ProtoMajor: 2,
					Body:       &closeNotifyBody{closed: bodyClosed, err: io.EOF},
				}, nil
			}, nil)
			if tc.afterIdle {
				require.ErrorIs(t, err, errAgentStreamIdle)
				require.ErrorIs(t, err, context.DeadlineExceeded)
			} else {
				require.ErrorIs(t, err, io.EOF)
				require.NotErrorIs(t, err, errAgentStreamIdle)
			}
			select {
			case <-bodyClosed:
			case <-time.After(2 * time.Second):
				t.Fatal("response body was not closed after do completion")
			}
		})
	}

	t.Run("processing_time_does_not_consume_idle_budget", func(t *testing.T) {
		// Idle budget must cover waiting only. A slow emit longer than one idle
		// window after a live frame must not trip idle when the stream then ends.
		pr, pw := io.Pipe()
		go func() {
			defer func() { _ = pw.Close() }()
			_ = writeAgentFrame(pw, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "tick"}}})
		}()
		started := time.Now()
		_, err := RunAgent(context.Background(), "test-token", input, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: pr}, nil
		}, func(AgentEvent) error {
			time.Sleep(250 * time.Millisecond)
			return nil
		})
		elapsed := time.Since(started)
		require.GreaterOrEqual(t, elapsed, 250*time.Millisecond)
		require.False(t, errors.Is(err, errAgentStreamIdle), "emit/processing longer than idle must not trip idle; got %v", err)
		require.Error(t, err)
		require.ErrorContains(t, err, "interrupted")
	})
}

type closeNotifyBody struct {
	closed chan struct{}
	err    error
	once   sync.Once
}

func (b *closeNotifyBody) Read([]byte) (int, error) { return 0, b.err }

func (b *closeNotifyBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

type errBody struct{ err error }

func (b *errBody) Read([]byte) (int, error) { return 0, b.err }

type ctxWaitBody struct{ ctx context.Context }

func (b *ctxWaitBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	if cause := context.Cause(b.ctx); cause != nil {
		return 0, cause
	}
	return 0, b.ctx.Err()
}
