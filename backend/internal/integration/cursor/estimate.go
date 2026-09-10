package cursor

import (
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tokenestimate"
)

const EstimatedBillingTier = "cursor-oauth-estimated"
const ReportedBillingTier = "cursor-oauth-reported"

// EstimateHandoffUsage uses the same tokenizer as Kiro, without inventing cache
// hits or adding estimates to reported usage. It counts only this request.
func EstimateHandoffUsage(input AgentRequest, result AgentResult) AgentUsage {
	parts := []string{input.System}
	for _, message := range input.Messages {
		parts = append(parts, message.Text)
		for _, call := range message.ToolCalls {
			parts = append(parts, call.Name, call.ID, estimateJSON(call.Arguments))
		}
	}
	for _, tool := range input.Tools {
		parts = append(parts, tool.Name, tool.Description, estimateJSON(tool.Schema))
	}
	output := []string{result.Text, result.Thinking}
	for _, call := range result.ToolCalls {
		output = append(output, call.Name, call.ID, estimateJSON(call.Arguments))
	}
	return AgentUsage{Input: int64(tokenestimate.Count(strings.Join(parts, "\n"))), Output: int64(tokenestimate.Count(strings.Join(output, "\n")))}
}
func estimateJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}
