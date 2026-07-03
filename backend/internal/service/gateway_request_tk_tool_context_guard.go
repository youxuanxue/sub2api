package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	tkClientToolContextErrorType = "invalid_request_error"
)

type tkAnthropicToolContextViolation struct {
	Reason       string
	Message      string
	MessageIndex int
	ToolUseID    string
}

type ClientToolContextCorruptError struct {
	Reason string
}

func (e *ClientToolContextCorruptError) Error() string {
	if e == nil || strings.TrimSpace(e.Reason) == "" {
		return "client tool context corrupt"
	}
	return "client tool context corrupt: " + e.Reason
}

func tkValidateAnthropicToolContext(body []byte, requireSystemSurface bool) *tkAnthropicToolContextViolation {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return nil
	}
	messageRows := messages.Array()
	hasToolResult := false
	for i, msg := range messageRows {
		role := msg.Get("role").String()
		content := msg.Get("content")
		if role != "user" || content.Type != gjson.JSON || !content.IsArray() {
			continue
		}
		resultIDs, violation := tkToolResultIDsFromUserContent(content, i)
		if violation != nil {
			return violation
		}
		if len(resultIDs) == 0 {
			continue
		}
		hasToolResult = true
		if i == 0 || messageRows[i-1].Get("role").String() != "assistant" {
			return tkToolContextViolation("orphan_tool_result_context", i, resultIDs[0],
				"Invalid Anthropic tool continuation: tool_result must be immediately preceded by the assistant tool_use message.")
		}
		toolUseIDs := tkToolUseIDsFromAssistantContent(messageRows[i-1].Get("content"))
		if len(toolUseIDs) == 0 {
			return tkToolContextViolation("orphan_tool_result_context", i, resultIDs[0],
				"Invalid Anthropic tool continuation: previous assistant message has no tool_use block for the tool_result.")
		}
		resultSet := make(map[string]struct{}, len(resultIDs))
		for _, id := range resultIDs {
			if _, duplicate := resultSet[id]; duplicate {
				return tkToolContextViolation("duplicate_tool_result_for_tool_use", i, id,
					"Invalid Anthropic tool continuation: tool_result.tool_use_id is duplicated in the user message.")
			}
			if _, ok := toolUseIDs[id]; !ok {
				return tkToolContextViolation("orphan_tool_result_context", i, id,
					"Invalid Anthropic tool continuation: tool_result.tool_use_id has no matching tool_use in the immediately preceding assistant message.")
			}
			resultSet[id] = struct{}{}
		}
		for id := range toolUseIDs {
			if _, ok := resultSet[id]; !ok {
				return tkToolContextViolation("missing_tool_result_for_tool_use", i, id,
					"Invalid Anthropic tool continuation: assistant tool_use was not answered by the immediately following user tool_result message.")
			}
		}
	}
	if hasToolResult && requireSystemSurface && !tkAnthropicBodyHasSystemSurface(body) {
		return tkToolContextViolation("tool_result_without_system_surface", -1, "",
			"Invalid Claude Code tool continuation: tool_result request is missing the expected system prompt surface.")
	}
	return nil
}

func tkToolResultIDsFromUserContent(content gjson.Result, messageIndex int) ([]string, *tkAnthropicToolContextViolation) {
	blocks := content.Array()
	resultIDs := make([]string, 0)
	seenNonToolResult := false
	for _, block := range blocks {
		blockType := block.Get("type").String()
		if blockType != "tool_result" {
			seenNonToolResult = true
			continue
		}
		if seenNonToolResult {
			return nil, tkToolContextViolation("tool_result_not_leading", messageIndex, block.Get("tool_use_id").String(),
				"Invalid Anthropic tool continuation: tool_result blocks must appear before any other content in the user message.")
		}
		id := strings.TrimSpace(block.Get("tool_use_id").String())
		if id == "" {
			return nil, tkToolContextViolation("tool_result_missing_tool_use_id", messageIndex, "",
				"Invalid Anthropic tool continuation: tool_result is missing tool_use_id.")
		}
		resultIDs = append(resultIDs, id)
	}
	return resultIDs, nil
}

