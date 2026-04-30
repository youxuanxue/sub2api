package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
)

func (s *AccountTestService) testNewAPIAccountConnectionTK(c *gin.Context, account *Account, modelID string, prompt string) error {
	ctx := c.Request.Context()

	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	if apiKey == "" {
		return s.sendErrorAndEnd(c, "No API key available")
	}
	channelType := account.ChannelType
	if channelType <= 0 {
		return s.sendErrorAndEnd(c, "Account is missing channel_type; reconfigure under the newapi platform")
	}

	testModelID := strings.TrimSpace(modelID)
	if testModelID == "" {
		testModelID = openai.DefaultTestModel
	}
	testModelID = account.GetMappedModel(testModelID)

	testPrompt := strings.TrimSpace(prompt)
	if testPrompt == "" {
		testPrompt = "hi"
	}

	stream := true
	maxTokens := uint(1024)
	payload := map[string]any{
		"model": testModelID,
		"messages": []map[string]any{
			{"role": "user", "content": testPrompt},
		},
		"stream":     stream,
		"max_tokens": maxTokens,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create test payload")
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	probeLabel := fmt.Sprintf("newapi/channel_type=%d", channelType)
	s.sendEvent(c, TestEvent{Type: "test_start", Model: probeLabel})

	rec := httptest.NewRecorder()
	probeCtx, _ := gin.CreateTestContext(rec)
	probeCtx.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	probeCtx.Request.Header = c.Request.Header.Clone()
	probeCtx.Request.Header.Set("Content-Type", "application/json")
	probeCtx.Request.ContentLength = int64(len(body))

	if err := dispatchNewAPIAccountTestChatCompletions(ctx, probeCtx, account, body); err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Upstream chat test failed: %s", err.Error()))
	}

	status := rec.Code
	if status == 0 {
		status = http.StatusOK
	}
	if status < 200 || status >= 300 {
		return s.sendErrorAndEnd(c, fmt.Sprintf("API returned %d: %s", status, rec.Body.String()))
	}

	return s.processOpenAIChatCompletionsStream(c, strings.NewReader(rec.Body.String()))
}

func (s *AccountTestService) processOpenAIChatCompletionsStream(c *gin.Context, body io.Reader) error {
	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return s.sendErrorAndEnd(c, "Stream ended before chat completion finished")
			}
			return s.sendErrorAndEnd(c, fmt.Sprintf("Stream read error: %s", err.Error()))
		}

		line = strings.TrimSpace(line)
		if line == "" || !sseDataPrefix.MatchString(line) {
			continue
		}

		jsonStr := sseDataPrefix.ReplaceAllString(line, "")
		if jsonStr == "[DONE]" {
			s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
			return nil
		}

		var data map[string]any
		if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
			continue
		}
		if errData, ok := data["error"].(map[string]any); ok {
			if msg, ok := errData["message"].(string); ok && msg != "" {
				return s.sendErrorAndEnd(c, msg)
			}
			return s.sendErrorAndEnd(c, "Unknown upstream error")
		}

		choices, ok := data["choices"].([]any)
		if !ok || len(choices) == 0 {
			continue
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			continue
		}
		if delta, ok := choice["delta"].(map[string]any); ok {
			if content, ok := delta["content"].(string); ok && content != "" {
				s.sendEvent(c, TestEvent{Type: "content", Text: content})
			}
		}
		if message, ok := choice["message"].(map[string]any); ok {
			if content, ok := message["content"].(string); ok && content != "" {
				s.sendEvent(c, TestEvent{Type: "content", Text: content})
			}
		}
		if finishReason, ok := choice["finish_reason"].(string); ok && finishReason != "" {
			s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
			return nil
		}
	}
}
