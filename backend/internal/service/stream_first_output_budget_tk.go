package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	// Universal stream first-useful-output defaults (docs/approved/candidate-eligibility-ssot.md).
	defaultStreamFirstOutputTimeout           = 30 * time.Second
	defaultStreamFirstOutputHighEffortTimeout = 60 * time.Second
	defaultNewAPIChatNonstreamFirstOutput     = 300 * time.Second
)

// resolveStreamFirstOutputTimeout is the cross-platform per-attempt budget until
// useful first output commits (stops silent failover). highEffort selects the
// high-reasoning override. This is intentionally not identical to metered TTFT.
func resolveStreamFirstOutputTimeout(cfg *config.Config, highEffort bool) time.Duration {
	if highEffort {
		if cfg != nil && cfg.Gateway.StreamFirstOutputHighEffortTimeoutSeconds > 0 {
			return time.Duration(cfg.Gateway.StreamFirstOutputHighEffortTimeoutSeconds) * time.Second
		}
		if cfg != nil && cfg.Gateway.OpenAIHighEffortFirstOutputTimeoutSeconds > 0 {
			return time.Duration(cfg.Gateway.OpenAIHighEffortFirstOutputTimeoutSeconds) * time.Second
		}
		return defaultStreamFirstOutputHighEffortTimeout
	}
	if cfg != nil && cfg.Gateway.StreamFirstOutputTimeoutSeconds > 0 {
		return time.Duration(cfg.Gateway.StreamFirstOutputTimeoutSeconds) * time.Second
	}
	return defaultStreamFirstOutputTimeout
}

