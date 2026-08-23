package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// tkCodexFailureStreamState tracks Codex failure-terminal semantics for Responses
// SSE streams (bare error before authoritative response.failed).
type tkCodexFailureStreamState struct {
	enabled                            bool
	sawBareError                       bool
	sawResponseFailed                  bool
	bareErrorPayload                   []byte
	suppressCurrentEvent               bool
	bareErrorAccountSideEffectsPending bool
	terminalFailurePending             bool
}

func newTkCodexFailureStreamStateForReasoning(account *Account) *tkCodexFailureStreamState {
	return &tkCodexFailureStreamState{
		enabled: account != nil && account.IsOpenAIOAuthLike(),
	}
}

func newTkCodexFailureStreamStateForPassthrough(account *Account) *tkCodexFailureStreamState {
	return &tkCodexFailureStreamState{
		enabled: account != nil && account.Platform == PlatformOpenAI,
	}
}

func (st *tkCodexFailureStreamState) onSSEEventLine(eventType string) {
	if !st.enabled {
		return
	}
	eventType = strings.TrimSpace(eventType)
	st.suppressCurrentEvent = eventType == "error" ||
		(st.sawBareError && !st.sawResponseFailed && eventType != "response.failed")
}

func (st *tkCodexFailureStreamState) onSuccessfulTerminalWhileBareError(eventType string) bool {
	if !st.enabled || !st.sawBareError || st.sawResponseFailed {
		return false
	}
	if eventType != "response.completed" && eventType != "response.done" {
		return false
	}
	st.sawBareError = false
	st.terminalFailurePending = false
	st.suppressCurrentEvent = false
	st.bareErrorPayload = nil
	st.bareErrorAccountSideEffectsPending = false
	return true
}

func (st *tkCodexFailureStreamState) afterPayloadRestore(eventType string) {
	if st.enabled && st.sawBareError && !st.sawResponseFailed && eventType != "response.failed" {
		st.suppressCurrentEvent = true
	}
}

func (st *tkCodexFailureStreamState) onFailureEvent(eventType string, dataBytes []byte) {
	if !st.enabled {
		return
	}
	switch eventType {
	case "error":
		st.sawBareError = true
		st.bareErrorPayload = append(st.bareErrorPayload[:0], dataBytes...)
		st.suppressCurrentEvent = true
	case "response.failed":
		st.sawResponseFailed = true
		st.suppressCurrentEvent = false
	}
}

func (st *tkCodexFailureStreamState) terminalFailurePendingFor(eventType string) bool {
	return !st.enabled || eventType == "response.failed"
}

func (st *tkCodexFailureStreamState) onOutputStartedFailureSideEffects(
	s *OpenAIGatewayService,
	c *gin.Context,
	account *Account,
	eventType string,
	dataBytes []byte,
	failedMessage string,
	respHeader http.Header,
) {
	if st.enabled && eventType == "error" {
		st.bareErrorAccountSideEffectsPending = true
		return
	}
	s.handleOpenAIStreamTerminalAccountSideEffects(c, account, dataBytes, failedMessage, respHeader)
	st.bareErrorAccountSideEffectsPending = false
}

func (st *tkCodexFailureStreamState) shouldFinalizeBareError(eventsLen int) bool {
	return st.enabled && st.sawBareError && !st.sawResponseFailed && eventsLen == 0
}

func (st *tkCodexFailureStreamState) finalizeBareErrorAtStreamEnd(
	s *OpenAIGatewayService,
	c *gin.Context,
	account *Account,
	respHeader http.Header,
	failedMessage string,
) {
	if st.enabled && st.sawBareError && !st.sawResponseFailed && st.bareErrorAccountSideEffectsPending {
		s.handleOpenAIStreamTerminalAccountSideEffects(c, account, st.bareErrorPayload, failedMessage, respHeader)
		st.bareErrorAccountSideEffectsPending = false
	}
}

type tkCodexStreamFailureInput struct {
	s                        *OpenAIGatewayService
	c                        *gin.Context
	account                  *Account
	codex                    *tkCodexFailureStreamState
	ctx                      context.Context
	upstreamRequestID        string
	respHeader               http.Header
	usage                    *OpenAIUsage
	firstTokenMs             **int
	clientOutputStarted      bool
	dataBytes                []byte
	eventType                string
	passthrough              bool
	failoverOnlyBeforeOutput bool
}

