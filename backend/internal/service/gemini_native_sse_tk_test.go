//go:build unit

package service

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type geminiNativeObservedWriter struct {
	gin.ResponseWriter
	writes chan string
}

func (w *geminiNativeObservedWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.writes <- string(b)
	return n, err
}

func TestGeminiNativeRelayPreservesHeartbeatBeforeContent(t *testing.T) {
	for _, path := range []string{"gemini", "antigravity"} {
		for name, heartbeat := range map[string]string{
			"legacy_comment": ": keepalive\r\n\r\n",
			"updated_edge":   "\n",
			"whitespace":     " \t\r\n",
		} {
			t.Run(path+"/"+name, func(t *testing.T) {
				gin.SetMode(gin.TestMode)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini:streamGenerateContent", nil)
				writes := make(chan string, 32)
				c.Writer = &geminiNativeObservedWriter{ResponseWriter: c.Writer, writes: writes}
				reader, writer := io.Pipe()
				defer reader.Close()
				defer writer.Close()
				resp := &http.Response{StatusCode: http.StatusOK, Body: reader, Header: http.Header{"Content-Type": {"text/event-stream"}}}
				done := make(chan struct{})
				var streamErr error
				go func() {
					defer close(done)
					if path == "gemini" {
						_, err := (&GeminiMessagesCompatService{cfg: &config.Config{}}).handleNativeStreamingResponse(c, resp, time.Now(), false, nil, "")
						streamErr = err
					} else {
						_, err := newAntigravityTestService(&config.Config{}).handleGeminiStreamingResponse(c, resp, time.Now())
						streamErr = err
					}
				}()
				t.Cleanup(func() {
					_ = writer.Close()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("native relay did not exit after upstream close")
					}
				})
				_, err := io.WriteString(writer, heartbeat)
				require.NoError(t, err)
				select {
				case frame := <-writes:
					require.Equal(t, geminiNativeSSEKeepaliveFrame, frame)
				case <-time.After(2 * time.Second):
					t.Fatal("upstream heartbeat was swallowed before content arrived")
				}
				_, err = io.WriteString(writer, "event: message\nid: private-id\nretry: 1000\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\ndata: [DONE]\n")
				require.NoError(t, err)
				require.NoError(t, writer.Close())
				<-done
				require.NoError(t, streamErr)
				body := rec.Body.String()
				require.True(t, geminiCLISSEFullyDrained(body), "body=%q", body)
				require.Contains(t, body, `"text":"ok"`)
				for _, rejected := range []string{"event:", "id:", "retry:", "[DONE]", ": keepalive"} {
					require.NotContains(t, body, rejected)
				}
			})
		}
	}
}

type geminiNativeErrorReader struct{ io.Reader }

func (r geminiNativeErrorReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		return n, errors.New("upstream connection reset")
	}
	return n, err
}

func TestGeminiNativeTerminalErrorsAreDataFrames(t *testing.T) {
	for _, scenario := range []string{"gemini_read_error", "antigravity_read_error", "antigravity_line_limit", "antigravity_timeout"} {
		t.Run(scenario, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini:streamGenerateContent", nil)
			cfg := &config.Config{}
			partial := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"
			var upstream io.ReadCloser = io.NopCloser(geminiNativeErrorReader{strings.NewReader(partial)})
			wantReason := "stream_read_error"
			switch scenario {
			case "antigravity_line_limit":
				cfg.Gateway.MaxLineSize = 64 * 1024
				upstream = io.NopCloser(strings.NewReader(strings.Repeat("x", 128*1024)))
				wantReason = "response_too_large"
			case "antigravity_timeout":
				cfg.Gateway.StreamDataIntervalTimeout = 1
				reader, writer := io.Pipe()
				defer writer.Close()
				upstream = reader
				wantReason = "stream_timeout"
			}
			defer upstream.Close()
			resp := &http.Response{StatusCode: http.StatusOK, Body: upstream, Header: http.Header{"Content-Type": {"text/event-stream"}}}
			var err error
			if scenario == "gemini_read_error" {
				_, err = (&GeminiMessagesCompatService{cfg: cfg}).handleNativeStreamingResponse(c, resp, time.Now(), false, nil, "")
			} else {
				_, err = newAntigravityTestService(cfg).handleGeminiStreamingResponse(c, resp, time.Now())
			}
			require.Error(t, err)
			body := rec.Body.String()
			require.True(t, geminiCLISSEFullyDrained(body), "body=%q", body)
			require.NotContains(t, body, "event:")
			require.NotContains(t, body, "finishReason")
			require.NotContains(t, body, "[DONE]")
			require.Equal(t, 1, strings.Count(body, `"error":`))
			var terminal *googleapi.ErrorResponse
			for _, line := range strings.Split(body, "\n") {
				if strings.HasPrefix(line, "data:") && strings.Contains(line, `"error":`) {
					terminal, err = googleapi.ParseError(strings.TrimPrefix(line, "data: "))
					require.NoError(t, err)
				}
			}
			require.NotNil(t, terminal)
			require.Equal(t, http.StatusBadGateway, terminal.Error.Code)
			require.Equal(t, wantReason, terminal.Error.Message)
			require.Equal(t, googleapi.HTTPStatusToGoogleStatus(http.StatusBadGateway), terminal.Error.Status)
		})
	}
}
