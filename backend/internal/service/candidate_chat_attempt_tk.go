package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const candidateChatMaxAttempts = 3
const candidateChatPendingLimit = 64 * 1024

var errCandidateChatFirstOutput = errors.New("upstream did not produce output within the attempt budget")
var errCandidateChatPendingLimit = errors.New("upstream pre-output buffer exceeded limit")
var errCandidateChatIncomplete = errors.New("upstream chat stream ended without a terminal event")

type candidateChatAttemptKey struct{}

// Only the timer and cancellation callbacks run concurrently with the writer.
type candidateChatAttempt struct {
	gin.ResponseWriter
	ctx        context.Context
	cancel     context.CancelCauseFunc
	mu         sync.Mutex
	timer      *time.Timer
	stopClient func() bool
	started    bool
	header     http.Header
	status     int
	pending    []byte
	line       []byte
	stream     bool
	terminal   bool
}

func newCandidateChatAttempt(ctx context.Context, writer gin.ResponseWriter, stream bool, timeout time.Duration) *candidateChatAttempt {
	upstream, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	a := &candidateChatAttempt{ResponseWriter: writer, ctx: upstream, cancel: cancel, stream: stream, header: writer.Header().Clone(), status: http.StatusOK}
	a.timer = time.AfterFunc(timeout, func() { a.abortBeforeOutput(errCandidateChatFirstOutput) })
	a.stopClient = context.AfterFunc(ctx, func() { a.abortBeforeOutput(ctx.Err()) })
	return a
}

func (a *candidateChatAttempt) abortBeforeOutput(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.started {
		a.cancel(err)
	}
}

func (a *candidateChatAttempt) close() {
	a.timer.Stop()
	a.stopClient()
	a.cancel(context.Canceled)
}

func (a *candidateChatAttempt) release() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := context.Cause(a.ctx); err != nil {
		return err
	}
	if !a.started {
		a.started = true
		a.timer.Stop()
		a.stopClient()
		for key, values := range a.header {
			a.ResponseWriter.Header()[key] = values
		}
		a.ResponseWriter.WriteHeader(a.status)
	}
	return nil
}

func (a *candidateChatAttempt) Header() http.Header               { return a.header }
func (a *candidateChatAttempt) WriteHeader(code int)              { a.status = code }
func (a *candidateChatAttempt) WriteHeaderNow()                   {}
func (a *candidateChatAttempt) Status() int                       { return a.status }
func (a *candidateChatAttempt) WriteString(s string) (int, error) { return a.Write([]byte(s)) }
func (a *candidateChatAttempt) Flush() {
	if a.started {
		a.ResponseWriter.Flush()
	}
}

// Role-only frames, comments and usage-only frames do not end the wait budget.
func (a *candidateChatAttempt) observe(data []byte) bool {
	if !a.stream {
		return gjson.ValidBytes(data)
	}
	a.line = append(a.line, data...)
	useful := false
	for {
		i := bytes.IndexByte(a.line, '\n')
		if i < 0 {
			break
		}
		line := string(bytes.TrimSpace(a.line[:i]))
		a.line = a.line[i+1:]
		payload, ok := extractOpenAISSEDataLine(line)
		if !ok {
			continue
		}
		if payload == "[DONE]" {
			a.terminal = true
			useful = true
			continue
		}
		root := gjson.Parse(payload)
		if root.Get("error").Exists() {
			a.terminal = true
			useful = true
		}
		for _, choice := range root.Get("choices").Array() {
			if finish := choice.Get("finish_reason"); finish.Exists() && finish.Type != gjson.Null && finish.String() != "" {
				a.terminal = true
				useful = true
			}
			delta := choice.Get("delta")
			if delta.Get("content").String() != "" || delta.Get("refusal").String() != "" || delta.Get("reasoning_content").String() != "" || len(delta.Get("tool_calls").Array()) > 0 || delta.Get("function_call").Exists() {
				useful = true
			}
		}
	}
	return useful
}