type tkCodexStreamFailureResult struct {
	cyberHit                    bool
	forceFlushFailedEvent       bool
	sawFailedEvent              bool
	failedMessage               string
	streamEarlyErr              error
	logCapacityFailoverSuppress bool
}

func (in tkCodexStreamFailureInput) handleFailureEvent(
	capacityFailoverSuppressedLogged bool,
) (tkCodexStreamFailureResult, bool) {
	var out tkCodexStreamFailureResult
	if in.eventType != "response.failed" && in.eventType != "error" {
		return out, false
	}
	in.codex.onFailureEvent(in.eventType, in.dataBytes)
	out.failedMessage = extractOpenAISSEErrorMessage(in.dataBytes)
	if out.failedMessage == "" {
		out.failedMessage = "Upstream response failed"
	}
	in.s.parseSSEUsageBytesWithType(in.dataBytes, in.eventType, in.usage)
	if hit, code, msg := detectOpenAICyberPolicy(in.dataBytes); hit {
		out.cyberHit = true
		MarkOpsCyberPolicy(in.c, CyberPolicyMark{
			Code:           code,
			Message:        msg,
			Body:           truncateString(string(in.dataBytes), 4096),
			UpstreamStatus: http.StatusOK,
			UpstreamInTok:  in.usage.InputTokens,
			UpstreamOutTok: in.usage.OutputTokens,
		})
	}
	outputStarted := openAIStreamClientOutputStarted(in.c, in.clientOutputStarted)
	if !outputStarted && !out.cyberHit {
		if compactErr := newOpenAICompactFallbackSignal(in.c, in.dataBytes, out.failedMessage); compactErr != nil {
			out.sawFailedEvent = true
			out.streamEarlyErr = compactErr
			return out, true
		}
	}
	if outputStarted && !out.cyberHit {
		in.codex.onOutputStartedFailureSideEffects(in.s, in.c, in.account, in.eventType, in.dataBytes, out.failedMessage, in.respHeader)
	}
	considerFailover := !outputStarted || (!in.failoverOnlyBeforeOutput && in.eventType == "response.failed")
	if considerFailover {
		shouldFailover := false
		if !out.cyberHit {
			if in.eventType == "error" {
				shouldFailover = openAIStreamErrorEventShouldFailover(in.dataBytes, out.failedMessage)
			} else {
				shouldFailover = openAIStreamFailedEventShouldFailover(in.dataBytes, out.failedMessage)
			}
		}
		if !openAIStreamFailoverBlockedByClientOutput(*in.firstTokenMs) && shouldFailover {
			out.sawFailedEvent = true
			out.streamEarlyErr = in.s.newOpenAIStreamFailoverError(in.c, in.account, in.passthrough, in.upstreamRequestID, in.dataBytes, out.failedMessage, in.respHeader)
			return out, true
		}
		if !out.cyberHit && !in.codex.sawBareError {
			if status, errType, errMsg, matched := applyOpenAIStreamFailedErrorPassthroughRule(in.c, in.account.Platform, in.dataBytes, out.failedMessage); matched {
				out.sawFailedEvent = true
				in.s.recordOpenAIStreamUpstreamError(in.c, in.account, in.passthrough, in.upstreamRequestID, "http_error", in.dataBytes, out.failedMessage)
				MarkResponseCommitted(in.c)
				in.c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				in.c.JSON(status, gin.H{
					"error": gin.H{
						"type":    errType,
						"message": errMsg,
					},
				})
				out.streamEarlyErr = fmt.Errorf("upstream response failed: passthrough rule matched message=%s", errMsg)
				return out, true
			}
		}
	}
	if !capacityFailoverSuppressedLogged && in.account != nil && in.account.Platform == PlatformOpenAI &&
		(in.eventType == "error" || in.eventType == "response.failed") &&
		openAIStreamClientOutputStarted(in.c, in.clientOutputStarted) &&
		isOpenAIUpstreamCapacityShedEvent(in.dataBytes) {
		out.logCapacityFailoverSuppress = true
	}
	out.forceFlushFailedEvent = true
	out.sawFailedEvent = true
	in.codex.terminalFailurePending = in.codex.terminalFailurePendingFor(in.eventType)
	return out, true
}

func newTkReasoningCodexStreamState(account *Account) *tkCodexFailureStreamState {
	return newTkCodexFailureStreamStateForReasoning(account)
}
