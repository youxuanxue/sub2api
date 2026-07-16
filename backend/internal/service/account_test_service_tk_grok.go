package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
)

func (s *AccountTestService) testGrokAccountConnection(c *gin.Context, account *Account, modelID string, prompt string) error {
	if account == nil {
		return s.sendErrorAndEnd(c, "Account not found")
	}

	authToken := ""
	normalizedBaseURL := ""
	switch account.Type {
	case AccountTypeAPIKey:
		authToken = account.GetOpenAIApiKey()
		baseURL := account.GetOpenAIBaseURL()
		if strings.TrimSpace(baseURL) == "" {
			return s.sendErrorAndEnd(c, "No grok base URL available")
		}
		var err error
		normalizedBaseURL, err = s.validateUpstreamBaseURL(baseURL)
		if err != nil {
			return s.sendErrorAndEnd(c, "Invalid base URL: "+err.Error())
		}
	case AccountTypeOAuth:
		authToken = account.GetGrokAccessToken()
		baseURL := account.GetGrokBaseURL()
		if strings.TrimSpace(baseURL) == "" {
			return s.sendErrorAndEnd(c, "No grok base URL available")
		}
		normalizedBaseURL = baseURL
	default:
		return s.sendErrorAndEnd(c, "Unsupported grok account type: "+account.Type)
	}
	if strings.TrimSpace(authToken) == "" {
		return s.sendErrorAndEnd(c, "No grok credential available")
	}

	testModelID := normalizeGrokAdminTestModel(modelID)
	testModelID = account.GetMappedModel(testModelID)

	return s.testGrokResponsesConnection(c, account, testModelID, prompt, normalizedBaseURL, authToken)
}

func (s *AccountTestService) testGrokResponsesConnection(
	c *gin.Context,
	account *Account,
	testModelID string,
	prompt string,
	normalizedBaseURL string,
	authToken string,
) error {
	ctx := c.Request.Context()
	apiURL, err := grokTestResponsesURL(account, normalizedBaseURL)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	payloadBytes, _ := json.Marshal(createGrokResponsesTestPayload(testModelID, prompt))

	s.sendEvent(c, TestEvent{Type: "test_start", Model: testModelID})
	s.sendEvent(c, TestEvent{Type: "status", Text: "正在通过 /v1/responses 测试连接"})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create Responses request")
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+authToken)
	if account.IsGrokOAuth() {
		applyGrokCLIHeaders(req.Header)
	}
	account.ApplyHeaderOverrides(req.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.resolveAccountTestTLSProfile(account))
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Responses API (/v1/responses) request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	now := time.Now()
	snapshot := parseGrokQuotaSnapshot(resp.Header, resp.StatusCode, now)
	if snapshot != nil && s.accountRepo != nil {
		resetAt, limited := grokRateLimitResetAtForAccount(account, snapshot, now)
		if limited {
			normalizeGrokExhaustedWindowResets(snapshot, resetAt, now)
		}
		_ = s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{grokQuotaSnapshotExtraKey: snapshot})
		if limited {
			persistGrokRateLimit(ctx, s.accountRepo, account, resetAt)
		} else if isSuccessfulGrokRateLimitRecovery(account, snapshot) {
			clearGrokRateLimitAfterRecovery(ctx, s.accountRepo, account)
		}
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusUnauthorized && s.accountRepo != nil {
			errMsg := fmt.Sprintf("Responses authentication failed (401): %s", string(body))
			_ = s.accountRepo.SetError(ctx, account.ID, errMsg)
		}
		return s.sendErrorAndEnd(c, fmt.Sprintf("Responses API (/v1/responses) returned %d: %s", resp.StatusCode, string(body)))
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		return s.processOpenAIStream(c, resp.Body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Failed to read Responses body: %s", err.Error()))
	}
	return s.processGrokResponsesJSON(c, body)
}

func grokTestResponsesURL(account *Account, normalizedBaseURL string) (string, error) {
	switch account.Type {
	case AccountTypeOAuth:
		return xai.BuildResponsesURL(strings.TrimSpace(account.GetGrokBaseURL()))
	case AccountTypeAPIKey:
		if strings.TrimSpace(normalizedBaseURL) == "" {
			return "", fmt.Errorf("no grok base URL available")
		}
		return buildOpenAIResponsesURL(normalizedBaseURL), nil
	default:
		return "", fmt.Errorf("unsupported grok account type: %s", account.Type)
	}
}

func createGrokResponsesTestPayload(modelID string, prompt string) map[string]any {
	testPrompt := strings.TrimSpace(prompt)
	if testPrompt == "" {
		testPrompt = "hi"
	}
	return map[string]any{
		"model": modelID,
		"input": []map[string]any{
			{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{
						"type": "input_text",
						"text": testPrompt,
					},
				},
			},
		},
		"stream":            true,
		"max_output_tokens": 32,
	}
}

func (s *AccountTestService) processGrokResponsesJSON(c *gin.Context, body []byte) error {
	if !json.Valid(body) {
		return s.sendErrorAndEnd(c, "Responses API returned invalid JSON")
	}

	status := strings.TrimSpace(jsonGetString(body, "status"))
	if status == "failed" {
		msg := jsonGetString(body, "error.message")
		if msg == "" {
			msg = "Grok response failed"
		}
		return s.sendErrorAndEnd(c, msg)
	}

	text := extractGrokResponsesOutputText(body)
	if text != "" {
		s.sendEvent(c, TestEvent{Type: "content", Text: text})
	}

	if status != "completed" && text == "" {
		return s.sendErrorAndEnd(c, "Responses API returned no output text")
	}

	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

func jsonGetString(body []byte, path string) string {
	parts := strings.Split(path, ".")
	var current any
	if err := json.Unmarshal(body, &current); err != nil {
		return ""
	}
	for _, part := range parts {
		obj, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = obj[part]
	}
	value, _ := current.(string)
	return strings.TrimSpace(value)
}

func extractGrokResponsesOutputText(body []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	output, ok := payload["output"].([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, item := range output {
		msg, ok := item.(map[string]any)
		if !ok || msg["type"] != "message" {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, part := range content {
			block, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := block["text"].(string); ok && strings.TrimSpace(text) != "" {
				_, _ = b.WriteString(text)
			}
		}
	}
	return b.String()
}

func normalizeGrokAdminTestModel(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return defaultGrokTestModelID
	}
	lower := strings.ToLower(modelID)
	if strings.HasPrefix(lower, "grok") {
		if strings.HasPrefix(lower, "grok-imagine-") {
			return defaultGrokTestModelID
		}
		return modelID
	}
	return defaultGrokTestModelID
}
