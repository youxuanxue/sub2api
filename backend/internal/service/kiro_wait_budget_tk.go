package service

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const kiroWaitBudgetKey = "tk_kiro_wait_budget"
const kiroWaitDeadlineHeader = "X-Tk-Kiro-Wait-Deadline-Ms"
const kiroWaitExhaustedOutcome = "wait_budget_exhausted"
const kiroWaitBudgetClientMessage = "Kiro request waiting budget exhausted"

type kiroWaitBudget struct {
	deadline time.Time
	budget   time.Duration
	high     bool
}

func resolveKiroWaitTimeout(cfg *config.Config, high bool) time.Duration {
	if high {
		if cfg != nil && cfg.Gateway.KiroHighEffortFirstOutputTimeoutSeconds > 0 {
			return time.Duration(cfg.Gateway.KiroHighEffortFirstOutputTimeoutSeconds) * time.Second
		}
		return defaultKiroHighEffortFirstOutputTimeout
	}
	if cfg != nil && cfg.Gateway.KiroFirstOutputTimeoutSeconds > 0 {
		return time.Duration(cfg.Gateway.KiroFirstOutputTimeoutSeconds) * time.Second
	}
	return defaultKiroFirstOutputTimeout
}

// A request chooses once. Retries (including account changes) only get the
// remaining time. A relay deadline can shorten this budget, never extend it.
func getKiroWaitBudget(c *gin.Context, cfg *config.Config, req *kiroproto.ClaudeRequest, start time.Time) kiroWaitBudget {
	if c != nil {
		if prior, ok := c.Get(kiroWaitBudgetKey); ok {
			if budget, valid := prior.(kiroWaitBudget); valid {
				return budget
			}
		}
	}
	if start.IsZero() {
		start = time.Now()
	}
	high := kiroStreamFirstOutputHighEffort(req)
	budget := resolveKiroWaitTimeout(cfg, high)
	b := kiroWaitBudget{deadline: start.Add(budget), budget: budget, high: high}
	if c != nil {
		if c.Request != nil {
			if millis, err := strconv.ParseInt(c.GetHeader(kiroWaitDeadlineHeader), 10, 64); err == nil && millis > 0 {
				if inherited := time.UnixMilli(millis); inherited.Before(b.deadline) {
					b.deadline = inherited
				}
			}
		}
		c.Set(kiroWaitBudgetKey, b)
	}
	return b
}

func armKiroWaitBudget(parent context.Context, c *gin.Context, cfg *config.Config, req *kiroproto.ClaudeRequest, start time.Time) (context.Context, *streamFirstOutputGuard, func(), error) {
	if err := parent.Err(); err != nil {
		return parent, nil, func() {}, err
	}
	b := getKiroWaitBudget(c, cfg, req, start)
	remaining := time.Until(b.deadline)
	if remaining <= 0 {
		return parent, nil, func() {}, kiroWaitBudgetExhausted(c)
	}
	// The outer cancel releases body watchers on every return, including early
	// upstream errors. Commit stops only the timer, preserving caller cancel.
	base, cancel := context.WithCancel(WithHTTPUpstreamProfile(parent, HTTPUpstreamProfileKiro))
	ctx, guard := armStreamFirstOutputGuard(base, remaining)
	return ctx, guard, func() { guard.Commit(); cancel() }, nil
}

func kiroWaitBudgetExhausted(c *gin.Context) error {
	if c != nil && c.Writer != nil && !c.Writer.Written() {
		c.Header(KiroOutcomeHeader, kiroWaitExhaustedOutcome)
	}
	recordOpsStreamFirstOutputTimeout(c)
	return &UpstreamFailoverError{
		StatusCode:               http.StatusGatewayTimeout,
		Scope:                    GatewayFailureScopeRequest,
		Reason:                   "kiro_wait_budget_exhausted",
		ClientStatusCode:         http.StatusGatewayTimeout,
		ClientMessage:            kiroWaitBudgetClientMessage,
		SafeToFailoverAfterWrite: true,
	}
}

func kiroWaitBudgetError(c *gin.Context, guard *streamFirstOutputGuard, guarded, caller context.Context) error {
	if caller != nil && caller.Err() != nil {
		return caller.Err()
	}
	if guard.BudgetFired(guarded, caller) {
		return kiroWaitBudgetExhausted(c)
	}
	return nil
}

// Only Kiro mirror hops emit the deadline. Replace any inbound header so a
// caller cannot buy a longer wait; direct edge requests also clamp it locally.
func setKiroWaitDeadlineHeader(c *gin.Context, req *http.Request, account *Account) {
	req.Header.Del(kiroWaitDeadlineHeader)
	if c == nil || account == nil || !account.IsKiroMirrorStub() {
		return
	}
	if value, ok := c.Get(kiroWaitBudgetKey); ok {
		if budget, valid := value.(kiroWaitBudget); valid {
			req.Header.Set(kiroWaitDeadlineHeader, strconv.FormatInt(budget.deadline.UnixMilli(), 10))
		}
	}
}

// Kiro reasoning stays internal. Synthetic message_start/ping and empty deltas
// must not disarm the prod relay guard before real text/tool output arrives.
func kiroPassthroughUsefulOutput(data string) bool {
	event := gjson.Parse(data)
	switch event.Get("type").String() {
	case "content_block_start":
		return event.Get("content_block.type").String() == "tool_use" || event.Get("content_block.text").String() != ""
	case "content_block_delta":
		return event.Get("delta.text").String() != "" || event.Get("delta.partial_json").String() != ""
	}
	return false
}
