package service

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// This schema intentionally has no text/name/raw fields. Inbound bodies are still
// complete in the handler context even when asynchronous QA capture is truncated.
type openAIProtectionBodyDiagnostic struct {
	Bytes          int    `json:"bytes"`
	SHA256         string `json:"sha256"`
	SearchHistory  int    `json:"search_history_count"`
	DeclaredTools  int    `json:"declared_tool_count"`
	NamespaceTools int    `json:"namespace_tool_count"`
	SearchDeclared bool   `json:"search_declared"`
	Compaction     bool   `json:"compaction_trigger"`
	ChoiceKind     string `json:"choice_kind"`
	RepairDecision string `json:"repair_decision"`
}

func inspectOpenAIProtectionBody(body []byte) openAIProtectionBodyDiagnostic {
	p := planOpenAIOAuthWebSearchHistory(body)
	digest := sha256.Sum256(body)
	d := openAIProtectionBodyDiagnostic{Bytes: len(body), SHA256: hex.EncodeToString(digest[:]), SearchHistory: p.historyCount, DeclaredTools: len(p.tools), SearchDeclared: p.hasSearch, Compaction: p.hasCompaction, RepairDecision: p.reason}
	for _, tool := range p.tools {
		if tool.Get("type").String() == "namespace" {
			d.NamespaceTools++
		}
	}
	if d.RepairDecision == "" {
		d.RepairDecision = "repairable"
		if p.textHistory {
			d.RepairDecision = "repairable_history_text"
		}
	}
	choice := gjson.GetBytes(body, "tool_choice")
	switch {
	case !choice.Exists() || choice.Type == gjson.Null:
		d.ChoiceKind = "default"
	case choice.Type == gjson.String && (choice.String() == "none" || choice.String() == "auto" || choice.String() == "required"):
		d.ChoiceKind = choice.String()
	case choice.IsObject() && choice.Get("type").String() == "allowed_tools":
		d.ChoiceKind = "allowed_tools"
	case choice.IsObject():
		d.ChoiceKind = "explicit_tool"
	default:
		d.ChoiceKind = "invalid"
	}
	return d
}

func recordOpenAIResponseProtectionDiagnostic(c *gin.Context, account *Account) {
	if c == nil || c.Request == nil {
		return
	}
	const loggedKey = "openai_protection_diagnostic_logged"
	if c.GetBool(loggedKey) {
		return
	}
	c.Set(loggedKey, true)
	fields := []zap.Field{zap.Bool("responses_lite", isOpenAIResponsesLiteHeader(c.GetHeader(responsesLiteHeader))), zap.Bool("compact_endpoint", isOpenAIResponsesCompactPath(c))}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID))
	}
	for _, field := range []struct{ key, name string }{{OpsRequestBodyKey, "inbound"}, {OpsUpstreamRequestBodyKey, "upstream"}} {
		value, ok := c.Get(field.key)
		if !ok {
			continue
		}
		var body []byte
		switch value := value.(type) {
		case []byte:
			body = value
		case string:
			body = []byte(value)
		}
		if len(body) > 0 {
			fields = append(fields, zap.Any(field.name, inspectOpenAIProtectionBody(body)))
		}
	}
	logger.FromContext(c.Request.Context()).Warn("openai.response_protection", fields...)
}