// streamFirstOutputHighEffort reports whether the request asks for high reasoning
// effort spellings that use the longer first-output budget.
func streamFirstOutputHighEffort(reasoningEffort string) bool {
	switch strings.ToLower(strings.TrimSpace(reasoningEffort)) {
	case "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

// newAPIChatFirstOutputTimeoutWithEffort is the per-attempt useful-first-output
// wait for replayable NewAPI Chat. Streaming inherits the universal stream SSOT
// (legacy NewAPIChatFirstOutputTimeout overrides when >0). Non-stream keeps its
// own whole-body budget (default 300s).
func newAPIChatFirstOutputTimeoutWithEffort(cfg *config.Config, stream bool, highEffort bool) time.Duration {
	if !stream {
		if cfg != nil && cfg.Gateway.NewAPIChatNonstreamFirstOutputTimeout > 0 {
			return time.Duration(cfg.Gateway.NewAPIChatNonstreamFirstOutputTimeout) * time.Second
		}
		return defaultNewAPIChatNonstreamFirstOutput
	}
	// Explicit legacy/platform override wins over the universal stream default
	// for standard effort; high effort always prefers the high-effort SSOT.
	if highEffort {
		return resolveStreamFirstOutputTimeout(cfg, true)
	}
	if cfg != nil && cfg.Gateway.NewAPIChatFirstOutputTimeout > 0 {
		return time.Duration(cfg.Gateway.NewAPIChatFirstOutputTimeout) * time.Second
	}
	return resolveStreamFirstOutputTimeout(cfg, false)
}

// streamFirstOutputGuard cancels a child context if useful first output is not
// committed before the budget. After Commit(), the timer is disarmed and the
// child context stays alive for the rest of the stream (parent cancellation
// still applies).
type streamFirstOutputGuard struct {
	cancel    context.CancelFunc
	timer     *time.Timer
	committed atomic.Bool
	stopOnce  sync.Once
	finished  chan struct{} // closed on Commit or budget fire
}

func armStreamFirstOutputGuard(parent context.Context, timeout time.Duration) (context.Context, *streamFirstOutputGuard) {
	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		return parent, &streamFirstOutputGuard{}
	}
	ctx, cancel := context.WithCancel(parent)
	g := &streamFirstOutputGuard{cancel: cancel, finished: make(chan struct{})}
	g.timer = time.AfterFunc(timeout, func() {
		if !g.committed.Load() && g.cancel != nil {
			g.cancel()
		}
		g.finish()
	})
	return ctx, g
}

func (g *streamFirstOutputGuard) finish() {
	if g == nil {
		return
	}
	g.stopOnce.Do(func() {
		if g.timer != nil {
			g.timer.Stop()
		}
		if g.finished != nil {
			close(g.finished)
		}
	})
}

// Commit marks useful first output and disarms the budget timer.
func (g *streamFirstOutputGuard) Commit() {
	if g == nil {
		return
	}
	g.committed.Store(true)
	g.finish()
}

// WatchClose closes body if the budget fires before Commit, so blocking body
// reads unblock and the caller can return streamFirstOutputFailoverError.
func (g *streamFirstOutputGuard) WatchClose(body io.Closer) {
	if g == nil || body == nil || g.finished == nil {
		return
	}
	go func() {
		<-g.finished
		if !g.Committed() {
			_ = body.Close()
		}
	}()
}

// Committed reports whether useful first output already arrived.
func (g *streamFirstOutputGuard) Committed() bool {
	return g != nil && g.committed.Load()
}

// BudgetFired reports a pre-commit cancel on the guarded ctx while the inbound
// caller is still alive (distinguishes first-output budget from client cancel).
func (g *streamFirstOutputGuard) BudgetFired(guarded, caller context.Context) bool {
	if g == nil || g.Committed() {
		return false
	}
	return guarded != nil && guarded.Err() != nil && (caller == nil || caller.Err() == nil)
}

func streamFirstOutputFailoverError() error {
	return &UpstreamFailoverError{
		StatusCode:       http.StatusGatewayTimeout,
		Scope:            GatewayFailureScopeAccount,
		Reason:           "first_output_unavailable",
		ClientStatusCode: http.StatusGatewayTimeout,
		ClientMessage:    "Upstream did not produce a complete response before failover",
	}
}

// streamFirstOutputFailoverErrorAfterKeepalive allows account switch after only
// non-semantic SSE keepalives were written (SafeToFailoverAfterWrite).
func streamFirstOutputFailoverErrorAfterKeepalive() error {
	return &UpstreamFailoverError{
		StatusCode:               http.StatusGatewayTimeout,
		Scope:                    GatewayFailureScopeAccount,
		Reason:                   "first_output_unavailable",
		ClientStatusCode:         http.StatusGatewayTimeout,
		ClientMessage:            "Upstream did not produce a complete response before failover",
		SafeToFailoverAfterWrite: true,
	}
}

// streamFirstOutputFailoverIfBudgetFired returns a failover error when the
// useful-first-output budget cancelled the attempt. Gin Size()>=0 means bytes
// already left for the client (typically header-wait / in-stream keepalives);
// those paths set SafeToFailoverAfterWrite so the handler Size gate can still
// switch accounts.
func streamFirstOutputFailoverIfBudgetFired(c *gin.Context, g *streamFirstOutputGuard, guarded, caller context.Context) error {
	if !g.BudgetFired(guarded, caller) {
		return nil
	}
	if c != nil && c.Writer != nil && c.Writer.Size() >= 0 {
		return streamFirstOutputFailoverErrorAfterKeepalive()
	}
	return streamFirstOutputFailoverError()
}

// kiroStreamFirstOutputHighEffort maps Claude Messages thinking / effort hints
// onto the high-effort first-output budget.
func kiroStreamFirstOutputHighEffort(req *kiroproto.ClaudeRequest) bool {
	if req == nil {
		return false
	}
	if req.OutputConfig != nil && streamFirstOutputHighEffort(req.OutputConfig.Effort) {
		return true
	}
	// Adaptive / enabled thinking is the long-TTFT path on Opus-class models.
	if req.Thinking != nil {
		switch strings.ToLower(strings.TrimSpace(req.Thinking.Type)) {
		case "enabled", "adaptive":
			return true
		}
		if req.Thinking.BudgetTokens >= 8000 {
			return true
		}
	}
	return false
}

// anthropicBodyStreamFirstOutputHighEffort maps Anthropic Messages JSON onto the
// high-effort first-output budget (output_config.effort / thinking).
func anthropicBodyStreamFirstOutputHighEffort(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if streamFirstOutputHighEffort(gjson.GetBytes(body, "output_config.effort").String()) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String())) {
	case "enabled", "adaptive":
		return true
	}
	return gjson.GetBytes(body, "thinking.budget_tokens").Int() >= 8000
}

// geminiBodyStreamFirstOutputHighEffort maps Gemini generationConfig thinking
// knobs onto the high-effort first-output budget.
func geminiBodyStreamFirstOutputHighEffort(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if gjson.GetBytes(body, "generationConfig.thinkingConfig.thinkingBudget").Int() >= 8000 {
		return true
	}
	level := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "generationConfig.thinkingConfig.thinkingLevel").String()))
	return level == "high"
}
