package service

import (
	"context"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
	"github.com/gin-gonic/gin"
)

const kiroWaitBudgetKey = "tk_kiro_wait_budget"
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
