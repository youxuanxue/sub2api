package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
)

// ForwardGeminiViaChat consumes an already validated execution Plan and reuses
// Chat's endpoint, credentials, supplier adapters and usage accounting.
func (s *OpenAIGatewayService) ForwardGeminiViaChat(ctx context.Context, c *gin.Context, account *Account, request protocolrouter.CanonicalRequest) (*ForwardResult, error) {
	plan, ok := ProtocolExecutionPlan(ctx)
	if !ok || plan.AdapterID() != protocolrouter.AdapterGeminiToChat {
		return nil, protocolrouter.ErrStalePlan
	}
	chat, err := apicompat.GeminiToChatRequest(request.Body(), request.RequestedModel(), request.Profile().Stream)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(chat)
	if err != nil {
		return nil, err
	}
	originalWriter, originalRequest := c.Writer, c.Request
	w := &geminiChatWriter{ResponseWriter: originalWriter, header: make(http.Header), status: http.StatusOK, stream: chat.Stream}
	c.Writer = w
	// Clone the URL too: request identity and candidate state retain the Gemini
	// path, while NewAPI's existing Chat transport consumes its own wire path.
	c.Request = originalRequest.Clone(ctx)
	c.Request.URL.Path = "/v1/chat/completions"
	c.Request.URL.RawPath = ""
	c.Request.URL.RawQuery = ""
	defer func() { c.Writer = originalWriter; c.Request = originalRequest }()
	result, forwardErr := s.ForwardAsChatCompletionsDispatched(ctx, c, account, body, "", plan.ResolvedModel())
	if result != nil {
		// NewAPI may report the mapped wire ID as Model. Gemini settlement's
		// client-facing model must remain the request selected by the Plan.
		result.Model = request.RequestedModel()
	}
	if w.err != nil {
		forwardErr = w.err
	}
	if forwardErr == nil {
		forwardErr = w.finish()
	}
	if forwardErr != nil {
		var retry *UpstreamFailoverError
		if !originalWriter.Written() && errors.As(forwardErr, &retry) {
			return ForwardResultFromOpenAI(result), forwardErr
		}
		status := w.status
		if status < 400 {
			status = http.StatusBadGateway
		}
		if !errors.Is(forwardErr, context.Canceled) {
			w.writeError(status)
		}
		// Strip a retry classification after any client output. The Gemini
		// handler must not replay a started response on another account.
		if errors.As(forwardErr, &retry) {
			forwardErr = fmt.Errorf("upstream failed after Gemini output: %v", retry)
		}
		return ForwardResultFromOpenAI(result), fmt.Errorf("gemini Chat conversion failed: %w", forwardErr)
	}
	return ForwardResultFromOpenAI(result), nil
}

const geminiChatEventLimit = 4 * 1024 * 1024

// geminiChatWriter converts complete SSE events as they arrive. Header writes
// and heartbeats do not commit the downstream response or disable failover.
type geminiChatWriter struct {
	gin.ResponseWriter
	header       http.Header
	status       int
	stream       bool
	buffer       []byte
	event        []byte
	pendingUsage *apicompat.GeminiChatUsage
	terminal     bool
	done         bool
	err          error
}