func tkToolUseIDsFromAssistantContent(content gjson.Result) map[string]struct{} {
	out := map[string]struct{}{}
	if content.Type != gjson.JSON || !content.IsArray() {
		return out
	}
	for _, block := range content.Array() {
		if block.Get("type").String() != "tool_use" {
			continue
		}
		id := strings.TrimSpace(block.Get("id").String())
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func tkAnthropicBodyHasSystemSurface(body []byte) bool {
	system := gjson.GetBytes(body, "system")
	switch system.Type {
	case gjson.String:
		if strings.TrimSpace(system.String()) != "" {
			return true
		}
	case gjson.JSON:
		if system.IsArray() {
			for _, item := range system.Array() {
				if strings.TrimSpace(item.Get("text").String()) != "" {
					return true
				}
			}
		}
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return false
	}
	for _, msg := range messages.Array() {
		content := msg.Get("content")
		switch content.Type {
		case gjson.String:
			if strings.Contains(content.String(), "<system-reminder>") {
				return true
			}
		case gjson.JSON:
			if !content.IsArray() {
				continue
			}
			for _, block := range content.Array() {
				if block.Get("type").String() == "text" &&
					strings.Contains(block.Get("text").String(), "<system-reminder>") {
					return true
				}
			}
		}
	}
	return false
}

func tkToolContextViolation(reason string, messageIndex int, toolUseID string, message string) *tkAnthropicToolContextViolation {
	return &tkAnthropicToolContextViolation{
		Reason:       reason,
		Message:      message,
		MessageIndex: messageIndex,
		ToolUseID:    toolUseID,
	}
}

func (s *GatewayService) tkRejectInvalidAnthropicToolContext(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	requireSystemSurface bool,
	passthrough bool,
) error {
	violation := tkValidateAnthropicToolContext(body, requireSystemSurface)
	if violation == nil {
		return nil
	}
	requestID := ""
	if ctx != nil {
		requestID, _ = ctx.Value(ctxkey.RequestID).(string)
	}
	accountID := int64(0)
	accountName := ""
	platform := PlatformAnthropic
	if account != nil {
		accountID = account.ID
		accountName = account.Name
		platform = account.Platform
	}
	setOpsUpstreamRequestBody(c, body)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           platform,
		AccountID:          accountID,
		AccountName:        accountName,
		UpstreamStatusCode: 0,
		Passthrough:        passthrough,
		Kind:               OpsUpstreamKindClientToolContextCorrupt,
		Message:            violation.Message,
		Detail:             fmt.Sprintf("reason=%s message_index=%d tool_use_id=%s", violation.Reason, violation.MessageIndex, violation.ToolUseID),
	})
	MarkOpsClientPolicyDenied(c, OpsClientPolicyDeniedReasonLocalPolicyDenied)
	slog.Warn("gateway.anthropic_client_tool_context_rejected",
		slog.String("request_id", requestID),
		slog.Int64("account_id", accountID),
		slog.String("reason", violation.Reason),
		slog.Int("message_index", violation.MessageIndex),
		slog.String("tool_use_id", violation.ToolUseID),
	)
	if c != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    tkClientToolContextErrorType,
				"message": violation.Message + " Start a new session or resume from a clean summary.",
			},
		})
		MarkResponseCommitted(c)
	}
	return &ClientToolContextCorruptError{Reason: violation.Reason}
}

func (s *GatewayService) tkRequiresClaudeCodeSystemSurface(ctx context.Context, c *gin.Context, account *Account) bool {
	if account != nil && s != nil && s.isCanonicalAnthropicOAuth(account) {
		return true
	}
	if IsClaudeCodeClient(ctx) {
		return true
	}
	if c != nil && c.Request != nil {
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.GetHeader("User-Agent"))), "claude-cli/")
	}
	return false
}
