package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	tkImageRequestAuditComponent = "audit.openai_image_request"
	tkImageRequestAuditMessage   = "openai_image.request_payload"
	tkImagePromptPreviewMaxBytes = 1024

	tkStudioSourceHeader = "X-TokenKey-Studio-Source"
	tkStudioRunIDHeader  = "X-TokenKey-Studio-Run-Id"
	tkStudioPanelHeader  = "X-TokenKey-Studio-Panel-Id"
)

func logOpenAIImageGenerationRequestAudit(c *gin.Context, apiKey *service.APIKey, userID int64, account *service.Account, body []byte, forwardBody []byte) {
	prompt := gjson.GetBytes(body, "prompt").String()
	logOpenAIImageRequestAudit(c, apiKey, userID, account, body, forwardBody, "images.generations", prompt, "prompt")
}

func logOpenAIStudioChatImageRequestAudit(c *gin.Context, apiKey *service.APIKey, userID int64, account *service.Account, body []byte, forwardBody []byte) {
	if !tkIsStudioImageTrace(c) {
		return
	}
	prompt, source := tkExtractOpenAIChatUserPrompt(body)
	logOpenAIImageRequestAudit(c, apiKey, userID, account, body, forwardBody, "chat.completions", prompt, source)
}

func logOpenAIImageRequestAudit(
	c *gin.Context,
	apiKey *service.APIKey,
	userID int64,
	account *service.Account,
	body []byte,
	forwardBody []byte,
	surface string,
	prompt string,
	promptSource string,
) {
	if c == nil || len(body) == 0 {
		return
	}
	requestID, clientRequestID := tkRequestIDs(c)
	requestModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	forwardModel := strings.TrimSpace(gjson.GetBytes(forwardBody, "model").String())
	if forwardModel == "" {
		forwardModel = requestModel
	}

	fields := map[string]any{
		"request_id":               requestID,
		"client_request_id":        clientRequestID,
		"user_id":                  userID,
		"api_key_id":               tkAPIKeyID(apiKey),
		"group_id":                 tkAPIKeyGroupID(apiKey),
		"account_id":               tkAccountID(account),
		"platform":                 tkAccountPlatform(account),
		"model":                    requestModel,
		"requested_model":          requestModel,
		"forward_model":            forwardModel,
		"surface":                  strings.TrimSpace(surface),
		"request_path":             tkRequestPath(c),
		"inbound_endpoint":         GetInboundEndpoint(c),
		"upstream_endpoint":        resolveOpenAIUpstreamEndpoint(c, account, nil),
		"request_body_sha256":      tkSHA256Hex(body),
		"request_body_bytes":       len(body),
		"forward_body_sha256":      tkSHA256Hex(forwardBody),
		"prompt_source":            strings.TrimSpace(promptSource),
		"prompt_preview":           tkPromptPreview(prompt),
		"prompt_preview_truncated": len(prompt) > tkImagePromptPreviewMaxBytes,
		"prompt_sha256":            tkSHA256Hex([]byte(prompt)),
		"prompt_bytes":             len([]byte(prompt)),
		"prompt_runes":             len([]rune(prompt)),
		"size":                     tkJSONScalarString(body, "size"),
		"forward_size":             tkJSONScalarString(forwardBody, "size"),
		"n":                        tkImageRequestN(body),
		"quality":                  tkJSONScalarString(body, "quality"),
		"style":                    tkJSONScalarString(body, "style"),
		"response_format":          tkJSONScalarString(body, "response_format"),
		"background":               tkJSONScalarString(body, "background"),
		"output_format":            tkJSONScalarString(body, "output_format"),
		"moderation":               tkJSONScalarString(body, "moderation"),
		"studio_source":            strings.TrimSpace(c.GetHeader(tkStudioSourceHeader)),
		"studio_run_id":            strings.TrimSpace(c.GetHeader(tkStudioRunIDHeader)),
		"studio_panel_id":          strings.TrimSpace(c.GetHeader(tkStudioPanelHeader)),
	}
	logger.WriteSinkEvent("info", tkImageRequestAuditComponent, tkImageRequestAuditMessage, fields)
}

func tkIsStudioImageTrace(c *gin.Context) bool {
	if c == nil {
		return false
	}
	source := strings.ToLower(strings.TrimSpace(c.GetHeader(tkStudioSourceHeader)))
	return strings.Contains(source, "studio") && strings.Contains(source, "image")
}

func tkExtractOpenAIChatUserPrompt(body []byte) (string, string) {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return "", "messages"
	}
	for _, msg := range messages.Array() {
		if role := strings.ToLower(strings.TrimSpace(msg.Get("role").String())); role != "" && role != "user" {
			continue
		}
		content := msg.Get("content")
		if content.Type == gjson.String {
			return content.String(), "messages.user.content"
		}
		if !content.IsArray() {
			continue
		}
		parts := make([]string, 0, len(content.Array()))
		for _, part := range content.Array() {
			if typ := strings.ToLower(strings.TrimSpace(part.Get("type").String())); typ != "" && typ != "text" {
				continue
			}
			text := part.Get("text").String()
			if strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n"), "messages.user.content[].text"
		}
	}
	return "", "messages.user.content"
}

func tkRequestIDs(c *gin.Context) (string, string) {
	if c == nil || c.Request == nil {
		return "", ""
	}
	requestID := strings.TrimSpace(c.Writer.Header().Get("X-Request-Id"))
	if requestID == "" {
		requestID, _ = c.Request.Context().Value(ctxkey.RequestID).(string)
		requestID = strings.TrimSpace(requestID)
	}
	clientRequestID, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
	return requestID, strings.TrimSpace(clientRequestID)
}

func tkPromptPreview(prompt string) string {
	preview := truncateString(prompt, tkImagePromptPreviewMaxBytes)
	return logredact.RedactText(preview)
}

func tkSHA256Hex(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func tkJSONScalarString(body []byte, path string) string {
	if len(body) == 0 || strings.TrimSpace(path) == "" {
		return ""
	}
	value := gjson.GetBytes(body, path)
	if !value.Exists() {
		return ""
	}
	if value.Type == gjson.String {
		return strings.TrimSpace(value.String())
	}
	return strings.TrimSpace(value.Raw)
}

func tkImageRequestN(body []byte) int64 {
	n := gjson.GetBytes(body, "n")
	if !n.Exists() || n.Int() <= 0 {
		return 1
	}
	return n.Int()
}

func tkAPIKeyID(apiKey *service.APIKey) int64 {
	if apiKey == nil {
		return 0
	}
	return apiKey.ID
}

func tkAPIKeyGroupID(apiKey *service.APIKey) any {
	if apiKey == nil || apiKey.GroupID == nil {
		return nil
	}
	return *apiKey.GroupID
}

func tkAccountID(account *service.Account) int64 {
	if account == nil {
		return 0
	}
	return account.ID
}

func tkAccountPlatform(account *service.Account) string {
	if account == nil {
		return ""
	}
	return account.Platform
}

func tkRequestPath(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return ""
	}
	return c.Request.URL.Path
}