func (a *candidateChatAttempt) Write(data []byte) (int, error) {
	if err := context.Cause(a.ctx); err != nil {
		return 0, err
	}
	// Adaptors may force streaming for thinking models; inspect the actual wire format.
	if !a.started {
		if contentType := a.header.Get("Content-Type"); contentType != "" {
			a.stream = strings.HasPrefix(contentType, "text/event-stream")
		}
	}
	useful := a.observe(data)
	if a.started && len(a.line) > candidateChatPendingLimit {
		a.cancel(errCandidateChatIncomplete)
		return 0, errCandidateChatIncomplete
	}
	if !a.started {
		if !a.stream && len(a.pending) > 0 {
			useful = gjson.ValidBytes(append(append([]byte(nil), a.pending...), data...))
		}
		if !useful && (len(a.pending)+len(data) > candidateChatPendingLimit || len(a.line) > candidateChatPendingLimit) {
			a.abortBeforeOutput(errCandidateChatPendingLimit)
			return 0, errCandidateChatPendingLimit
		}
		a.pending = append(a.pending, data...)
		if !useful {
			return len(data), nil
		}
		if err := a.release(); err != nil {
			return 0, err
		}
		_, err := a.ResponseWriter.Write(a.pending)
		a.pending = nil
		return len(data), err
	}
	return a.ResponseWriter.Write(data)
}

// The handler still owns switching; the shared attempt budget only lowers its cap.
func CandidateChatSwitchLimit(ctx context.Context, configured int) int {
	if r := CandidateRequestFromContext(ctx); r != nil && r.chatAttempts > 0 {
		return min(configured, candidateChatMaxAttempts-1)
	}
	return configured
}

func candidateChatReplayable(r *CandidateRequest, account *Account, ctx context.Context, body []byte) bool {
	if r == nil || account.Platform != PlatformNewAPI || r.shape != ShapeOpenAIChat || r.websocket || r.continuationAccountID > 0 {
		return false
	}
	target, planned := protocolExecutionTarget(ctx)
	if !planned || target != protocolrouter.ProtocolChatCompletions {
		return false
	}
	if gjson.GetBytes(body, "previous_response_id").Exists() {
		return false
	}
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if tool.Get("type").String() != "function" {
			return false
		}
	}
	for _, modality := range gjson.GetBytes(body, "modalities").Array() {
		if modality.String() != "text" {
			return false
		}
	}
	return true
}

func (s *OpenAIGatewayService) beginCandidateChatAttempt(ctx context.Context, c *gin.Context, account *Account, body []byte) (context.Context, func(*OpenAIForwardResult, error) (*OpenAIForwardResult, error), error) {
	r := CandidateRequestFromContext(ctx)
	if !candidateChatReplayable(r, account, ctx, body) {
		return ctx, nil, nil
	}
	timeout := time.Minute
	if s.cfg != nil && s.cfg.Gateway.NewAPIChatFirstOutputTimeout > 0 {
		timeout = time.Duration(s.cfg.Gateway.NewAPIChatFirstOutputTimeout) * time.Second
	}
	if r.chatDeadline.IsZero() {
		r.chatDeadline = time.Now().Add(candidateChatMaxAttempts * timeout)
	}
	if r.chatAttempts >= candidateChatMaxAttempts || time.Until(r.chatDeadline) <= 0 {
		return ctx, nil, &UpstreamFailoverError{StatusCode: http.StatusGatewayTimeout, Scope: GatewayFailureScopeRequest, ClientStatusCode: http.StatusGatewayTimeout, ClientMessage: "Upstream retry budget exhausted"}
	}
	r.chatAttempts++
	timeout = min(timeout, time.Until(r.chatDeadline))
	a := newCandidateChatAttempt(ctx, c.Writer, gjson.GetBytes(body, "stream").Bool(), timeout)
	request := c.Request
	ctx = context.WithValue(a.ctx, candidateChatAttemptKey{}, a)
	c.Request = c.Request.WithContext(ctx)
	c.Writer = a
	return ctx, func(result *OpenAIForwardResult, err error) (*OpenAIForwardResult, error) {
		defer a.close()
		c.Writer, c.Request = a.ResponseWriter, request
		cause := context.Cause(a.ctx)
		if !a.started {
			if request.Context().Err() != nil {
				return nil, request.Context().Err()
			}
			if cause != nil || err == nil {
				return nil, &UpstreamFailoverError{StatusCode: http.StatusGatewayTimeout, Scope: GatewayFailureScopeAccount, Reason: "first_output_unavailable", ClientStatusCode: http.StatusGatewayTimeout, ClientMessage: "Upstream did not produce a complete response before failover"}
			}
		} else if a.stream && !a.terminal && err == nil {
			err = errCandidateChatIncomplete
		}
		return result, err
	}, nil
}
