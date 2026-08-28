package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"

	"github.com/gin-gonic/gin"
)

const accountTestSuppressErrorLogContextKey = "account_test_suppress_error_log"

func (s *AccountTestService) ProbeSupplierModel(ctx context.Context, input SupplierProbeInput) SupplierProbeResult {
	baseResult := SupplierProbeResult{
		ClientModelID: strings.TrimSpace(input.ClientModelID), UpstreamModelID: strings.TrimSpace(input.UpstreamModelID),
	}
	if s == nil {
		baseResult.Status = SupplierProbeStatusFailed
		baseResult.Detail = "account test service unavailable"
		return baseResult
	}
	if input.Account == nil || baseResult.ClientModelID == "" || baseResult.UpstreamModelID == "" {
		baseResult.Status = SupplierProbeStatusFailed
		baseResult.Detail = supplierProbeSafeDetail(SupplierProbeStatusFailed)
		return baseResult
	}
	if isFMGoSeedanceSupplierProbe(baseResult.ClientModelID, baseResult.UpstreamModelID) {
		baseResult.Status = SupplierProbeStatusProtocolUnsupported
		baseResult.Detail = supplierProbeSafeDetail(SupplierProbeStatusProtocolUnsupported)
		return baseResult
	}
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest("POST", "/internal/supplier-probe", nil).WithContext(ctx)
	ginContext.Set(accountTestSuppressErrorLogContextKey, true)
	err := s.testAccountConnectionWithAccount(
		ginContext,
		cloneSupplierProjectionAccount(input.Account),
		baseResult.ClientModelID,
		"hi",
		AccountTestModeDefault,
	)
	result := supplierProbeResultFromSSE(recorder.Body.String(), err)
	result.ClientModelID = baseResult.ClientModelID
	result.UpstreamModelID = baseResult.UpstreamModelID
	if result.Status == SupplierProbeStatusPassed && result.Protocol == "account_test" && input.Account.Platform == PlatformNewAPI {
		result.Protocol = "openai_chat_completions"
	}
	return result
}

func isFMGoSeedanceSupplierProbe(clientModelID, upstreamModelID string) bool {
	return strings.TrimSpace(clientModelID) == "doubao-seedance-2-0-260128" &&
		strings.TrimSpace(upstreamModelID) == "feimiao-seedance-2-0-260128"
}

func supplierProbeResultFromSSE(body string, probeErr error) SupplierProbeResult {
	events := supplierProbeEventsFromSSE(body)
	var completion *TestEvent
	classifiers := make([]string, 0, len(events)+1)
	for index := range events {
		event := &events[index]
		if event.Type == "test_complete" {
			completion = event
		}
		if event.Error != "" {
			classifiers = append(classifiers, event.Error)
		}
		if event.Code != "" {
			classifiers = append(classifiers, event.Code)
		}
		if event.Status != "" {
			classifiers = append(classifiers, event.Status)
		}
	}
	if probeErr == nil && completion != nil && completion.Success {
		protocol := supplierProbeProtocolFromEvents(events)
		if protocol != "account_test" && protocol != "openai_chat_completions" {
			return SupplierProbeResult{
				Status: SupplierProbeStatusProtocolUnsupported, Protocol: protocol,
				Detail: supplierProbeSafeDetail(SupplierProbeStatusProtocolUnsupported),
			}
		}
		return SupplierProbeResult{Status: SupplierProbeStatusPassed, Protocol: protocol}
	}
	if probeErr != nil {
		classifiers = append(classifiers, probeErr.Error())
	}
	combined := strings.ToLower(strings.Join(classifiers, " "))
	switch {
	case strings.Contains(combined, "401"), strings.Contains(combined, "403"), strings.Contains(combined, "unauthorized"), strings.Contains(combined, "forbidden"):
		return SupplierProbeResult{Status: SupplierProbeStatusAuthFailed, Detail: supplierProbeSafeDetail(SupplierProbeStatusAuthFailed)}
	case strings.Contains(combined, "model not found"), strings.Contains(combined, "model_not_found"), strings.Contains(combined, "unknown model"):
		return SupplierProbeResult{Status: SupplierProbeStatusModelUnsupported, Detail: supplierProbeSafeDetail(SupplierProbeStatusModelUnsupported)}
	case strings.Contains(combined, "404"), strings.Contains(combined, "unsupported endpoint"), strings.Contains(combined, "invalid request format"), strings.Contains(combined, "task protocol"):
		return SupplierProbeResult{Status: SupplierProbeStatusProtocolUnsupported, Detail: supplierProbeSafeDetail(SupplierProbeStatusProtocolUnsupported)}
	default:
		return SupplierProbeResult{Status: SupplierProbeStatusFailed, Detail: supplierProbeSafeDetail(SupplierProbeStatusFailed)}
	}
}

func supplierProbeEventsFromSSE(body string) []TestEvent {
	lines := strings.Split(body, "\n")
	events := make([]TestEvent, 0, len(lines)/2)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var event TestEvent
		if payload == "" || json.Unmarshal([]byte(payload), &event) != nil {
			continue
		}
		events = append(events, event)
	}
	return events
}

func supplierProbeProtocolFromEvents(events []TestEvent) string {
	evidence := make([]string, 0, len(events)*3)
	for _, event := range events {
		evidence = append(evidence, event.Type, event.Text)
		if event.Data != nil {
			encoded, _ := json.Marshal(event.Data)
			evidence = append(evidence, string(encoded))
		}
	}
	body := strings.ToLower(strings.Join(evidence, " "))
	switch {
	case strings.Contains(body, "chat.completion"):
		return "openai_chat_completions"
	case strings.Contains(body, "response.completed"):
		return "openai_responses"
	case strings.Contains(body, "message_start"):
		return "anthropic_messages"
	default:
		return "account_test"
	}
}

func supplierProbeSafeDetail(status SupplierProbeStatus) string {
	switch status {
	case SupplierProbeStatusPassed:
		return ""
	case SupplierProbeStatusAuthFailed:
		return "upstream authentication failed"
	case SupplierProbeStatusModelUnsupported:
		return "upstream model unsupported"
	case SupplierProbeStatusProtocolUnsupported:
		return "supplier protocol unsupported"
	default:
		return "upstream probe failed"
	}
}
