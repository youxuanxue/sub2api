package trajectory

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/klauspost/compress/zstd"
)

type EvidenceBlob struct {
	Request struct {
		Path string `json:"path"`
		Body any    `json:"body"`
	} `json:"request"`
	Response struct {
		StatusCode int            `json:"status_code"`
		Headers    map[string]any `json:"headers"`
		Body       any            `json:"body"`
	} `json:"response"`
	Stream struct {
		Chunks []map[string]any `json:"chunks"`
	} `json:"stream"`
}

type SourceRecord struct {
	Record *ent.QARecord
	Blob   *EvidenceBlob
}

type ExportRow struct {
	SessionID      string    `json:"session_id"`
	TurnIndex      int       `json:"turn_index"`
	Role           string    `json:"role"`
	MessageKind    string    `json:"message_kind"`
	ToolName       string    `json:"tool_name,omitempty"`
	ToolCallID     string    `json:"tool_call_id,omitempty"`
	ToolSchemaJSON any       `json:"tool_schema_json,omitempty"`
	ToolCallJSON   any       `json:"tool_call_json,omitempty"`
	ToolResultJSON any       `json:"tool_result_json,omitempty"`
	ContentJSON    any       `json:"content_json,omitempty"`
	Model          string    `json:"model,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
	RequestID      string    `json:"request_id"`
	TrajectoryID   string    `json:"trajectory_id,omitempty"`
}

type ExportSummary struct {
	RecordCount     int
	SessionCount    int
	ToolCallCount   int
	ToolResultCount int
}

type extractedTool struct {
	Name    string
	CallID  string
	Payload any
}

func DecodeEvidenceBlob(payload []byte) (*EvidenceBlob, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	raw, err := dec.DecodeAll(payload, nil)
	if err != nil {
		return nil, err
	}
	var blob EvidenceBlob
	if err := json.Unmarshal(raw, &blob); err != nil {
		return nil, err
	}
	return &blob, nil
}

func ProjectRecords(sources []SourceRecord) ([]ExportRow, ExportSummary, error) {
	turnBySession := map[string]int{}
	sessions := map[string]struct{}{}
	rows := make([]ExportRow, 0, len(sources)*4)

	for _, source := range sources {
		if source.Record == nil || source.Blob == nil {
			continue
		}
		sessionID := resolveSessionID(source.Record)
		turnBySession[sessionID]++
		turnIndex := turnBySession[sessionID]
		sessions[sessionID] = struct{}{}

		model := strings.TrimSpace(source.Record.RequestedModel)
		if source.Record.UpstreamModel != nil && strings.TrimSpace(*source.Record.UpstreamModel) != "" {
			model = strings.TrimSpace(*source.Record.UpstreamModel)
		}
		requestBody := normalizeProjectionValue(source.Blob.Request.Body)
		responseBody := normalizeProjectionValue(source.Blob.Response.Body)
		base := ExportRow{
			SessionID:    sessionID,
			TurnIndex:    turnIndex,
			Model:        model,
			Timestamp:    source.Record.CreatedAt.UTC(),
			RequestID:    strings.TrimSpace(source.Record.RequestID),
			TrajectoryID: strings.TrimSpace(derefString(source.Record.TrajectoryID)),
		}

		rows = append(rows, exportRowWith(base, "user", "request", ExportRow{
			ContentJSON: extractRequestContent(requestBody),
		}))
		for _, tool := range extractToolResults(requestBody) {
			rows = append(rows, exportRowWith(base, "tool", "tool_result", ExportRow{
				ToolName:       tool.Name,
				ToolCallID:     tool.CallID,
				ToolResultJSON: tool.Payload,
			}))
		}
		for _, tool := range extractToolSchemas(requestBody) {
			rows = append(rows, exportRowWith(base, "assistant", "tool_schema", ExportRow{
				ToolName:       tool.Name,
				ToolSchemaJSON: tool.Payload,
			}))
		}
		rows = append(rows, exportRowWith(base, "assistant", "response", ExportRow{
			ContentJSON: extractResponseContent(responseBody),
		}))
		for _, tool := range extractToolCalls(responseBody) {
			rows = append(rows, exportRowWith(base, "assistant", "tool_call", ExportRow{
				ToolName:     tool.Name,
				ToolCallID:   tool.CallID,
				ToolCallJSON: tool.Payload,
			}))
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].SessionID != rows[j].SessionID {
			return rows[i].SessionID < rows[j].SessionID
		}
		if rows[i].TurnIndex != rows[j].TurnIndex {
			return rows[i].TurnIndex < rows[j].TurnIndex
		}
		if !rows[i].Timestamp.Equal(rows[j].Timestamp) {
			return rows[i].Timestamp.Before(rows[j].Timestamp)
		}
		return rows[i].MessageKind < rows[j].MessageKind
	})

	summary := ExportSummary{RecordCount: len(rows), SessionCount: len(sessions)}
	for _, row := range rows {
		switch row.MessageKind {
		case "tool_call":
			summary.ToolCallCount++
		case "tool_result":
			summary.ToolResultCount++
		}
	}
	return rows, summary, nil
}

func exportRowWith(base ExportRow, role string, kind string, extra ExportRow) ExportRow {
	row := base
	row.Role = role
	row.MessageKind = kind
	row.ToolName = extra.ToolName
	row.ToolCallID = extra.ToolCallID
	row.ToolSchemaJSON = normalizeProjectionValue(extra.ToolSchemaJSON)
	row.ToolCallJSON = normalizeProjectionValue(extra.ToolCallJSON)
	row.ToolResultJSON = normalizeProjectionValue(extra.ToolResultJSON)
	row.ContentJSON = normalizeProjectionValue(extra.ContentJSON)
	return row
}

func resolveSessionID(record *ent.QARecord) string {
	if record == nil {
		return "unknown"
	}
	if record.SynthSessionID != nil && strings.TrimSpace(*record.SynthSessionID) != "" {
		return strings.TrimSpace(*record.SynthSessionID)
	}
	if record.TrajectoryID != nil && strings.TrimSpace(*record.TrajectoryID) != "" {
		return strings.TrimSpace(*record.TrajectoryID)
	}
	if strings.TrimSpace(record.RequestID) != "" {
		return strings.TrimSpace(record.RequestID)
	}
	return "unknown"
}

func extractRequestContent(body any) any {
	if value, ok := mapValue(body, "messages"); ok {
		return value
	}
	if value, ok := mapValue(body, "input"); ok {
		return value
	}
	if value, ok := mapValue(body, "contents"); ok {
		return value
	}
	if value, ok := mapValue(body, "prompt"); ok {
		return value
	}
	return body
}

func extractResponseContent(body any) any {
	if choices, ok := sliceValueFromMap(body, "choices"); ok && len(choices) > 0 {
		if message, ok := mapValue(choices[0], "message"); ok {
			return message
		}
		return choices
	}
	if value, ok := mapValue(body, "output"); ok {
		return value
	}
	if value, ok := mapValue(body, "content"); ok {
		return value
	}
	if value, ok := mapValue(body, "candidates"); ok {
		return value
	}
	return body
}

func extractToolSchemas(body any) []extractedTool {
	tools, ok := sliceValueFromMap(body, "tools")
	if !ok {
		return nil
	}
	out := make([]extractedTool, 0, len(tools))
	for _, tool := range tools {
		name := toolName(tool)
		if name == "" {
			continue
		}
		out = append(out, extractedTool{Name: name, Payload: tool})
	}
	return out
}

func extractToolCalls(body any) []extractedTool {
	out := []extractedTool{}
	if toolCalls, ok := sliceValueFromMap(body, "tool_calls"); ok {
		out = append(out, flattenTools(toolCalls)...)
	}
	if choices, ok := sliceValueFromMap(body, "choices"); ok {
		for _, choice := range choices {
			message, ok := mapValue(choice, "message")
			if !ok {
				continue
			}
			if toolCalls, ok := sliceValueFromMap(message, "tool_calls"); ok {
				out = append(out, flattenTools(toolCalls)...)
			}
		}
	}
	if output, ok := sliceValueFromMap(body, "output"); ok {
		for _, item := range output {
			kind := strings.ToLower(strings.TrimSpace(stringValue(item, "type")))
			if kind == "function_call" || kind == "tool_call" || kind == "tool_use" {
				name := toolName(item)
				if name == "" {
					continue
				}
				out = append(out, extractedTool{Name: name, CallID: toolCallID(item), Payload: item})
			}
		}
	}
	if content, ok := sliceValueFromMap(body, "content"); ok {
		for _, item := range content {
			kind := strings.ToLower(strings.TrimSpace(stringValue(item, "type")))
			if kind == "tool_use" {
				name := toolName(item)
				if name == "" {
					continue
				}
				out = append(out, extractedTool{Name: name, CallID: toolCallID(item), Payload: item})
			}
		}
	}
	return out
}

func extractToolResults(body any) []extractedTool {
	out := []extractedTool{}
	if messages, ok := sliceValueFromMap(body, "messages"); ok {
		for _, message := range messages {
			role := strings.ToLower(strings.TrimSpace(stringValue(message, "role")))
			if role == "tool" {
				name := toolName(message)
				out = append(out, extractedTool{Name: name, CallID: toolCallID(message), Payload: message})
			}
			if content, ok := sliceValueFromMap(message, "content"); ok {
				for _, item := range content {
					kind := strings.ToLower(strings.TrimSpace(stringValue(item, "type")))
					if kind == "tool_result" {
						out = append(out, extractedTool{Name: toolName(item), CallID: toolCallID(item), Payload: item})
					}
				}
			}
		}
	}
	if input, ok := sliceValueFromMap(body, "input"); ok {
		for _, item := range input {
			kind := strings.ToLower(strings.TrimSpace(stringValue(item, "type")))
			if kind == "function_call_output" || kind == "tool_result" {
				out = append(out, extractedTool{Name: toolName(item), CallID: toolCallID(item), Payload: item})
			}
		}
	}
	return out
}

func flattenTools(values []any) []extractedTool {
	out := make([]extractedTool, 0, len(values))
	for _, value := range values {
		name := toolName(value)
		if name == "" {
			continue
		}
		out = append(out, extractedTool{Name: name, CallID: toolCallID(value), Payload: value})
	}
	return out
}

func toolName(value any) string {
	if name := strings.TrimSpace(stringValue(value, "name")); name != "" {
		return name
	}
	if fn, ok := mapValue(value, "function"); ok {
		if name := strings.TrimSpace(stringValue(fn, "name")); name != "" {
			return name
		}
	}
	if tool, ok := mapValue(value, "tool"); ok {
		if name := strings.TrimSpace(stringValue(tool, "name")); name != "" {
			return name
		}
	}
	return ""
}

func toolCallID(value any) string {
	for _, key := range []string{"tool_call_id", "tool_use_id", "call_id", "id"} {
		if candidate := strings.TrimSpace(stringValue(value, key)); candidate != "" {
			return candidate
		}
	}
	return ""
}

func normalizeProjectionValue(value any) any {
	if value == nil {
		return nil
	}
	switch v := value.(type) {
	case []byte:
		if len(v) == 0 {
			return nil
		}
		var out any
		if json.Unmarshal(v, &out) == nil {
			return out
		}
		return string(v)
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil
		}
		var out any
		if json.Unmarshal([]byte(trimmed), &out) == nil {
			return out
		}
		return trimmed
	default:
		return v
	}
}

func mapValue(value any, key string) (any, bool) {
	m, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := m[key]
	return v, ok
}

func sliceValueFromMap(value any, key string) ([]any, bool) {
	raw, ok := mapValue(value, key)
	if !ok {
		return nil, false
	}
	items, ok := raw.([]any)
	return items, ok
}

func stringValue(value any, key string) string {
	raw, ok := mapValue(value, key)
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return v
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
