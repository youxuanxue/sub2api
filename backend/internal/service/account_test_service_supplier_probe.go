package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
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
	if supplierAccountUsesVideoProbe(input.Account) {
		return s.probeSupplierVideoModel(ctx, input, baseResult)
	}
	if supplierAccountUsesAnthropicMessagesProbe(input.Account) {
		return s.probeSupplierAnthropicMessagesModel(ctx, input, baseResult)
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

func supplierAccountUsesVideoProbe(account *Account) bool {
	return account != nil && account.ChannelType == newapiconstant.ChannelTypeDoubaoVideo
}

func supplierAccountUsesAnthropicMessagesProbe(account *Account) bool {
	return account != nil && account.ChannelType == newapiconstant.ChannelTypeAnthropic
}

func (s *AccountTestService) probeSupplierAnthropicMessagesModel(
	ctx context.Context,
	input SupplierProbeInput,
	baseResult SupplierProbeResult,
) SupplierProbeResult {
	account := input.Account
	baseURL := strings.TrimRight(strings.TrimSpace(account.GetBaseURL()), "/")
	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	if baseURL == "" || apiKey == "" {
		baseResult.Status = SupplierProbeStatusFailed
		baseResult.Detail = supplierProbeSafeDetail(SupplierProbeStatusFailed)
		return baseResult
	}
	messagesURL := supplierAnthropicMessagesProbeURL(baseURL)
	body, err := json.Marshal(map[string]any{
		"model":      baseResult.UpstreamModelID,
		"max_tokens": 8,
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
		"stream": false,
	})
	if err != nil {
		baseResult.Status = SupplierProbeStatusFailed
		baseResult.Detail = supplierProbeSafeDetail(SupplierProbeStatusFailed)
		return baseResult
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, messagesURL, bytes.NewReader(body))
	if err != nil {
		baseResult.Status = SupplierProbeStatusFailed
		baseResult.Detail = supplierProbeSafeDetail(SupplierProbeStatusFailed)
		return baseResult
	}
	// CloudWise Anthropic MaaS accepts Bearer (prod matrix); also set x-api-key for
	// Anthropic-native relays that ignore Authorization.
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		baseResult.Status = SupplierProbeStatusFailed
		baseResult.Detail = supplierProbeSafeDetail(SupplierProbeStatusFailed)
		return baseResult
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	status := supplierAnthropicMessagesProbeStatus(resp.StatusCode, raw)
	baseResult.Status = status
	if status == SupplierProbeStatusPassed {
		baseResult.Protocol = "anthropic_messages"
	} else {
		baseResult.Detail = supplierProbeSafeDetail(status)
	}
	return baseResult
}

func supplierAnthropicMessagesProbeURL(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(trimmed, "/v1/messages") {
		return trimmed
	}
	if strings.HasSuffix(trimmed, "/v1") {
		return trimmed + "/messages"
	}
	return trimmed + "/v1/messages"
}

func supplierAnthropicMessagesProbeStatus(statusCode int, body []byte) SupplierProbeStatus {
	lower := strings.ToLower(string(body))
	switch {
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return SupplierProbeStatusAuthFailed
	case statusCode == http.StatusNotFound || strings.Contains(lower, "model not found") ||
		strings.Contains(lower, "model_not_found") || strings.Contains(lower, "unknown model"):
		return SupplierProbeStatusModelUnsupported
	case statusCode == http.StatusOK && (strings.Contains(lower, "chat.completion") ||
		strings.Contains(lower, "response.completed")):
		return SupplierProbeStatusProtocolUnsupported
	case statusCode == http.StatusOK && (strings.Contains(lower, `"type":"message"`) ||
		strings.Contains(lower, `"type": "message"`) || strings.Contains(lower, "message_start")):
		return SupplierProbeStatusPassed
	case statusCode >= 200 && statusCode < 300:
		// 2xx without Anthropic message shape is not Messages evidence.
		return SupplierProbeStatusProtocolUnsupported
	default:
		return SupplierProbeStatusFailed
	}
}

func (s *AccountTestService) probeSupplierVideoModel(
	ctx context.Context,
	input SupplierProbeInput,
	baseResult SupplierProbeResult,
) SupplierProbeResult {
	account := input.Account
	baseURL := strings.TrimRight(strings.TrimSpace(account.GetBaseURL()), "/")
	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	if baseURL == "" || apiKey == "" {
		baseResult.Status = SupplierProbeStatusFailed
		baseResult.Detail = supplierProbeSafeDetail(SupplierProbeStatusFailed)
		return baseResult
	}
	submitURL := supplierVideoProbeURL(account.ChannelType, baseURL, baseResult.UpstreamModelID)
	upstreamModelID := baseResult.UpstreamModelID
	fmgo := newapiintegration.IsFMGoBaseURL(account.ChannelType, baseURL)
	xrtoken := newapiintegration.IsXRTokenBaseURL(account.ChannelType, baseURL)
	if xrtoken {
		// XRToken publishes Ark SKUs under volcengine/; bare ids may work on
		// some SKUs but production relay always prefixes — probe must match.
		upstreamModelID = newapiintegration.XRTokenUpstreamVideoModel(upstreamModelID)
	}
	body := supplierArkContentsProbeBody(upstreamModelID)
	fmgoVideos := fmgo && newapiintegration.FMGoUsesVideosDialect(baseResult.UpstreamModelID)
	if fmgoVideos {
		body = supplierFMGoVideosProbeBody(baseResult.UpstreamModelID)
	} else if fmgo {
		body = supplierFMGoChatProbeBody(baseResult.UpstreamModelID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, submitURL, bytes.NewReader(body))
	if err != nil {
		baseResult.Status = SupplierProbeStatusFailed
		baseResult.Detail = supplierProbeSafeDetail(SupplierProbeStatusFailed)
		return baseResult
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	if fmgo && !fmgoVideos {
		req.Header.Set("Prefer", "respond-async")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		baseResult.Status = SupplierProbeStatusFailed
		baseResult.Detail = supplierProbeSafeDetail(SupplierProbeStatusFailed)
		return baseResult
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	status := supplierVideoProbeStatus(resp.StatusCode, raw)
	baseResult.Status = status
	if status == SupplierProbeStatusPassed {
		if fmgoVideos {
			baseResult.Protocol = "fmgo_videos"
		} else if fmgo {
			baseResult.Protocol = "fmgo_chat_completions"
		} else {
			baseResult.Protocol = "openai_video"
		}
	}
	baseResult.Detail = supplierProbeSafeDetail(status)
	return baseResult
}

func supplierVideoProbeURL(channelType int, baseURL, upstreamModelID string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	switch {
	case newapiintegration.IsFMGoBaseURL(channelType, baseURL):
		return newapiintegration.NormalizeFMGoBaseURL(baseURL) + newapiintegration.FMGoSubmitPath(upstreamModelID)
	case newapiintegration.IsXRTokenBaseURL(channelType, baseURL):
		return newapiintegration.NormalizeXRTokenBaseURL(baseURL) + "/v1/contents/generations/tasks"
	default:
		return baseURL + "/api/v3/contents/generations/tasks"
	}
}

// supplierArkContentsProbeBody builds the Ark / XRToken contents.generations
// probe payload. Official Ark and XRToken both reject a top-level `prompt`
// field (MissingParameter: content); Sync used to send prompt-only and
// permanently failed every XRToken configured model.
func supplierArkContentsProbeBody(upstreamModelID string) []byte {
	model := strings.TrimSpace(upstreamModelID)
	body, err := json.Marshal(map[string]any{
		"model": model,
		"content": []map[string]any{
			{"type": "text", "text": "probe"},
		},
	})
	if err != nil {
		return []byte(`{"model":` + strconv.Quote(model) + `,"content":[{"type":"text","text":"probe"}]}`)
	}
	return body
}

func supplierFMGoVideosProbeBody(upstreamModelID string) []byte {
	resolution := newapiintegration.FMGoDefaultResolution
	duration := newapiintegration.FMGoDefaultDuration
	model := strings.TrimSpace(upstreamModelID)
	for _, res := range []string{"720p", "480p"} {
		if strings.Contains(model, "-"+res+"-") {
			resolution = res
			break
		}
	}
	for _, seconds := range []int{30, 15, 12, 10, 8, 6, 5} {
		if strings.HasSuffix(model, fmt.Sprintf("-%ds", seconds)) {
			duration = seconds
			break
		}
	}
	if newapiintegration.FMGoModelFamily(model) == newapiintegration.FMGoFamilyMini && resolution == "720p" && duration == 15 {
		duration = 10
	}
	body, err := json.Marshal(map[string]any{
		"model":        model,
		"prompt":       "probe",
		"aspect_ratio": "16:9",
		"resolution":   resolution,
		"seconds":      strconv.Itoa(duration),
	})
	if err != nil {
		return []byte(`{"model":` + strconv.Quote(model) + `,"prompt":"probe"}`)
	}
	return body
}

func supplierFMGoChatProbeBody(upstreamModelID string) []byte {
	resolution := newapiintegration.FMGoDefaultResolution
	duration := newapiintegration.FMGoDefaultDuration
	model := strings.TrimSpace(upstreamModelID)
	for _, res := range []string{"720p", "480p"} {
		if strings.Contains(model, "-"+res+"-") {
			resolution = res
			break
		}
	}
	for _, seconds := range []int{15, 12, 10, 8, 6} {
		if strings.HasSuffix(model, fmt.Sprintf("-%ds", seconds)) {
			duration = seconds
			break
		}
	}
	body, err := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]any{
			{"role": "user", "content": "probe"},
		},
		"generationConfig": map[string]any{
			"videoConfig": map[string]any{
				"duration":    duration,
				"aspectRatio": "16:9",
				"resolution":  resolution,
			},
		},
		"async": true,
	})
	if err != nil {
		return []byte(`{"model":` + strconv.Quote(model) + `,"async":true}`)
	}
	return body
}

func supplierVideoProbeStatus(statusCode int, body []byte) SupplierProbeStatus {
	lower := strings.ToLower(string(body))
	switch {
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden ||
		strings.Contains(lower, "unauthorized") || strings.Contains(lower, "forbidden"):
		return SupplierProbeStatusAuthFailed
	case strings.Contains(lower, "model_not_found") || strings.Contains(lower, "model not found") ||
		strings.Contains(lower, "unknown model"):
		return SupplierProbeStatusModelUnsupported
	case strings.Contains(lower, "fail_to_fetch_task") || strings.Contains(lower, "not found"):
		return SupplierProbeStatusFailed
	case statusCode >= 200 && statusCode < 300:
		return SupplierProbeStatusPassed
	default:
		return SupplierProbeStatusFailed
	}
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
