package trajectory

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// sessionResponse preserves native objects. Captured terminal snapshots win over
// deltas; incomplete or unrecognized reconstruction remains explicitly qualified.
func sessionResponse(shape WireShape, body, stream json.RawMessage) (json.RawMessage, []string) {
	if m := rawObject(body); len(m) > 0 {
		if len(responseMessages(shape, body)) > 0 {
			return body, nativeResponseIssues(shape, m)
		}
		if _, ok := m["error"]; ok {
			return body, []string{"response_error"}
		}
	}
	payloads, issues := sessionSSEPayloads(stream)
	if len(payloads) == 0 {
		return body, append(issues, "missing_response")
	}
	switch shape {
	case WireAnthropicMessages:
		return anthropicSessionStream(payloads, issues)
	case WireOpenAIResponses:
		return responsesSessionStream(payloads, issues)
	case WireOpenAIChat:
		return chatSessionStream(payloads, issues)
	case WireGemini:
		return geminiSessionStream(payloads, issues)
	default:
		return body, append(issues, "unsupported_stream")
	}
}

func sessionSSEPayloads(stream json.RawMessage) ([]json.RawMessage, []string) {
	chunks := rawArray(rawObject(stream)["chunks"])
	var wire strings.Builder
	var issues []string
	for _, chunk := range chunks {
		b, err := base64.StdEncoding.DecodeString(rawString(rawObject(chunk)["raw_b64"]))
		if err != nil {
			issues = appendIssue(issues, "invalid_stream_encoding")
			continue
		}
		_, _ = wire.Write(b)
	}
	text := strings.ReplaceAll(wire.String(), "\r\n", "\n")
	var payloads []json.RawMessage
	for _, frame := range strings.Split(text, "\n\n") {
		var data []string
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if len(data) == 0 {
			continue
		}
		value := strings.Join(data, "\n")
		if value == "[DONE]" {
			payloads = append(payloads, json.RawMessage(`"[DONE]"`))
			continue
		}
		if !json.Valid([]byte(value)) {
			issues = appendIssue(issues, "invalid_stream_event")
			continue
		}
		payloads = append(payloads, json.RawMessage(value))
	}
	// Gemini can be captured as a JSON response despite stream=true.
	if len(payloads) == 0 && json.Valid([]byte(text)) {
		if array := rawArray([]byte(text)); array != nil {
			payloads = array
		} else {
			payloads = []json.RawMessage{json.RawMessage(text)}
		}
	}
	return payloads, issues
}
func appendIssue(issues []string, value string) []string {
	for _, v := range issues {
		if v == value {
			return issues
		}
	}
	return append(issues, value)
}
func mergeRaw(dst, src map[string]json.RawMessage) {
	for k, v := range src {
		dst[k] = v
	}
}
func appendRawText(dst map[string]json.RawMessage, key string, value json.RawMessage) {
	dst[key] = rawJSON(rawString(dst[key]) + rawString(value))
}
func sortedRawItems(items map[int]map[string]json.RawMessage) []json.RawMessage {
	indices := make([]int, 0, len(items))
	for i := range items {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	out := make([]json.RawMessage, 0, len(indices))
	for _, i := range indices {
		out = append(out, rawJSON(items[i]))
	}
	return out
}
func rawIndex(raw json.RawMessage) int { n, _ := strconv.Atoi(string(raw)); return n }

func anthropicSessionStream(events []json.RawMessage, issues []string) (json.RawMessage, []string) {
	message := map[string]json.RawMessage{}
	blocks := map[int]map[string]json.RawMessage{}
	args := map[int]*strings.Builder{}
	texts := map[int]map[string]*strings.Builder{}
	terminal := false
	for _, raw := range events {
		ev := rawObject(raw)
		index := rawIndex(ev["index"])
		switch rawString(ev["type"]) {
		case "message_start":
			mergeRaw(message, rawObject(ev["message"]))
		case "content_block_start":
			delete(texts, index)
			blocks[index] = rawObject(ev["content_block"])
			if blocks[index] == nil {
				blocks[index] = map[string]json.RawMessage{}
			}
		case "content_block_delta":
			delta := rawObject(ev["delta"])
			block := blocks[index]
			if block == nil {
				block = map[string]json.RawMessage{}
				blocks[index] = block
				issues = appendIssue(issues, "missing_block_start")
			}
			switch rawString(delta["type"]) {
			case "text_delta", "thinking_delta", "signature_delta":
				key := strings.TrimSuffix(rawString(delta["type"]), "_delta")
				if texts[index] == nil {
					texts[index] = map[string]*strings.Builder{}
				}
				if texts[index][key] == nil {
					texts[index][key] = &strings.Builder{}
					_, _ = texts[index][key].WriteString(rawString(block[key]))
				}
				_, _ = texts[index][key].WriteString(rawString(delta[key]))
			case "input_json_delta":
				if args[index] == nil {
					args[index] = &strings.Builder{}
				}
				_, _ = args[index].WriteString(rawString(delta["partial_json"]))
			case "citations_delta":
				citations := rawArray(block["citations"])
				block["citations"] = rawJSON(append(citations, delta["citation"]))
			default:
				issues = appendIssue(issues, "unprojected_stream_delta")
			}
		case "message_delta":
			mergeRaw(message, rawObject(ev["delta"]))
			u := rawObject(message["usage"])
			if u == nil {
				u = map[string]json.RawMessage{}
			}
			mergeRaw(u, rawObject(ev["usage"]))
			message["usage"] = rawJSON(u)
		case "message_stop":
			terminal = true
		case "error":
			message["error"] = ev["error"]
			issues = appendIssue(issues, "response_error")
		}
	}
	for index, fields := range texts {
		for key, text := range fields {
			blocks[index][key] = rawJSON(text.String())
		}
	}
	for i, builder := range args {
		arg := builder.String()
		if json.Valid([]byte(arg)) {
			blocks[i]["input"] = json.RawMessage(arg)
		} else {
			blocks[i]["partial_input_json"] = rawJSON(arg)
			issues = appendIssue(issues, "incomplete_tool_arguments")
		}
	}
	message["content"] = rawJSON(sortedRawItems(blocks))
	if !terminal {
		issues = appendIssue(issues, "incomplete_stream")
	}
	return rawJSON(message), issues
}

func responsesSessionStream(events []json.RawMessage, issues []string) (json.RawMessage, []string) {
	response := map[string]json.RawMessage{}
	items := map[int]map[string]json.RawMessage{}
	completed := map[int]bool{}
	terminal := false
	ensure := func(i int) map[string]json.RawMessage {
		if items[i] == nil {
			items[i] = map[string]json.RawMessage{}
		}
		return items[i]
	}
	for _, raw := range events {
		ev := rawObject(raw)
		index := rawIndex(ev["output_index"])
		typ := rawString(ev["type"])
		switch typ {
		case "response.created", "response.in_progress":
			mergeRaw(response, rawObject(ev["response"]))
		case "response.output_item.added":
			mergeRaw(ensure(index), rawObject(ev["item"]))
		case "response.output_item.done":
			items[index] = rawObject(ev["item"])
			if items[index] == nil {
				items[index] = map[string]json.RawMessage{}
			}
			completed[index] = true
		case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			item := ensure(index)
			field := "content"
			indexField := "content_index"
			partType := "output_text"
			if typ == "response.reasoning_summary_text.delta" {
				field = "summary"
				indexField = "summary_index"
				partType = "summary_text"
			}
			if typ == "response.reasoning_text.delta" {
				partType = "reasoning_text"
			}
			parts := rawArray(item[field])
			pi := rawIndex(ev[indexField])
			if pi < 0 || pi > len(parts) {
				issues = appendIssue(issues, "invalid_stream_index")
				continue
			}
			for len(parts) <= pi {
				parts = append(parts, rawJSON(map[string]any{"type": partType, "text": ""}))
			}
			p := rawObject(parts[pi])
			if p == nil {
				p = map[string]json.RawMessage{"type": rawJSON(partType)}
			}
			appendRawText(p, "text", ev["delta"])
			parts[pi] = rawJSON(p)
			item[field] = rawJSON(parts)
		case "response.function_call_arguments.delta":
			appendRawText(ensure(index), "arguments", ev["delta"])
		case "response.completed", "response.incomplete", "response.failed":
			terminal = true
			mergeRaw(response, rawObject(ev["response"]))
			if typ != "response.completed" {
				issues = appendIssue(issues, "response_"+strings.TrimPrefix(typ, "response."))
			}
		}
	}
	if len(rawArray(response["output"])) == 0 {
		response["output"] = rawJSON(sortedRawItems(items))
		for index := range items {
			if !completed[index] {
				issues = appendIssue(issues, "incomplete_output_item")
			}
		}
	}
	if !terminal {
		issues = appendIssue(issues, "incomplete_stream")
	}
	return rawJSON(response), issues
}

func chatSessionStream(events []json.RawMessage, issues []string) (json.RawMessage, []string) {
	response := map[string]json.RawMessage{}
	choices := map[int]map[string]json.RawMessage{}
	messages := map[int]map[string]json.RawMessage{}
	tools := map[int]map[int]map[string]json.RawMessage{}
	terminal := false
	for _, raw := range events {
		if rawString(raw) == "[DONE]" {
			terminal = true
			continue
		}
		ev := rawObject(raw)
		for k, v := range ev {
			if k != "choices" {
				response[k] = v
			}
		}
		for _, choiceRaw := range rawArray(ev["choices"]) {
			choice := rawObject(choiceRaw)
			ci := rawIndex(choice["index"])
			if choices[ci] == nil {
				choices[ci] = map[string]json.RawMessage{}
				messages[ci] = map[string]json.RawMessage{}
				tools[ci] = map[int]map[string]json.RawMessage{}
			}
			for k, v := range choice {
				if k != "delta" {
					choices[ci][k] = v
				}
			}
			delta := rawObject(choice["delta"])
			msg := messages[ci]
			for k, v := range delta {
				switch k {
				case "content", "reasoning_content", "reasoning", "refusal":
					if string(v) != "null" {
						appendRawText(msg, k, v)
					}
				case "tool_calls":
					for _, toolRaw := range rawArray(v) {
						tool := rawObject(toolRaw)
						ti := rawIndex(tool["index"])
						if tools[ci][ti] == nil {
							tools[ci][ti] = map[string]json.RawMessage{}
						}
						dst := tools[ci][ti]
						for tk, tv := range tool {
							if tk == "function" {
								fn := rawObject(dst[tk])
								if fn == nil {
									fn = map[string]json.RawMessage{}
								}
								for fk, fv := range rawObject(tv) {
									if fk == "arguments" {
										appendRawText(fn, fk, fv)
									} else {
										fn[fk] = fv
									}
								}
								dst[tk] = rawJSON(fn)
							} else if tk != "index" {
								dst[tk] = tv
							}
						}
					}
				case "reasoning_details":
					// Provider extensions vary; preserve every reported fragment instead of
					// guessing a text/cipher merge rule. Raw SSE remains the authoritative source.
					msg[k] = rawJSON(append(rawArray(msg[k]), rawArray(v)...))
					issues = appendIssue(issues, "reasoning_detail_fragments")
				default:
					msg[k] = v
				}
			}
		}
	}
	for i, choice := range choices {
		if len(tools[i]) > 0 {
			messages[i]["tool_calls"] = rawJSON(sortedRawItems(tools[i]))
		}
		if _, ok := choice["message"]; !ok {
			choice["message"] = rawJSON(messages[i])
		}
		if rawString(choice["finish_reason"]) == "" {
			issues = appendIssue(issues, "incomplete_choice")
		}
	}
	response["choices"] = rawJSON(sortedRawItems(choices))
	if !terminal {
		issues = appendIssue(issues, "incomplete_stream")
	}
	return rawJSON(response), issues
}

func geminiSessionStream(events []json.RawMessage, issues []string) (json.RawMessage, []string) {
	response := map[string]json.RawMessage{}
	candidates := map[int]map[string]json.RawMessage{}
	terminal := false
	for _, raw := range events {
		ev := rawObject(raw)
		for k, v := range ev {
			if k != "candidates" {
				response[k] = v
			}
		}
		for _, candidateRaw := range rawArray(ev["candidates"]) {
			candidate := rawObject(candidateRaw)
			i := rawIndex(candidate["index"])
			if candidates[i] == nil {
				candidates[i] = map[string]json.RawMessage{}
			}
			dst := candidates[i]
			content := rawObject(dst["content"])
			if content == nil {
				content = map[string]json.RawMessage{}
			}
			incoming := rawObject(candidate["content"])
			// Native parts preserve thoughtSignature, inlineData and function payloads;
			// chunk boundaries are retained rather than flattening multimodal content.
			parts := append(rawArray(content["parts"]), rawArray(incoming["parts"])...)
			mergeRaw(content, incoming)
			content["parts"] = rawJSON(parts)
			mergeRaw(dst, candidate)
			dst["content"] = rawJSON(content)
			if rawString(candidate["finishReason"]) != "" {
				terminal = true
			}
		}
	}
	response["candidates"] = rawJSON(sortedRawItems(candidates))
	if !terminal {
		issues = appendIssue(issues, "incomplete_stream")
	}
	return rawJSON(response), issues
}

func nativeResponseIssues(shape WireShape, response map[string]json.RawMessage) []string {
	if shape == WireOpenAIResponses {
		status := rawString(response["status"])
		if status != "" && status != "completed" {
			return []string{"response_" + status}
		}
	}
	return nil
}
