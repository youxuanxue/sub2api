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
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	// First-output defaults (docs/approved/candidate-eligibility-ssot.md).
	// first-output-timeout-ssot:begin
	defaultStreamFirstOutputTimeout           = 120 * time.Second
	defaultStreamFirstOutputHighEffortTimeout = 600 * time.Second
	defaultKiroFirstOutputTimeout             = 120 * time.Second
	defaultKiroHighEffortFirstOutputTimeout   = 600 * time.Second
	defaultNewAPIChatNonstreamFirstOutput     = 600 * time.Second
	// first-output-timeout-ssot:end
)

// ResolveGrokFirstByteHeaderTimeout is the Grok transport ResponseHeaderTimeout
// aligned to the universal stream first-useful-output SSOT. Explicit
// gateway.grok_response_header_timeout (>0) overrides; zero inherits
// stream_first_output_timeout_seconds / code default 120s.
func ResolveGrokFirstByteHeaderTimeout(cfg *config.Config) time.Duration {
	if cfg != nil && cfg.Gateway.GrokResponseHeaderTimeout > 0 {
		return time.Duration(cfg.Gateway.GrokResponseHeaderTimeout) * time.Second
	}
	return resolveStreamFirstOutputTimeout(cfg, false)
}

// resolveStreamFirstOutputTimeout is the cross-platform per-attempt budget until
// useful first output commits (stops silent failover). highEffort selects the
// high-reasoning override. This is intentionally not identical to metered TTFT.
func resolveStreamFirstOutputTimeout(cfg *config.Config, highEffort bool) time.Duration {
	if highEffort {
		if cfg != nil && cfg.Gateway.StreamFirstOutputHighEffortTimeoutSeconds > 0 {
			return time.Duration(cfg.Gateway.StreamFirstOutputHighEffortTimeoutSeconds) * time.Second
		}
		// Do not fall back to openai_high_effort_* here: that knob is
		// OpenAI-native only (see openAIFirstOutputTimeout). Universal
		// Anthropic/Gemini high-effort must inherit the stream SSOT
		// code default when the stream high-effort key is unset/zero.
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

// firstOutputHighEffortModel keeps known long-thinking model families in the
// complex tier even when the client omits an explicit effort value.
func firstOutputHighEffortModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(claude.NormalizeModelID(model)))
	normalized = strings.TrimPrefix(normalized, "anthropic.")
	for _, family := range []string{"claude-opus", "claude-fable"} {
		if normalized == family || strings.HasPrefix(normalized, family+"-") || strings.HasPrefix(normalized, family+".") {
			return true
		}
	}
	return isOpenAIGPT6AstraModel(normalized) ||
		isOpenAIGPT56Model(normalized) ||
		openai.IsGPT6SolOrLunaModelSpelling(normalized) ||
		openai.IsGPT61SolModelSpelling(normalized)
}

