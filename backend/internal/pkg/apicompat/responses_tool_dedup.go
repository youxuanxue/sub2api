package apicompat

import (
	"encoding/json"
	"strings"
)

// Tool-merge dedupe owner for Responses-shaped tool lists.
//
// Three merge points append one tool list onto another — the carrier lift in
// internal/service/gateway_forward_as_responses.go, EffectiveResponsesTools in
// this package, and the Moonshot K3 dynamic-tool promotion in
// internal/service/openai_gateway_bridge_dispatch_tk_kimi_k3_dynamic_tools.go.
// A client that declares a tool at the top level and again in the carrier (or a
// system message) therefore used to reach the upstream with two entries of the
// same name. Observed symptoms: two `shell` entries when additional_tools
// repeats a top-level tool, and two `web_search` entries at the Anthropic exit
// where responses_to_anthropic_request.go maps {"type":"web_search"} onto
// {"type":"web_search_20250305","name":"web_search"} beside a client function
// of that name.
//
// Key shape mirrors grokResponsesToolDedupKey in
// internal/service/openai_gateway_grok.go, the one merge point that already got
// this right: identity is type+name, with a whole-JSON fallback for entries
// that carry neither. First writer wins, so the client's own declaration is the
// one that survives and the order of the kept entries is unchanged.
//
// Narrow by design: this collapses exact duplicates only. It is not the owner
// for cross-kind conflicts such as a `custom` tool colliding with a `function`
// of the same name, which ValidateResponsesClientTools in
// responses_client_tools.go already rejects.

// ResponsesToolDedupKey returns the identity used to collapse duplicate
// Responses tool declarations.
func ResponsesToolDedupKey(tool ResponsesTool) string {
	toolType := strings.TrimSpace(tool.Type)
	name := strings.TrimSpace(tool.Name)
	if toolType != "" && name != "" {
		return "type:" + toolType + "\x00name:" + name
	}
	if toolType != "" {
		return "type:" + toolType
	}
	if name != "" {
		return "name:" + name
	}
	if raw, err := json.Marshal(tool); err == nil {
		return "json:" + string(raw)
	}
	return ""
}

// AppendResponsesToolsDeduped appends extra onto base, skipping any entry whose
// ResponsesToolDedupKey already appears. It reports whether anything was added.
func AppendResponsesToolsDeduped(base []ResponsesTool, extra ...ResponsesTool) ([]ResponsesTool, bool) {
	if len(extra) == 0 {
		return base, false
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	for _, tool := range base {
		seen[ResponsesToolDedupKey(tool)] = struct{}{}
	}
	appended := false
	for _, tool := range extra {
		key := ResponsesToolDedupKey(tool)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		base = append(base, tool)
		appended = true
	}
	return base, appended
}

// RawResponsesToolDedupKey is the untyped-map counterpart, for merge points
// that work on decoded JSON instead of ResponsesTool values. Entries that are
// not JSON objects keep their own identity so they are never collapsed.
func RawResponsesToolDedupKey(raw any) string {
	tool, ok := raw.(map[string]any)
	if !ok {
		if encoded, err := json.Marshal(raw); err == nil {
			return "json:" + string(encoded)
		}
		return ""
	}
	toolType := strings.TrimSpace(stringValue(tool["type"]))
	name := strings.TrimSpace(stringValue(tool["name"]))
	if name == "" {
		// Chat Completions nests the declaration as {"function":{"name":...}}.
		if fn, ok := tool["function"].(map[string]any); ok {
			name = strings.TrimSpace(stringValue(fn["name"]))
		}
	}
	if toolType != "" && name != "" {
		return "type:" + toolType + "\x00name:" + name
	}
	if toolType != "" {
		return "type:" + toolType
	}
	if name != "" {
		return "name:" + name
	}
	if encoded, err := json.Marshal(tool); err == nil {
		return "json:" + string(encoded)
	}
	return ""
}

// AppendRawResponsesToolsDeduped is AppendResponsesToolsDeduped for decoded
// JSON tool lists.
func AppendRawResponsesToolsDeduped(base []any, extra ...any) ([]any, bool) {
	if len(extra) == 0 {
		return base, false
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	for _, tool := range base {
		seen[RawResponsesToolDedupKey(tool)] = struct{}{}
	}
	appended := false
	for _, tool := range extra {
		key := RawResponsesToolDedupKey(tool)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		base = append(base, tool)
		appended = true
	}
	return base, appended
}
