package service

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Compaction replays hosted search results with no executable tools. ChatGPT's
// OAuth endpoint still requires their declaration or emits "response protection
// is unavailable". Only repair no-tools/explicit-none requests: adding a tool to
// an active tool set could grant a capability the caller did not authorize.
// API-key and /responses/compact callers are excluded at the call sites.
func normalizeOpenAIOAuthWebSearchHistory(body []byte, lite bool) ([]byte, bool, error) {
	if !bytes.Contains(body, []byte("web_search_call")) {
		return body, false, nil
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false, nil
	}
	items := input.Array()
	hasHistory, hasTools, hasSearch := false, false, false
	inspectTools := func(tools gjson.Result) bool {
		if tools.Exists() && tools.Type != gjson.Null && !tools.IsArray() {
			return false // Preserve invalid declarations for upstream validation.
		}
		for _, tool := range tools.Array() {
			hasTools = true
			switch tool.Get("type").String() {
			case "web_search", "web_search_preview", "web_search_preview_2025_03_11":
				hasSearch = true
			}
		}
		return true
	}
	if !inspectTools(gjson.GetBytes(body, "tools")) {
		return body, false, nil
	}
	for _, item := range items {
		switch item.Get("type").String() {
		case "web_search_call":
			hasHistory = true
		case "additional_tools":
			if !inspectTools(item.Get("tools")) {
				return body, false, nil
			}
		}
	}
	if !hasHistory || hasSearch {
		return body, false, nil
	}
	choice := gjson.GetBytes(body, "tool_choice")
	if hasTools {
		if choice.Type != gjson.String || choice.String() != "none" {
			return body, false, nil
		}
	} else if choice.Exists() && choice.Type != gjson.Null &&
		(choice.Type != gjson.String || (choice.String() != "none" && choice.String() != "auto")) {
		return body, false, nil
	}

	tool := `{"type":"web_search","external_web_access":false}`
	var next []byte
	var err error
	if lite {
		// Keep the trigger last; do not reserialize history or large integers.
		at := len(items)
		for i, item := range items {
			if item.Get("type").String() == "compaction_trigger" {
				at = i
				break
			}
		}
		raw := make([]string, 0, len(items)+1)
		for i := 0; i <= len(items); i++ {
			if i == at {
				raw = append(raw, `{"type":"additional_tools","role":"developer","tools":[`+tool+`]}`)
			}
			if i < len(items) {
				raw = append(raw, items[i].Raw)
			}
		}
		next, err = sjson.SetRawBytes(body, "input", []byte("["+strings.Join(raw, ",")+"]"))
	} else if tools := gjson.GetBytes(body, "tools"); tools.IsArray() {
		next, err = sjson.SetRawBytes(body, "tools.-1", []byte(tool))
	} else {
		next, err = sjson.SetRawBytes(body, "tools", []byte("["+tool+"]"))
	}
	if err != nil {
		return body, false, err
	}
	next, err = sjson.SetBytes(next, "tool_choice", "none")
	if err != nil {
		return body, false, err
	}
	return next, true, nil
}

// Match only an error message, never arbitrary echoed output/history. This is
// shared across accounts, but remains an upstream failure (not a client 400).
func isOpenAIResponseProtectionUnavailable(message string, payload []byte) bool {
	for _, candidate := range []string{message,
		gjson.GetBytes(payload, "response.error.message").String(),
		gjson.GetBytes(payload, "error.message").String()} {
		if strings.EqualFold(strings.TrimSpace(candidate), "response protection is unavailable") {
			return true
		}
	}
	return false
}
