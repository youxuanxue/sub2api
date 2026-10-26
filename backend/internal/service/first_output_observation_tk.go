package service

import (
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const opsFirstOutputObservationKey = "ops_first_output_observation"

// Request-local observations only; none of these fields change the guard.
type opsFirstOutputObservationState struct {
	BudgetMS        int64
	BudgetReason    string
	AttemptCount    int
	account         *Account
	startedAt       time.Time
	timeoutRecorded bool
}

func opsFirstOutputObservation(c *gin.Context) *opsFirstOutputObservationState {
	if c == nil {
		return nil
	}
	if value, ok := c.Get(opsFirstOutputObservationKey); ok {
		if observation, ok := value.(*opsFirstOutputObservationState); ok {
			return observation
		}
	}
	value := &opsFirstOutputObservationState{}
	c.Set(opsFirstOutputObservationKey, value)
	return value
}

// Attempt means one selected-protocol execution, including same-account replay.
// Multiple error events within an execution do not increase this count.
func BeginOpsFirstOutputAttempt(c *gin.Context) {
	if observation := opsFirstOutputObservation(c); observation != nil {
		observation.AttemptCount++
		observation.BudgetMS = 0
		observation.BudgetReason = ""
		observation.account = nil
		observation.timeoutRecorded = false
	}
}

func OpsFirstOutputAttemptCount(c *gin.Context) int {
	if observation := opsFirstOutputObservation(c); observation != nil {
		return observation.AttemptCount
	}
	return 0
}

func RecordOpsFirstOutputBudget(c *gin.Context, account *Account, budget time.Duration, reason string) {
	if c == nil || account == nil || budget <= 0 {
		return
	}
	observation := opsFirstOutputObservation(c)
	if observation.BudgetMS == budget.Milliseconds() && observation.BudgetReason == reason && observation.account == account {
		return
	}
	observation.BudgetMS = budget.Milliseconds()
	observation.BudgetReason = reason
	observation.account = account
	observation.startedAt = time.Now()
	requestID := ""
	if c.Request != nil {
		requestID, _ = c.Request.Context().Value(ctxkey.RequestID).(string)
	}
	model, _ := c.Get(OpsModelKey)
	logger.L().Info("gateway.first_output_budget",
		zap.String("component", "audit.first_output"),
		zap.String("request_id", requestID), zap.Any("model", model),
		zap.String("platform", account.Platform), zap.Int64("account_id", account.ID),
		zap.Int64("budget_ms", observation.BudgetMS), zap.String("budget_reason", reason),
		zap.Int("attempt_count", observation.AttemptCount))
}

func recordOpsStreamFirstOutputTimeout(c *gin.Context) {
	observation := opsFirstOutputObservation(c)
	if observation == nil || observation.account == nil || observation.timeoutRecorded {
		return
	}
	observation.timeoutRecorded = true
	account := observation.account
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Kind: "first_output_timeout", Reason: "first_output_unavailable",
		Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
		ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account),
		UpstreamStatusCode: 504, BudgetMS: observation.BudgetMS, BudgetReason: observation.BudgetReason,
		Message: "Upstream produced no useful output before the deadline",
		Detail:  fmt.Sprintf("phase=semantic_output elapsed_ms=%d timeout_ms=%d", time.Since(observation.startedAt).Milliseconds(), observation.BudgetMS),
	})
}

func opsFirstOutputBudgetReason(c *gin.Context) string {
	if observation := opsFirstOutputObservation(c); observation != nil {
		return observation.BudgetReason
	}
	return ""
}

func streamFirstOutputBudgetReason(high bool) string {
	if high {
		return "high_effort"
	}
	return "default"
}

// firstOutputBudgetReason selects the first-output budget *class*
// (default / high_effort / compact / large_body). Shared selection policy for
// OpenAI-native streams and NewAPI Chat replay (#2610 strategy); each platform
// still resolves the numeric timeout via its own knobs
// (openAIFirstOutputTimeout vs newAPIChatFirstOutputTimeoutWithEffort).
func firstOutputBudgetReason(c *gin.Context, body []byte, effort string) string {
	if isOpenAIResponsesCompactPath(c) || HasCompactionTriggerInInput(body) {
		return "compact"
	}
	if len(body) >= openAIFirstOutputLargeBodyBytes {
		return "large_body"
	}
	if firstOutputHighEffortModel(gjson.GetBytes(body, "model").String()) {
		return "high_effort"
	}
	return streamFirstOutputBudgetReason(streamFirstOutputHighEffort(effort))
}

// firstOutputBudgetForcesHigh reports whether a budget-reason class must use
// the platform high-latency first-output timeout (compact / large body /
// explicit or model-family high reasoning).
func firstOutputBudgetForcesHigh(reason string) bool {
	switch reason {
	case "compact", "large_body", "high_effort":
		return true
	default:
		return false
	}
}

// FinalizeOpsFirstOutputEvaluation annotates only actual timeout events. A
// successful retry is counted even though it produces no upstream error event.
func FinalizeOpsFirstOutputEvaluation(c *gin.Context, outcome string) bool {
	if c == nil {
		return false
	}
	value, _ := c.Get(OpsUpstreamErrorsKey)
	events, _ := value.([]*OpsUpstreamErrorEvent)
	observation := opsFirstOutputObservation(c)
	found := false
	for _, event := range events {
		if event == nil || event.Kind != "first_output_timeout" {
			continue
		}
		event.AttemptCount = observation.AttemptCount
		event.TimeoutOutcome = outcome
		found = true
	}
	return found
}
