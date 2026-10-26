package kiro

import "encoding/json"

// PayloadSizeDiagnostics contains sizes only; no client text, tool names or IDs.
// Before/After bracket truncation, after protocol conversion and normalization.
type PayloadSizeDiagnostics struct {
	LimitBytes int                  `json:"limit_bytes"`
	Before     PayloadSizeBreakdown `json:"before_truncation"`
	After      PayloadSizeBreakdown `json:"after_truncation"`
}

// The byte components are disjoint JSON value sizes. OtherBytes includes keys,
// separators, envelopes and inference configuration. Together they sum to TotalBytes.
// History includes its tool results/images; current-message components are separate.
type PayloadSizeBreakdown struct {
	TotalBytes              int `json:"total_bytes"`
	SystemPrimingBytes      int `json:"system_priming_bytes"`
	HistoryBytes            int `json:"history_bytes"`
	CurrentTextBytes        int `json:"current_text_bytes"`
	CurrentToolsBytes       int `json:"current_tools_bytes"`
	CurrentToolResultsBytes int `json:"current_tool_results_bytes"`
	CurrentImagesBytes      int `json:"current_images_bytes"`
	OtherBytes              int `json:"other_bytes"`
	HistoryEntries          int `json:"history_entries"`
}

func jsonValueSize(value any) int {
	raw, _ := json.Marshal(value) // payload types have already passed serialization
	return len(raw)
}

func measurePayloadSize(payload *KiroPayload, hasPriming bool) PayloadSizeBreakdown {
	d := PayloadSizeBreakdown{TotalBytes: payloadByteSize(payload)}
	for i, entry := range payload.ConversationState.History {
		if hasPriming && i < 2 {
			d.SystemPrimingBytes += jsonValueSize(entry)
		} else {
			d.HistoryBytes += jsonValueSize(entry)
			d.HistoryEntries++
		}
	}
	u := &payload.ConversationState.CurrentMessage.UserInputMessage
	d.CurrentTextBytes = jsonValueSize(u.Content)
	if len(u.Images) > 0 {
		d.CurrentImagesBytes = jsonValueSize(u.Images)
	}
	if c := u.UserInputMessageContext; c != nil {
		if len(c.Tools) > 0 {
			d.CurrentToolsBytes = jsonValueSize(c.Tools)
		}
		if len(c.ToolResults) > 0 {
			d.CurrentToolResultsBytes = jsonValueSize(c.ToolResults)
		}
	}
	d.OtherBytes = d.TotalBytes - d.SystemPrimingBytes - d.HistoryBytes - d.CurrentTextBytes - d.CurrentToolsBytes - d.CurrentToolResultsBytes - d.CurrentImagesBytes
	return d
}