func (w *geminiChatWriter) Header() http.Header    { return w.header }
func (w *geminiChatWriter) Status() int            { return w.status }
func (w *geminiChatWriter) WriteHeader(status int) { w.status = status }
func (w *geminiChatWriter) WriteHeaderNow()        {}
func (w *geminiChatWriter) Flush() {
	if w.Written() {
		w.ResponseWriter.Flush()
	}
}
func (w *geminiChatWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *geminiChatWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	w.buffer = append(w.buffer, p...)
	if len(w.buffer) > geminiChatEventLimit {
		w.err = fmt.Errorf("chat conversion buffer limit exceeded")
		return 0, w.err
	}
	if !w.stream || w.status >= 400 {
		return len(p), nil
	}
	for {
		index := bytes.IndexByte(w.buffer, '\n')
		if index < 0 {
			break
		}
		line := strings.TrimSuffix(string(w.buffer[:index]), "\r")
		w.buffer = w.buffer[index+1:]
		if line == "" {
			if w.err = w.flushEvent(); w.err != nil {
				return 0, w.err
			}
		} else if strings.HasPrefix(line, "data:") {
			if len(w.event) > 0 {
				w.event = append(w.event, '\n')
			}
			w.event = append(w.event, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")...)
			if len(w.event) > geminiChatEventLimit {
				w.err = fmt.Errorf("chat SSE event limit exceeded")
				return 0, w.err
			}
		}
	}
	return len(p), nil
}

func (w *geminiChatWriter) flushEvent() error {
	data := bytes.TrimSpace(w.event)
	w.event = nil
	if len(data) == 0 {
		return nil
	}
	if string(data) == "[DONE]" {
		if !w.terminal {
			return fmt.Errorf("chat stream ended without finish_reason")
		}
		w.done = true
		return nil
	}
	if w.done {
		return fmt.Errorf("chat payload after stream end")
	}
	response, err := apicompat.ChatToGeminiResponse(data, true)
	if err != nil {
		return err
	}
	if response == nil {
		return nil
	}
	if len(response.Candidates) == 0 && !w.Written() {
		w.pendingUsage = response.Usage
		return nil
	}
	if response.Usage == nil && w.pendingUsage != nil {
		response.Usage = w.pendingUsage
	}
	w.pendingUsage = nil
	for _, c := range response.Candidates {
		if w.terminal && c.Content != nil {
			return fmt.Errorf("chat content after finish_reason")
		}
		if c.FinishReason != "" {
			w.terminal = true
		}
	}
	return w.emit(response)
}

func (w *geminiChatWriter) emit(response *apicompat.GeminiChatResponse) error {
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if !w.Written() {
		h := w.ResponseWriter.Header()
		// Transport headers such as content-length/encoding describe Chat bytes.
		for _, key := range []string{"X-Request-Id", "Retry-After"} {
			if value := w.header.Get(key); value != "" {
				h.Set(key, value)
			}
		}
		h.Del("Content-Length")
		h.Del("Content-Encoding")
		if w.stream {
			h.Set("Content-Type", "text/event-stream")
			h.Set("Cache-Control", "no-cache")
			h.Set("X-Accel-Buffering", "no")
		} else {
			h.Set("Content-Type", "application/json")
		}
		w.ResponseWriter.WriteHeader(http.StatusOK)
	}
	if w.stream {
		body = append(append([]byte("data: "), body...), '\n', '\n')
	}
	_, err = w.ResponseWriter.Write(body)
	if w.stream && err == nil {
		w.ResponseWriter.Flush()
	}
	return err
}

func (w *geminiChatWriter) finish() error {
	if w.status >= 400 {
		return fmt.Errorf("chat upstream status %d", w.status)
	}
	if w.stream {
		if len(bytes.TrimSpace(w.buffer)) > 0 {
			return fmt.Errorf("incomplete Chat SSE frame")
		}
		if err := w.flushEvent(); err != nil {
			return err
		}
		if !w.terminal {
			return fmt.Errorf("chat stream ended without finish_reason")
		}
		return nil
	}
	response, err := apicompat.ChatToGeminiResponse(w.buffer, false)
	if err != nil {
		return err
	}
	if response == nil {
		return fmt.Errorf("empty Chat response")
	}
	return w.emit(response)
}

func (w *geminiChatWriter) writeError(status int) {
	// Do not expose upstream bodies or parser diagnostics (which can contain
	// provider data) in the public Google error contract.
	body, _ := json.Marshal(gin.H{"error": gin.H{"code": status, "status": geminiChatErrorStatus(status), "message": "The upstream request could not be completed"}})
	if w.Written() {
		if w.stream {
			_, w.err = w.ResponseWriter.Write(append(append([]byte("data: "), body...), '\n', '\n'))
			w.ResponseWriter.Flush()
		}
		return
	}
	w.ResponseWriter.Header().Set("Content-Type", "application/json")
	if retry := w.header.Get("Retry-After"); retry != "" {
		w.ResponseWriter.Header().Set("Retry-After", retry)
	}
	w.ResponseWriter.WriteHeader(status)
	_, w.err = w.ResponseWriter.Write(body)
}

func geminiChatErrorStatus(status int) string {
	switch status {
	case 400:
		return "INVALID_ARGUMENT"
	case 401:
		return "UNAUTHENTICATED"
	case 403:
		return "PERMISSION_DENIED"
	case 404:
		return "NOT_FOUND"
	case 429:
		return "RESOURCE_EXHAUSTED"
	case 503:
		return "UNAVAILABLE"
	case 504:
		return "DEADLINE_EXCEEDED"
	default:
		return "INTERNAL"
	}
}
