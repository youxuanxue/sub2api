package service

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	kimiToolParametersRequestKey = "tk_kimi_tool_parameters_request"
	// kimiGenericParameterReject is the upstream sentence for a recursive tool
	// schema. KVV accepts the 400 only when the client text also says schema
	// or parameters. "parameter" alone does not match.
	kimiGenericParameterReject = "a parameter specified in the request is not valid"
	kimiToolSchemaRejectSuffix = ": tool function parameters schema"
)

func noteKimiToolParametersRequest(c *gin.Context, model string, body []byte) {
	if c == nil || !isKimiModel(model) || !kimiBodyHasToolParameters(body) {
		return
	}
	c.Set(kimiToolParametersRequestKey, true)
}

func kimiBodyHasToolParameters(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	found := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("function.parameters").Exists() {
			found = true
			return false
		}
		return true
	})
	return found
}

// TkRewriteKimiToolSchemaRejectMessage appends a schema cue to the one generic
// upstream 400 that KVV sees for recursive tool schemas. Other 400 text, other
// models, and requests without tool parameters stay unchanged. Ops already
// stored the original message before this client-facing rewrite.
func TkRewriteKimiToolSchemaRejectMessage(c *gin.Context, statusCode int, message string) string {
	if c == nil || statusCode != 400 {
		return message
	}
	flagged, _ := c.Get(kimiToolParametersRequestKey)
	if flagged != true {
		return message
	}
	trimmed := strings.TrimSpace(message)
	if !strings.EqualFold(strings.TrimRight(trimmed, "."), kimiGenericParameterReject) {
		return message
	}
	return trimmed + kimiToolSchemaRejectSuffix
}
