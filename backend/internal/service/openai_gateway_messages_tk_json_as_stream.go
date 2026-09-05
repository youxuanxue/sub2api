package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func (s *OpenAIGatewayService) handleAnthropicJSONResponsesAsStream(
	resp *http.Response,
	c *gin.Context,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read json responses body: %w", err)
	}
	if bodyHasSSEFraming(body) {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return s.handleAnthropicStreamingResponse(resp, c, account, originalModel, billingModel, upstreamModel, startTime)
	}

	var finalResponse apicompat.ResponsesResponse
	if err := json.Unmarshal(body, &finalResponse); err != nil || (strings.TrimSpace(finalResponse.ID) == "" && strings.TrimSpace(finalResponse.Status) == "") {
		return nil, s.newOpenAIStreamFailoverError(c, account, false, requestID, body, "OpenAI messages upstream returned non-SSE JSON without a Responses object")
	}

	status := strings.TrimSpace(finalResponse.Status)
	if status == "failed" {
		return s.handleAnthropicJSONResponsesFailed(resp, c, account, requestID, body, &finalResponse)
	}

	state := apicompat.NewResponsesEventToAnthropicState()
	state.Model = originalModel
	if anthropicMessageStartAlreadySent(c) {
		state.MessageStartSent = true
	}
	eventType := "response.completed"
	if status == "incomplete" {
		eventType = "response.incomplete"
	}
	events := apicompat.ResponsesEventToAnthropicEvents(&apicompat.ResponsesStreamEvent{
		Type:     eventType,
		Response: &finalResponse,
	}, state)
	writeStreamHeaders := s.newStreamHeaderWriter(c, resp.Header)
	for _, evt := range events {
		sse, marshalErr := apicompat.ResponsesAnthropicEventToSSE(evt)
		if marshalErr != nil {
			continue
		}
		writeStreamHeaders()
		if _, writeErr := fmt.Fprint(c.Writer, sse); writeErr != nil {
			break
		}
	}
	if len(events) > 0 {
		c.Writer.Flush()
	}

	usage := OpenAIUsage{}
	if finalResponse.Usage != nil {
		usage = copyOpenAIUsageFromResponsesUsage(finalResponse.Usage)
	}
	ms := int(time.Since(startTime).Milliseconds())
	result := &OpenAIForwardResult{
		RequestID:                     requestID,
		ResponseID:                    finalResponse.ID,
		Usage:                         usage,
		Model:                         originalModel,
		BillingModel:                  billingModel,
		UpstreamModel:                 upstreamModel,
		UpstreamResponseModel:         observedUpstreamResponseModel(c),
		UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
		Stream:                        true,
		Duration:                      time.Since(startTime),
		FirstTokenMs:                  &ms,
		StopReason:                    state.StopReason,
		IncompleteReason:              state.IncompleteReason,
		ContentTextLen:                utf8.RuneCountInString(collectAnthropicStreamText(events)),
	}
	logOpenAISuccessMissingUsage(c.Request.Context(), c, account, resp, &usage, eventType, false)
	return result, nil
}

func (s *OpenAIGatewayService) handleAnthropicJSONResponsesFailed(
	resp *http.Response,
	c *gin.Context,
	account *Account,
	requestID string,
	body []byte,
	finalResponse *apicompat.ResponsesResponse,
) (*OpenAIForwardResult, error) {
	payload := body
	if gjson.GetBytes(body, "type").String() != "response.failed" {
		if wrapped, wrapErr := json.Marshal(gin.H{"type": "response.failed", "response": finalResponse}); wrapErr == nil {
			payload = wrapped
		}
	}
	usage := OpenAIUsage{}
	if finalResponse != nil && finalResponse.Usage != nil {
		usage = copyOpenAIUsageFromResponsesUsage(finalResponse.Usage)
	}
	if hit, code, msg := detectOpenAICyberPolicy(payload); hit {
		MarkOpsCyberPolicy(c, CyberPolicyMark{
			Code:           code,
			Message:        msg,
			Body:           truncateString(string(payload), 4096),
			UpstreamStatus: http.StatusOK,
			UpstreamInTok:  usage.InputTokens,
			UpstreamOutTok: usage.OutputTokens,
		})
		clientMsg := msg
		if clientMsg == "" {
			clientMsg = "Request blocked by upstream cyber-security policy"
		}
		if c.Writer.Written() {
			if _, writeErr := fmt.Fprint(c.Writer, buildAnthropicStreamErrorSSE("invalid_request_error", clientMsg)); writeErr == nil {
				c.Writer.Flush()
			}
		} else {
			writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", clientMsg)
		}
		return nil, fmt.Errorf("openai cyber_policy: %s", msg)
	}
	message := extractOpenAISSEErrorMessage(payload)
	if !c.Writer.Written() && openAIStreamFailedEventShouldFailover(payload, message) {
		return nil, s.newOpenAIStreamFailoverError(c, account, false, requestID, payload, message, resp.Header)
	}
	message = s.recordOpenAIStreamUpstreamError(c, account, false, requestID, "http_error", payload, message)
	errStatus, errType := openAIStreamFailedClientResponse(payload, message, "api_error")
	if c.Writer.Written() {
		if _, writeErr := fmt.Fprint(c.Writer, buildAnthropicStreamErrorSSE(errType, message)); writeErr == nil {
			c.Writer.Flush()
		}
	} else {
		writeAnthropicError(c, errStatus, errType, message)
	}
	return nil, fmt.Errorf("upstream response failed: %s", message)
}

func collectAnthropicStreamText(events []apicompat.AnthropicStreamEvent) string {
	var text strings.Builder
	for _, evt := range events {
		if evt.Delta != nil && evt.Delta.Type == "text_delta" {
			_, _ = text.WriteString(evt.Delta.Text)
		}
	}
	return text.String()
}