// newAPIChatFirstOutputTimeoutWithEffort is the per-attempt useful-first-output
// wait for replayable NewAPI Chat. Streaming inherits the universal stream SSOT
// (legacy NewAPIChatFirstOutputTimeout overrides when >0). Non-stream keeps its
// own whole-body budget (default 600s).
//
// Callers decide highEffort via the shared first-output promotion policy
// (firstOutputBudgetReason / firstOutputBudgetForcesHigh): compact,
// large body, or high reasoning — same classes as OpenAI #2610; only the
// numeric knobs differ (NewAPI high-effort override vs OpenAI high-effort).
func newAPIChatFirstOutputTimeoutWithEffort(cfg *config.Config, stream bool, highEffort bool) time.Duration {
	if !stream {
		if cfg != nil && cfg.Gateway.NewAPIChatNonstreamFirstOutputTimeout > 0 {
			return time.Duration(cfg.Gateway.NewAPIChatNonstreamFirstOutputTimeout) * time.Second
		}
		return defaultNewAPIChatNonstreamFirstOutput
	}
	// Explicit legacy/platform override wins over the universal stream default
	// for standard effort; high effort prefers its own NewAPI override and
	// otherwise falls back to the high-effort SSOT.
	//
	// The stream high-effort SSOT (600s) is sized for long-thinking families
	// (claude-opus / gpt-6 class) that really do stall before the first token.
	// NewAPI serves domestic models, where cn-prod measurement shows the
	// opposite shape: high/max TTFT P50 6909ms against 4219ms for standard
	// effort, while post-first-token generation is *shorter* (5617ms vs
	// 11919ms). Inheriting 600s there leaves the slowest third of streaming
	// traffic with no pre-output replay at all, so this platform gets its own
	// knob. Zero keeps the historical inherit.
	if highEffort {
		if cfg != nil && cfg.Gateway.NewAPIChatHighEffortFirstOutputTimeout > 0 {
			return time.Duration(cfg.Gateway.NewAPIChatHighEffortFirstOutputTimeout) * time.Second
		}
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
	decisionMu     sync.Mutex // Commit and budget cancellation must have one order.
	budgetCanceled atomic.Bool
	cancel         context.CancelFunc
	timer          *time.Timer
	committed      atomic.Bool
	stopOnce       sync.Once
	finished       chan struct{} // closed on Commit or budget fire
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
		g.decisionMu.Lock()
		if !g.committed.Load() && g.cancel != nil {
			g.budgetCanceled.Store(true)
			g.cancel()
		}
		g.decisionMu.Unlock()
		g.finish()
	})
	return ctx, g
}

func (g *streamFirstOutputGuard) finish() {
	if g == nil {
		return
	}
	g.stopOnce.Do(func() {
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
	g.decisionMu.Lock()
	g.committed.Store(true)
	// Only the caller, after arm returns, may read timer. The timer callback
	// can run before AfterFunc's result is assigned to g.timer.
	if g.timer != nil {
		g.timer.Stop()
	}
	g.decisionMu.Unlock()
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
		// A budget that won before late output must still unblock this body.
		// Committed remains true for replay safety, independently of that result.
		if g.budgetCanceled.Load() {
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

// firstOutputUnavailableClientErrorType is required alongside ClientStatusCode:
// handleFailoverExhausted only honors ClientStatusCode when the error type is
// non-empty, and otherwise maps 504 down to a generic 502 (openai_gateway_handler.go
// mapUpstreamError). Without it, "upstream produced no useful output" was
// indistinguishable from a real upstream 502 for both clients and our 504 alerting.
const firstOutputUnavailableClientErrorType = "timeout_error"

func streamFirstOutputFailoverError() error {
	return &UpstreamFailoverError{
		StatusCode:       http.StatusGatewayTimeout,
		Scope:            GatewayFailureScopeAccount,
		Reason:           "first_output_unavailable",
		ClientStatusCode: http.StatusGatewayTimeout,
		ClientErrorType:  firstOutputUnavailableClientErrorType,
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
		ClientErrorType:          firstOutputUnavailableClientErrorType,
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
	recordOpsStreamFirstOutputTimeout(c)
	if c != nil && c.Writer != nil && c.Writer.Size() >= 0 {
		return streamFirstOutputFailoverErrorAfterKeepalive()
	}
	return streamFirstOutputFailoverError()
}

// kiroStreamFirstOutputHighEffort selects the high tier for Fable/Opus families
// and explicit Claude Messages thinking / high-effort hints.
func kiroStreamFirstOutputHighEffort(req *kiroproto.ClaudeRequest) bool {
	if req == nil {
		return false
	}
	if firstOutputHighEffortModel(req.Model) {
		return true
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
	}
	return false
}

// anthropicBodyStreamFirstOutputHighEffort maps Anthropic Messages JSON onto the
// high-effort first-output budget (output_config.effort / thinking).
func anthropicBodyStreamFirstOutputHighEffort(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if firstOutputHighEffortModel(gjson.GetBytes(body, "model").String()) {
		return true
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
