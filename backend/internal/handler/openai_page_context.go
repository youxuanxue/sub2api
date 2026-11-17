package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var codexSelectedPageName = regexp.MustCompile(`^codex_apps_selected_page_part_([1-9][0-9]*)_of_([1-9][0-9]*)$`)

// normalizeCodexPageContext converts Codex standalone named function_call_output
// items that lack call_id into untrusted user text before HTTP Responses
// validation. Desktop page snapshots (codex_apps_*) and VSCode ide_context
// injections share this shape (Codex #39782 / upstream #8857/#8635/#8622).
//
// Real tool results always carry call_id; leaving those untouched preserves
// pairing. Page-context names keep the existing snapshot validation so malformed
// page payloads still fail closed with the call_id error.
func normalizeCodexPageContext(body []byte) ([]byte, bool) {
	if !mayContainStandaloneCallOutput(body) || !hasUniqueJSONMembers(body) {
		return body, false
	}
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil {
		return body, false
	}
	var input []json.RawMessage
	if json.Unmarshal(request["input"], &input) != nil {
		return body, false
	}
	changed := false
	for i, raw := range input {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		readString := func(key string) string {
			var value string
			_ = json.Unmarshal(item[key], &value)
			return value
		}
		if readString("type") != "function_call_output" || readString("namespace") != "agent" {
			continue
		}
		if callID, exists := item["call_id"]; exists {
			var value string
			if bytes.Equal(bytes.TrimSpace(callID), []byte("null")) || json.Unmarshal(callID, &value) != nil || strings.TrimSpace(value) != "" {
				continue
			}
		}
		name, output := readString("name"), readString("output")
		if strings.TrimSpace(name) == "" {
			continue
		}
		// Page snapshots keep strict validation; other agent-namespace named
		// standalone outputs (VSCode ide_context, etc.) convert as user text.
		if isCodexPageContextName(name) {
			if !validCodexPageContext(name, output) {
				continue
			}
		} else if !isCodexStandaloneContextName(name) {
			// Unknown real-looking tool names without call_id stay fail-closed.
			continue
		}
		message := map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]string{{
				"type": "input_text",
				"text": "<external_" + name + ">\n" + output + "\n</external_" + name + ">",
			}},
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			return body, false
		}
		input[i] = encoded
		changed = true
	}
	if !changed {
		return body, false
	}
	encodedInput, err := json.Marshal(input)
	if err != nil {
		return body, false
	}
	request["input"] = encodedInput
	encoded, err := json.Marshal(request)
	if err != nil {
		return body, false
	}
	return encoded, true
}

func mayContainStandaloneCallOutput(body []byte) bool {
	// Fast reject: standalone named outputs need a name field near function_call_output.
	return bytes.Contains(body, []byte(`"function_call_output"`)) && bytes.Contains(body, []byte(`"name"`))
}

func isCodexPageContextName(name string) bool {
	return name == "codex_apps_open_page" || strings.HasPrefix(name, "codex_apps_selected_page_part_")
}

// isCodexStandaloneContextName recognizes non-page IDE/context injections that
// Codex clients emit as named function_call_output without call_id on HTTP.
func isCodexStandaloneContextName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "ide_context") || strings.HasPrefix(lower, "ide_") {
		return true
	}
	if strings.HasPrefix(lower, "codex_apps_") {
		return true
	}
	return false
}

func validCodexPageContext(name, output string) bool {
	if name == "codex_apps_open_page" {
		return validCodexPageSnapshot(output)
	}
	parts := codexSelectedPageName.FindStringSubmatch(name)
	if len(parts) != 3 {
		return false
	}
	part, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	total, err := strconv.Atoi(parts[2])
	if err != nil || part > total {
		return false
	}
	header, snapshot, ok := strings.Cut(output, "\n")
	if !ok || strings.TrimSuffix(header, "\r") != fmt.Sprintf("Companion Page snapshot part %d of %d:", part, total) || strings.TrimSpace(snapshot) == "" {
		return false
	}
	// A multi-part snapshot may split inside a JSON string; preserve each chunk verbatim.
	return total > 1 || validCodexPageSnapshot(snapshot)
}

func validCodexPageSnapshot(text string) bool {
	if !hasUniqueJSONMembers([]byte(text)) {
		return false
	}
	var snapshot map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &snapshot) != nil {
		return false
	}
	id, exists := snapshot["page_id"]
	if !exists {
		return false
	}
	if bytes.Equal(bytes.TrimSpace(id), []byte("null")) {
		return true
	}
	var value string
	return json.Unmarshal(id, &value) == nil && strings.TrimSpace(value) != ""
}
