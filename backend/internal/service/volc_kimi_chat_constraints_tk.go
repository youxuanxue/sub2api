package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// normalizeVolcKimiChatRequest owns the evidenced Volc Agent Plan Kimi K3
// thinking/tool contract while presenting a Moonshot-compatible public surface
// for kimi-k3. See docs/approved/volc-kimi-chat-compatibility.md.
func normalizeVolcKimiChatRequest(body []byte) ([]byte, error) {
	if err := validateVolcKimiRequestLimits(body); err != nil {
		return nil, err
	}
	shaped, err := normalizeVolcKimiInboundMessages(body)
	if err != nil {
		return nil, err
	}
	shaped, err = normalizeVolcKimiThinking(shaped)
	if err != nil {
		return nil, err
	}
	shaped, err = pinVolcKimiThinkingTemperature(shaped)
	if err != nil {
		return nil, err
	}
	shaped, err = normalizeVolcKimiAllowedTools(shaped)
	if err != nil {
		return nil, err
	}
	// A single allowed function + required is equivalent to a named selection,
	// and works with this supply's default thinking (named selection returns 400).
	if gjson.GetBytes(shaped, "tool_choice.type").String() != "function" {
		return shaped, nil
	}
	if gjson.GetBytes(shaped, "function_call").Exists() || gjson.GetBytes(shaped, "functions").Exists() {
		return nil, newOpenAIInvalidParameterError("tool_choice", "cannot combine named tool_choice with legacy function fields")
	}
	shaped, err = normalizeChatToolConstraints(shaped)
	if err != nil {
		var inv *OpenAIInvalidParameterError
		if errors.As(err, &inv) {
			return nil, err
		}
		return nil, newOpenAIInvalidParameterError("tool_choice", err.Error())
	}
	return sjson.SetBytes(shaped, "tool_choice", "required")
}

// normalizeVolcKimiInboundMessages rewrites only the message and tool shapes
// that Volc Agent Plan kimi-k3 rejected on cn-prod (2026-10-23) and that a
// direct replay then accepted:
//   - a JSON object in messages.content is either one content part (wrap it in
//     an array) or an ordinary object (send its JSON text as a string);
//   - a tool message whose tool_call_id is missing, blank, or absent from
//     assistant tool_calls returns 400 (param messages). A matched tool
//     message is kept. Historical tool_calls that lack function.name stay a
//     separate 400 and are not rewritten into a user message;
//   - a function tool whose name is empty or whitespace returns 400 (param
//     tools). Names are not dropped so the request can continue;
//   - a string video_url becomes {"url": ...} (2026-11-05);
//   - a text part with text "" is dropped (2026-11-06);
//   - thinking / reasoning_text / prompt_cache_breakpoint content parts are
//     dropped (2026-11-18/19); refusal / summary_text likewise (2026-11-20
//     cn-prod rt@tk.com; direct Volc sanitize→200);
//   - type=video / input_video is rewritten to video_url when a URL is present
//     (2026-11-18; input_video 2026-11-20 direct Volc enum-clear evidence);
//   - audio_url / input_audio parts return 400 (Volc kimi cannot deliver them);
//   - type=file is normalized (2026-11-19 cn-prod rt@tk.com): missing/empty
//     file object or 0/>1 of file_id|file_url|file_data → local 400; video/*
//     file_data/file_url → video_url; text/* data → text part; non-PDF binary
//     → local 400; real PDF / file_id pass through;
//   - an assistant text message inserted between assistant.tool_calls and the
//     matching tool result(s) is dropped (2026-11-17 cn-prod tokensea_online;
//     2026-11-18 also drops assistant text between parallel tool results);
//   - a message whose content is JSON null is rewritten to "" (2026-11-18
//     cn-prod rt@tk.com). Volc returns missing `messages.content` for null;
//     empty string is accepted. Dynamic-tool system messages with null content
//     are promoted earlier and never reach this rewrite.
//
// Matched tool results, string content, and content-part arrays are unchanged
// except for the rewrites above. Reachable video parts are not stripped.
// Reachability is a separate preflight: rejectUnreachableVolcKimiVideoURLs.
// Orphan tool messages stay 400 (KVV 2026-11-13); they are not rewritten.
func normalizeVolcKimiInboundMessages(body []byte) ([]byte, error) {
	shaped, err := coerceVolcKimiNullMessageContent(body)
	if err != nil {
		return nil, err
	}
	shaped, err = normalizeVolcKimiMessageContents(shaped)
	if err != nil {
		return nil, err
	}
	shaped, err = dropVolcKimiInterleavedAssistantText(shaped)
	if err != nil {
		return nil, err
	}
	shaped, err = normalizeVolcKimiOrphanToolMessages(shaped)
	if err != nil {
		return nil, err
	}
	return normalizeVolcKimiEmptyFunctionNames(shaped)
}

// dropVolcKimiInterleavedAssistantText removes assistant messages that have no
// tool_calls while a tool round-trip is still open: after assistant.tool_calls
// until every id from that turn has a matching tool result. Volc Agent Plan
// rejects that order with InvalidParameter / tool_call_id is not found
// (cn-prod 2026-11-17/18, tokensea_online Claude-Code-style trajectories).
// Live ablation: dropping those interleaved turns recovered 200; tool schemas
// alone were not the cause. Parallel tool results may have assistant text
// between them — those turns are dropped too. Ordinary assistant text outside
// an open tool round-trip is kept. user/system messages end the open round
// without being dropped. Orphan tool ids stay a separate 400.
func dropVolcKimiInterleavedAssistantText(body []byte) ([]byte, error) {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body, nil
	}
	arr := msgs.Array()
	if len(arr) == 0 {
		return body, nil
	}
	kept := make([]json.RawMessage, 0, len(arr))
	changed := false
	for i := 0; i < len(arr); {
		msg := arr[i]
		if !volcKimiAssistantWithToolCalls(msg) {
			kept = append(kept, json.RawMessage(msg.Raw))
			i++
			continue
		}
		kept = append(kept, json.RawMessage(msg.Raw))
		pending := volcKimiToolCallIDs(msg)
		i++
		for i < len(arr) && len(pending) > 0 {
			nxt := arr[i]
			role := strings.ToLower(strings.TrimSpace(nxt.Get("role").String()))
			switch {
			case role == "tool":
				delete(pending, strings.TrimSpace(nxt.Get("tool_call_id").String()))
				kept = append(kept, json.RawMessage(nxt.Raw))
				i++
			case volcKimiAssistantWithToolCalls(nxt):
				// New tool round; leave nxt for the outer loop.
				pending = nil
			case role == "assistant":
				changed = true
				i++
			default:
				// user/system: stop the open round; outer loop keeps nxt.
				pending = nil
			}
		}
	}
	if !changed {
		return body, nil
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "messages", encoded)
}

func volcKimiAssistantWithToolCalls(msg gjson.Result) bool {
	if !strings.EqualFold(msg.Get("role").String(), "assistant") {
		return false
	}
	calls := msg.Get("tool_calls")
	return calls.IsArray() && len(calls.Array()) > 0
}

// coerceVolcKimiNullMessageContent rewrites messages.content JSON null to "".
// cn-prod 2026-11-18 rt@tk.com: probes that emitted {"role":"system","content":null}
// (with or without top-level tools) got Volc 400 missing `messages.content`.
// The same prompts with content "" succeed. Absent content keys are unchanged.
func coerceVolcKimiNullMessageContent(body []byte) ([]byte, error) {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body, nil
	}
	shaped := body
	for i, msg := range msgs.Array() {
		content := msg.Get("content")
		if !content.Exists() || content.Type != gjson.Null {
			continue
		}
		next, err := sjson.SetBytes(shaped, fmt.Sprintf("messages.%d.content", i), "")
		if err != nil {
			return nil, err
		}
		shaped = next
	}
	return shaped, nil
}

func volcKimiToolCallIDs(msg gjson.Result) map[string]struct{} {
	out := make(map[string]struct{})
	for _, call := range msg.Get("tool_calls").Array() {
		if id := strings.TrimSpace(call.Get("id").String()); id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func normalizeVolcKimiMessageContents(body []byte) ([]byte, error) {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body, nil
	}
	shaped := body
	var err error
	for i, msg := range msgs.Array() {
		content := msg.Get("content")
		if !content.IsObject() {
			continue
		}
		path := fmt.Sprintf("messages.%d.content", i)
		if volcKimiContentPartType(content.Get("type").String()) {
			wrapped, marshalErr := json.Marshal([]json.RawMessage{json.RawMessage(content.Raw)})
			if marshalErr != nil {
				return nil, marshalErr
			}
			shaped, err = sjson.SetRawBytes(shaped, path, wrapped)
		} else {
			encoded, marshalErr := json.Marshal(content.Raw)
			if marshalErr != nil {
				return nil, marshalErr
			}
			shaped, err = sjson.SetRawBytes(shaped, path, encoded)
		}
		if err != nil {
			return nil, err
		}
	}
	shaped, err = dropVolcKimiEmptyTextParts(shaped)
	if err != nil {
		return nil, err
	}
	shaped, err = dropVolcKimiReasoningContentParts(shaped)
	if err != nil {
		return nil, err
	}
	if err = rejectVolcKimiAudioContentParts(shaped); err != nil {
		return nil, err
	}
	shaped, err = rewriteVolcKimiVideoTypeParts(shaped)
	if err != nil {
		return nil, err
	}
	shaped, err = wrapVolcKimiStringVideoURLs(shaped)
	if err != nil {
		return nil, err
	}
	return normalizeVolcKimiFileContentParts(shaped)
}

const (
	volcKimiAudioUnsupportedMessage = "audio input is not supported for kimi on this service"
)

// dropVolcKimiReasoningContentParts removes messages.content parts whose type is
// thinking, reasoning_text, prompt_cache_breakpoint, refusal, or summary_text.
// Volc Agent Plan only accepts text / image_url / video_url / file (and rejects
// those types with InvalidParameter). Clients often replay prior reasoning,
// refusals, summaries, or cache markers into history; the parts carry no
// deliverable user input, so dropping them keeps meaning (same posture as empty
// text parts). Top-level thinking.type / reasoning_effort are unchanged.
// Evidence 2026-11-20: direct Volc raw→400 / drop→200 for refusal + summary_text.
func dropVolcKimiReasoningContentParts(body []byte) ([]byte, error) {
	shaped := body
	var err error
	for i, msg := range gjson.GetBytes(body, "messages").Array() {
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		parts := content.Array()
		kept := make([]json.RawMessage, 0, len(parts))
		for _, part := range parts {
			if volcKimiReasoningContentPart(part) {
				continue
			}
			kept = append(kept, json.RawMessage(part.Raw))
		}
		if len(kept) == len(parts) {
			continue
		}
		path := fmt.Sprintf("messages.%d.content", i)
		if len(kept) == 0 {
			shaped, err = sjson.SetBytes(shaped, path, "")
		} else {
			encoded, marshalErr := json.Marshal(kept)
			if marshalErr != nil {
				return nil, marshalErr
			}
			shaped, err = sjson.SetRawBytes(shaped, path, encoded)
		}
		if err != nil {
			return nil, err
		}
	}
	return shaped, nil
}

func volcKimiReasoningContentPart(part gjson.Result) bool {
	switch strings.ToLower(strings.TrimSpace(part.Get("type").String())) {
	case "thinking", "reasoning_text", "prompt_cache_breakpoint", "refusal", "summary_text":
		return true
	default:
		return false
	}
}

// rejectVolcKimiAudioContentParts returns a field-level 400 when a content part
// uses audio_url or input_audio. Volc kimi cannot deliver audio input
// (upstream: "No available endpoints support input audio"); a local parameter
// error matches the logprobs undeliverable-field posture and avoids a supplier
// trace. Parts are not stripped — that would silently drop client media.
func rejectVolcKimiAudioContentParts(body []byte) error {
	for _, msg := range gjson.GetBytes(body, "messages").Array() {
		content := msg.Get("content")
		switch {
		case content.IsArray():
			for _, part := range content.Array() {
				if volcKimiAudioContentPart(part) {
					return newOpenAIInvalidParameterError("messages.content", volcKimiAudioUnsupportedMessage)
				}
			}
		case content.IsObject():
			if volcKimiAudioContentPart(content) {
				return newOpenAIInvalidParameterError("messages.content", volcKimiAudioUnsupportedMessage)
			}
		}
	}
	return nil
}

func volcKimiAudioContentPart(part gjson.Result) bool {
	switch strings.ToLower(strings.TrimSpace(part.Get("type").String())) {
	case "audio_url", "input_audio":
		return true
	default:
		return part.Get("audio_url").Exists() || part.Get("input_audio").Exists()
	}
}

// rewriteVolcKimiVideoTypeParts rewrites type=video / input_video to
// type=video_url when a URL is present (video_url string/object, video.url, or
// top-level url). Volc rejects those aliases ("supported values are: text,
// image_url, video_url, …"). Parts without a URL are left for upstream / other
// checks. Media validity / reachability stay separate (do not strip).
func rewriteVolcKimiVideoTypeParts(body []byte) ([]byte, error) {
	shaped := body
	var err error
	for i, msg := range gjson.GetBytes(body, "messages").Array() {
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		for j, part := range content.Array() {
			kind := strings.ToLower(strings.TrimSpace(part.Get("type").String()))
			if kind != "video" && kind != "input_video" {
				continue
			}
			url, ok := volcKimiVideoPartURL(part)
			if !ok {
				continue
			}
			base := fmt.Sprintf("messages.%d.content.%d", i, j)
			shaped, err = sjson.SetBytes(shaped, base+".type", "video_url")
			if err != nil {
				return nil, err
			}
			shaped, err = sjson.SetBytes(shaped, base+".video_url", map[string]string{"url": url})
			if err != nil {
				return nil, err
			}
			if part.Get("video").Exists() {
				shaped, err = sjson.DeleteBytes(shaped, base+".video")
				if err != nil {
					return nil, err
				}
			}
			if part.Get("url").Exists() && !part.Get("video_url").Exists() {
				// Bare url was the source; leave video_url object only.
				shaped, err = sjson.DeleteBytes(shaped, base+".url")
				if err != nil {
					return nil, err
				}
			}
		}
	}
	return shaped, nil
}

func volcKimiVideoPartURL(part gjson.Result) (string, bool) {
	if raw, ok := volcKimiVideoURLValue(part); ok {
		if strings.TrimSpace(raw) != "" {
			return strings.TrimSpace(raw), true
		}
	}
	if v := part.Get("video"); v.IsObject() {
		if u := strings.TrimSpace(v.Get("url").String()); u != "" {
			return u, true
		}
	}
	if u := strings.TrimSpace(part.Get("url").String()); u != "" {
		return u, true
	}
	return "", false
}

// dropVolcKimiEmptyTextParts removes content parts of type text that carry no
// input: text == "", text == null, or the text field missing entirely. Volc
// rejects "" with an unnamed InvalidParameter (cn-prod 2026-11-06) and rejects
// missing/null text with "missing messages.content.text" (cn-prod 2026-11-17,
// tokensea_online). Such parts add no input, so dropping them keeps the
// meaning. If no parts remain, the content becomes "", which Volc accepts.
// Whitespace-only text and string content "" are accepted upstream and kept.
func dropVolcKimiEmptyTextParts(body []byte) ([]byte, error) {
	shaped := body
	var err error
	for i, msg := range gjson.GetBytes(body, "messages").Array() {
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		parts := content.Array()
		kept := make([]json.RawMessage, 0, len(parts))
		for _, part := range parts {
			if volcKimiTextPartHasNoInput(part) {
				continue
			}
			kept = append(kept, json.RawMessage(part.Raw))
		}
		if len(kept) == len(parts) {
			continue
		}
		path := fmt.Sprintf("messages.%d.content", i)
		if len(kept) == 0 {
			shaped, err = sjson.SetBytes(shaped, path, "")
		} else {
			encoded, marshalErr := json.Marshal(kept)
			if marshalErr != nil {
				return nil, marshalErr
			}
			shaped, err = sjson.SetRawBytes(shaped, path, encoded)
		}
		if err != nil {
			return nil, err
		}
	}
	return shaped, nil
}

// volcKimiTextPartHasNoInput reports a type=text part with empty, null, or
// absent text. Non-text parts and whitespace-only strings return false.
func volcKimiTextPartHasNoInput(part gjson.Result) bool {
	if !strings.EqualFold(strings.TrimSpace(part.Get("type").String()), "text") {
		return false
	}
	text := part.Get("text")
	if !text.Exists() || text.Type == gjson.Null {
		return true
	}
	return text.Type == gjson.String && text.Str == ""
}

// wrapVolcKimiStringVideoURLs rewrites a string video_url part to the object
// form. Moonshot documents the string as equivalent to video_url.url; Volc
// rejects it ("expected an object", cn-prod 2026-11-05).
func wrapVolcKimiStringVideoURLs(body []byte) ([]byte, error) {
	shaped := body
	var err error
	for i, msg := range gjson.GetBytes(body, "messages").Array() {
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		for j, part := range content.Array() {
			video := part.Get("video_url")
			if video.Type != gjson.String {
				continue
			}
			path := fmt.Sprintf("messages.%d.content.%d.video_url", i, j)
			shaped, err = sjson.SetBytes(shaped, path, map[string]string{"url": video.String()})
			if err != nil {
				return nil, err
			}
		}
	}
	return shaped, nil
}

func volcKimiContentPartType(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "text", "image_url", "video_url", "video", "input_video", "input_audio", "audio_url", "file", "thinking", "reasoning_text", "prompt_cache_breakpoint", "refusal", "summary_text":
		// video / input_video / thinking / reasoning_text / prompt_cache /
		// refusal / summary_text / audio_* are recognized so a lone object
		// content is wrapped into a part array before drop/rewrite/reject.
		return true
	default:
		return false
	}
}

const (
	volcKimiFileMissingMessage = "messages.content.file is required for type=file"
	volcKimiFileXorMessage     = "exactly one of file_id, file_url, or file_data must be set"
	volcKimiFilePDFOnlyMessage = "file input must be a PDF"
)

// normalizeVolcKimiFileContentParts owns Volc Agent Plan type=file shape
// compatibility (cn-prod 2026-11-19 rt@tk.com / vap-1106-4 / vap-1105-51):
//   - type=file without a nested file object (or with null/empty) → local 400;
//   - flat file_id/file_url/file_data on the part are nested under file first;
//   - file must carry exactly one of file_id, file_url, file_data → else 400;
//   - video/* data URI or video-looking file_url → rewrite to video_url
//     (same posture as type=video rewrite; data: size gate stays downstream);
//   - text/* data URI → decode to a text part (Moonshot-style extract);
//   - application/pdf or %PDF magic → keep as file;
//   - other non-PDF payloads → local 400 (do not strip; do not invent success).
//
// file_id is always forwarded. Parts are never silently dropped when they
// carried a payload. KVV sampling/tools/orphan rules are unchanged.
func normalizeVolcKimiFileContentParts(body []byte) ([]byte, error) {
	shaped := body
	var err error
	for i, msg := range gjson.GetBytes(body, "messages").Array() {
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		parts := content.Array()
		kept := make([]json.RawMessage, 0, len(parts))
		changed := false
		for _, part := range parts {
			out, rewrite, partErr := normalizeVolcKimiFilePart(part)
			if partErr != nil {
				return nil, partErr
			}
			if rewrite {
				changed = true
			}
			kept = append(kept, out)
		}
		if !changed {
			continue
		}
		path := fmt.Sprintf("messages.%d.content", i)
		encoded, marshalErr := json.Marshal(kept)
		if marshalErr != nil {
			return nil, marshalErr
		}
		shaped, err = sjson.SetRawBytes(shaped, path, encoded)
		if err != nil {
			return nil, err
		}
	}
	return shaped, nil
}

func normalizeVolcKimiFilePart(part gjson.Result) (json.RawMessage, bool, error) {
	typ := strings.ToLower(strings.TrimSpace(part.Get("type").String()))
	if typ != "file" {
		// Only the evidenced type=file shape is normalized. A nested "file"
		// object on another part type is left alone.
		return json.RawMessage(part.Raw), false, nil
	}
	fileNode := part.Get("file")
	hasFileObj := fileNode.Exists() && fileNode.Type != gjson.Null
	hasFlat := volcKimiFileSourceFieldSet(part)

	fileMap := map[string]any{}
	if hasFileObj {
		if !fileNode.IsObject() {
			return nil, false, newOpenAIInvalidParameterError("messages.content.file", volcKimiFileMissingMessage)
		}
		if err := json.Unmarshal([]byte(fileNode.Raw), &fileMap); err != nil {
			return nil, false, newOpenAIInvalidParameterError("messages.content.file", volcKimiFileMissingMessage)
		}
	} else if hasFlat {
		for _, key := range []string{"file_id", "file_url", "file_data", "filename"} {
			if v := part.Get(key); v.Exists() && v.Type != gjson.Null {
				fileMap[key] = v.Value()
			}
		}
	} else {
		return nil, false, newOpenAIInvalidParameterError("messages.content.file", volcKimiFileMissingMessage)
	}

	id := volcKimiFileStringField(fileMap, "file_id")
	urlVal := volcKimiFileStringField(fileMap, "file_url")
	data := volcKimiFileStringField(fileMap, "file_data")
	setCount := 0
	for _, v := range []string{id, urlVal, data} {
		if v != "" {
			setCount++
		}
	}
	if setCount != 1 {
		return nil, false, newOpenAIInvalidParameterError("messages.content.file", volcKimiFileXorMessage)
	}

	switch {
	case id != "":
		if hasFileObj && strings.EqualFold(strings.TrimSpace(part.Get("type").String()), "file") {
			return json.RawMessage(part.Raw), false, nil
		}
		encoded, err := marshalVolcKimiFilePart(fileMap)
		if err != nil {
			return nil, false, err
		}
		return encoded, true, nil
	case data != "":
		return rewriteVolcKimiFileDataPart(part, data, fileMap, hasFileObj)
	default: // file_url
		return rewriteVolcKimiFileURLPart(part, urlVal, fileMap, hasFileObj)
	}
}

func volcKimiFileSourceFieldSet(part gjson.Result) bool {
	for _, key := range []string{"file_id", "file_url", "file_data"} {
		v := part.Get(key)
		if v.Exists() && v.Type != gjson.Null && strings.TrimSpace(v.String()) != "" {
			return true
		}
	}
	return false
}

func volcKimiFileStringField(fileMap map[string]any, key string) string {
	raw, ok := fileMap[key]
	if !ok || raw == nil {
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func marshalVolcKimiFilePart(fileMap map[string]any) (json.RawMessage, error) {
	payload := map[string]any{
		"type": "file",
		"file": fileMap,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

func rewriteVolcKimiFileDataPart(part gjson.Result, data string, fileMap map[string]any, hasFileObj bool) (json.RawMessage, bool, error) {
	kind, payload, ok := classifyVolcKimiFileData(data)
	switch kind {
	case volcKimiFileKindVideo:
		encoded, err := json.Marshal(map[string]any{
			"type":      "video_url",
			"video_url": map[string]string{"url": data},
		})
		if err != nil {
			return nil, false, err
		}
		return json.RawMessage(encoded), true, nil
	case volcKimiFileKindText:
		if !ok {
			return nil, false, newOpenAIInvalidParameterError("messages.content.file", volcKimiFilePDFOnlyMessage)
		}
		encoded, err := json.Marshal(map[string]any{
			"type": "text",
			"text": payload,
		})
		if err != nil {
			return nil, false, err
		}
		return json.RawMessage(encoded), true, nil
	case volcKimiFileKindPDF:
		if hasFileObj && strings.EqualFold(strings.TrimSpace(part.Get("type").String()), "file") {
			return json.RawMessage(part.Raw), false, nil
		}
		encoded, err := marshalVolcKimiFilePart(fileMap)
		if err != nil {
			return nil, false, err
		}
		return encoded, true, nil
	default:
		return nil, false, newOpenAIInvalidParameterError("messages.content.file", volcKimiFilePDFOnlyMessage)
	}
}

func rewriteVolcKimiFileURLPart(part gjson.Result, rawURL string, fileMap map[string]any, hasFileObj bool) (json.RawMessage, bool, error) {
	lower := strings.ToLower(strings.TrimSpace(rawURL))
	if strings.HasPrefix(lower, "data:") {
		return rewriteVolcKimiFileDataPart(part, rawURL, fileMap, hasFileObj)
	}
	if volcKimiFileURLLooksLikeVideo(lower) {
		encoded, err := json.Marshal(map[string]any{
			"type":      "video_url",
			"video_url": map[string]string{"url": rawURL},
		})
		if err != nil {
			return nil, false, err
		}
		return json.RawMessage(encoded), true, nil
	}
	if volcKimiFileURLLooksLikeNonPDF(lower) {
		return nil, false, newOpenAIInvalidParameterError("messages.content.file", volcKimiFilePDFOnlyMessage)
	}
	if hasFileObj && strings.EqualFold(strings.TrimSpace(part.Get("type").String()), "file") {
		return json.RawMessage(part.Raw), false, nil
	}
	encoded, err := marshalVolcKimiFilePart(fileMap)
	if err != nil {
		return nil, false, err
	}
	return encoded, true, nil
}

type volcKimiFileKind int

const (
	volcKimiFileKindPDF volcKimiFileKind = iota + 1
	volcKimiFileKindVideo
	volcKimiFileKindText
	volcKimiFileKindOther
)

func classifyVolcKimiFileData(raw string) (volcKimiFileKind, string, bool) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(raw), "data:") {
		meta, payload, cutOK := strings.Cut(raw, ",")
		if !cutOK {
			return volcKimiFileKindOther, "", false
		}
		metaLower := strings.ToLower(strings.TrimSpace(meta))
		rest := strings.TrimSpace(strings.TrimPrefix(metaLower, "data:"))
		mediaType, paramPart, _ := strings.Cut(rest, ";")
		mediaType = strings.TrimSpace(mediaType)
		isBase64 := false
		for _, param := range strings.Split(paramPart, ";") {
			if strings.TrimSpace(param) == "base64" {
				isBase64 = true
				break
			}
		}
		switch {
		case strings.HasPrefix(mediaType, "video/"):
			return volcKimiFileKindVideo, "", true
		case strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || mediaType == "application/xml":
			text, ok := decodeVolcKimiFilePayload(payload, isBase64)
			return volcKimiFileKindText, text, ok
		case mediaType == "application/pdf" || mediaType == "":
			decoded, ok := decodeVolcKimiFileBytes(payload, isBase64)
			if ok && isVolcKimiPDFMagic(decoded) {
				return volcKimiFileKindPDF, "", true
			}
			if mediaType == "application/pdf" {
				// Claimed PDF but no magic — still forward; Volc validates.
				return volcKimiFileKindPDF, "", true
			}
			if ok && looksLikeVolcKimiPlainText(decoded) {
				return volcKimiFileKindText, string(decoded), true
			}
			return volcKimiFileKindOther, "", false
		default:
			decoded, ok := decodeVolcKimiFileBytes(payload, isBase64)
			if ok && isVolcKimiPDFMagic(decoded) {
				return volcKimiFileKindPDF, "", true
			}
			return volcKimiFileKindOther, "", false
		}
	}
	decoded, ok := decodeVolcKimiFileBytes(raw, true)
	if !ok {
		return volcKimiFileKindOther, "", false
	}
	if isVolcKimiPDFMagic(decoded) {
		return volcKimiFileKindPDF, "", true
	}
	if looksLikeVolcKimiPlainText(decoded) {
		return volcKimiFileKindText, string(decoded), true
	}
	return volcKimiFileKindOther, "", false
}

func decodeVolcKimiFilePayload(payload string, isBase64 bool) (string, bool) {
	payload = strings.TrimSpace(payload)
	if !isBase64 {
		return payload, payload != ""
	}
	decoded, ok := decodeVolcKimiFileBytes(payload, true)
	if !ok {
		return "", false
	}
	return string(decoded), true
}

func decodeVolcKimiFileBytes(payload string, isBase64 bool) ([]byte, bool) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil, false
	}
	if !isBase64 {
		return []byte(payload), true
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			return nil, false
		}
	}
	return decoded, true
}

func isVolcKimiPDFMagic(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	return string(b[:4]) == "%PDF"
}

func looksLikeVolcKimiPlainText(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	// Reject obvious binary (NUL) and require mostly printable UTF-8.
	nul := 0
	printable := 0
	for _, c := range b {
		if c == 0 {
			nul++
		}
		if c == '\n' || c == '\r' || c == '\t' || (c >= 32 && c < 127) {
			printable++
		}
	}
	if nul > 0 {
		return false
	}
	return printable*10 >= len(b)*8
}

func volcKimiFileURLLooksLikeVideo(lowerURL string) bool {
	path := lowerURL
	if u, err := url.Parse(lowerURL); err == nil && u.Path != "" {
		path = u.Path
	}
	for _, ext := range []string{".mp4", ".webm", ".mov", ".mkv", ".m4v", ".avi"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

func volcKimiFileURLLooksLikeNonPDF(lowerURL string) bool {
	path := lowerURL
	if u, err := url.Parse(lowerURL); err == nil && u.Path != "" {
		path = u.Path
	}
	for _, ext := range []string{".txt", ".md", ".csv", ".json", ".xml", ".html", ".htm", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".wav", ".mp3"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

const volcKimiVideoUnreachableMessage = "video_url is not reachable; provide a video URL this service can access"

// volcKimiMinDataVideoBytes is the minimum decoded payload size for an inline
// data: video_url. Tiny placeholders (cn-prod 2026-11-19: url_len≈182 AAAA…)
// pass the previous non-empty check then draw Volc's unnamed InvalidParameter;
// real inline videos are larger. Do not strip the part — local 400 only.
const volcKimiMinDataVideoBytes = 256

// probeVolcKimiVideoURL reports whether one video_url can be read. Tests replace
// it; production uses a public-client HEAD so private and link-local targets are
// refused before dial.
var probeVolcKimiVideoURL = defaultProbeVolcKimiVideoURL

// rejectUnreachableVolcKimiVideoURLs turns a video_url the service cannot read
// into a field-level 400. Reachable http(s) URLs and inline data URLs that look
// like a real video/* base64 payload are left for the supplier. Empty, non-video
// MIME, invalid base64, or undersized data: payloads become the same local
// parameter error as an unreachable http(s) URL — never silently stripped.
func rejectUnreachableVolcKimiVideoURLs(ctx context.Context, body []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for _, raw := range volcKimiVideoURLs(body) {
		if err := probeVolcKimiVideoURL(ctx, raw); err != nil {
			return newOpenAIInvalidParameterError("messages.content", volcKimiVideoUnreachableMessage)
		}
	}
	return nil
}

func volcKimiVideoURLs(body []byte) []string {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return nil
	}
	var urls []string
	for _, msg := range msgs.Array() {
		content := msg.Get("content")
		switch {
		case content.IsArray():
			for _, part := range content.Array() {
				if raw, ok := volcKimiVideoURLValue(part); ok {
					urls = append(urls, raw)
				}
			}
		case content.IsObject():
			if raw, ok := volcKimiVideoURLValue(content); ok {
				urls = append(urls, raw)
			}
		}
	}
	return urls
}

func volcKimiVideoURLValue(part gjson.Result) (string, bool) {
	video := part.Get("video_url")
	isVideo := strings.EqualFold(strings.TrimSpace(part.Get("type").String()), "video_url")
	if !isVideo && !video.Exists() {
		return "", false
	}
	switch {
	case video.Type == gjson.String:
		return strings.TrimSpace(video.String()), true
	case video.IsObject():
		return strings.TrimSpace(video.Get("url").String()), true
	default:
		return "", true
	}
}

func defaultProbeVolcKimiVideoURL(ctx context.Context, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("empty video_url")
	}
	if strings.HasPrefix(strings.ToLower(raw), "data:") {
		return validateVolcKimiDataVideoURL(raw)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return fmt.Errorf("invalid video_url")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https", "http":
	default:
		// Supplier-specific schemes such as ms:// are not fetched here.
		return nil
	}
	if urlvalidator.IsBlockedHost(parsed.Hostname()) {
		return fmt.Errorf("video_url host is not public")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return probeVolcKimiHTTPVideo(ctx, httpclient.NewPublicClient(5*time.Second), raw)
}

// validateVolcKimiDataVideoURL accepts only video/* base64 data URIs whose
// decoded payload meets volcKimiMinDataVideoBytes. Placeholder or non-video
// data URIs are refused locally so Volc does not return an unnamed
// InvalidParameter; the content part itself is not stripped.
func validateVolcKimiDataVideoURL(raw string) error {
	meta, payload, ok := strings.Cut(raw, ",")
	if !ok || strings.TrimSpace(payload) == "" {
		return fmt.Errorf("empty data video_url")
	}
	metaLower := strings.ToLower(strings.TrimSpace(meta))
	if !strings.HasPrefix(metaLower, "data:") {
		return fmt.Errorf("invalid data video_url")
	}
	rest := strings.TrimSpace(strings.TrimPrefix(metaLower, "data:"))
	mediaType, paramPart, _ := strings.Cut(rest, ";")
	mediaType = strings.TrimSpace(mediaType)
	if !strings.HasPrefix(mediaType, "video/") {
		return fmt.Errorf("data video_url mime must be video/*")
	}
	isBase64 := false
	for _, param := range strings.Split(paramPart, ";") {
		if strings.TrimSpace(param) == "base64" {
			isBase64 = true
			break
		}
	}
	if !isBase64 {
		return fmt.Errorf("data video_url must be base64")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(payload))
		if err != nil {
			return fmt.Errorf("data video_url is not valid base64")
		}
	}
	if len(decoded) < volcKimiMinDataVideoBytes {
		return fmt.Errorf("data video_url payload too small")
	}
	return nil
}

const volcKimiVideoProbeAttempts = 3

// probeVolcKimiHTTPVideo checks one already-classified public http(s) URL.
// HEAD is enough when the server answers; 401/403/405/501 fall back to a
// one-byte ranged GET because some stores reject HEAD while still serving the
// file. Transport errors and 5xx also fall back to ranged GET, then retry the
// whole probe a few times — cn-prod remote URL fetches often fail in bursts
// rather than as permanent unreachability.
func probeVolcKimiHTTPVideo(ctx context.Context, client *http.Client, raw string) error {
	var lastErr error
	for attempt := 0; attempt < volcKimiVideoProbeAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt) * 200 * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				if lastErr != nil {
					return lastErr
				}
				return ctx.Err()
			case <-timer.C:
			}
		}
		err := probeVolcKimiHTTPVideoOnce(ctx, client, raw)
		if err == nil {
			return nil
		}
		lastErr = err
		if !volcKimiVideoProbeTransient(err) {
			return err
		}
	}
	return lastErr
}

func probeVolcKimiHTTPVideoOnce(ctx context.Context, client *http.Client, raw string) error {
	status, err := volcKimiVideoStatus(ctx, client, http.MethodHead, raw)
	if err != nil {
		// Some CDNs reset HEAD while still serving ranged GET.
		status, err = volcKimiVideoStatus(ctx, client, http.MethodGet, raw)
		if err != nil {
			return err
		}
	}
	reachable, retryGET := interpretVolcKimiVideoStatus(status)
	if reachable {
		return nil
	}
	if !retryGET {
		return &volcKimiVideoProbeStatusError{status: status}
	}
	status, err = volcKimiVideoStatus(ctx, client, http.MethodGet, raw)
	if err != nil {
		return err
	}
	if ok, _ := interpretVolcKimiVideoStatus(status); !ok {
		return &volcKimiVideoProbeStatusError{status: status}
	}
	return nil
}

type volcKimiVideoProbeStatusError struct {
	status int
}

func (e *volcKimiVideoProbeStatusError) Error() string {
	if e == nil {
		return "video_url probe failed"
	}
	return fmt.Sprintf("video_url returned %d", e.status)
}

func volcKimiVideoProbeTransient(err error) bool {
	if err == nil {
		return false
	}
	var statusErr *volcKimiVideoProbeStatusError
	if errors.As(err, &statusErr) {
		return statusErr.status >= 500 || statusErr.status == http.StatusTooManyRequests
	}
	// Transport / timeout / DNS flakes are retryable; permanent parse issues are not.
	return true
}

func interpretVolcKimiVideoStatus(status int) (reachable bool, retryGET bool) {
	if status >= 200 && status < 300 {
		return true, false
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return false, true
	default:
		return false, false
	}
}

func volcKimiVideoStatus(ctx context.Context, client *http.Client, method, raw string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, raw, nil)
	if err != nil {
		return 0, err
	}
	if method == http.MethodGet {
		req.Header.Set("Range", "bytes=0-0")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
	return resp.StatusCode, nil
}

func normalizeVolcKimiOrphanToolMessages(body []byte) ([]byte, error) {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body, nil
	}
	ids := make(map[string]struct{})
	for _, msg := range msgs.Array() {
		if !strings.EqualFold(msg.Get("role").String(), "assistant") {
			continue
		}
		for _, call := range msg.Get("tool_calls").Array() {
			// Count the id even when function.name is missing. That history is
			// already rejected elsewhere; dropping the id here would rewrite a
			// paired tool message into a user message and hide the 400.
			id := strings.TrimSpace(call.Get("id").String())
			if id != "" {
				ids[id] = struct{}{}
			}
		}
	}
	for _, msg := range msgs.Array() {
		if !strings.EqualFold(msg.Get("role").String(), "tool") {
			continue
		}
		if _, ok := ids[strings.TrimSpace(msg.Get("tool_call_id").String())]; ok {
			continue
		}
		return nil, newOpenAIInvalidParameterError("messages", "tool message requires a tool_call_id that matches an assistant tool call")
	}
	return body, nil
}

func normalizeVolcKimiEmptyFunctionNames(body []byte) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body, nil
	}
	for _, tool := range tools.Array() {
		if !volcKimiFunctionTool(tool) {
			continue
		}
		name := tool.Get("function.name")
		// A missing name is a different check. Only a present blank name is rejected here.
		if !name.Exists() || name.Type == gjson.Null {
			continue
		}
		if strings.TrimSpace(name.String()) == "" {
			return nil, newOpenAIInvalidParameterError("tools", "function.name must not be empty")
		}
	}
	return body, nil
}

func volcKimiFunctionTool(tool gjson.Result) bool {
	if !tool.Get("function").Exists() {
		return false
	}
	typ := tool.Get("type")
	if !typ.Exists() || typ.Type == gjson.Null {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(typ.String()), "function")
}

func normalizeVolcKimiThinking(body []byte) ([]byte, error) {
	selected := ""
	paths := []string{"thinking", "enable_thinking", "chat_template_kwargs.thinking", "chat_template_kwargs.enable_thinking"}
	for _, path := range paths {
		value := gjson.GetBytes(body, path)
		if !value.Exists() || value.Type == gjson.Null {
			continue
		}
		mode := ""
		switch value.Type {
		case gjson.True:
			mode = "enabled"
		case gjson.False:
			mode = "disabled"
		default:
			if value.IsObject() && (path == "thinking" || path == "chat_template_kwargs.thinking") {
				rawType := value.Get("type")
				// A missing or blank type is unspecified, not a malformed switch.
				// Volc Agent Plan accepts thinking.type="" and still returns
				// reasoning tokens (cn-prod direct replay, 2026-10-28, accounts
				// 77 and 11). Rejecting it locally was a false 400. Effort
				// normalization below rewrites a blank type to enabled when
				// reasoning_effort is present.
				if !rawType.Exists() || strings.TrimSpace(rawType.String()) == "" {
					continue
				}
				mode = strings.TrimSpace(rawType.String())
			}
		}
		if mode != "enabled" && mode != "disabled" && mode != "auto" {
			return nil, newOpenAIInvalidParameterError(path, "thinking must be a boolean or an object with type enabled, disabled or auto")
		}
		if selected != "" && selected != mode {
			return nil, newOpenAIInvalidParameterError("thinking", "conflicting thinking switches")
		}
		selected = mode
	}
	shaped := body
	var err error
	if selected != "" {
		// Preserve provider options in a canonical object; scalar aliases are rebuilt.
		if !gjson.GetBytes(shaped, "thinking").IsObject() {
			shaped, err = sjson.DeleteBytes(shaped, "thinking")
			if err != nil {
				return nil, err
			}
		}
		shaped, err = sjson.SetBytes(shaped, "thinking.type", selected)
		if err != nil {
			return nil, err
		}
		for _, path := range paths[1:] {
			shaped, err = sjson.DeleteBytes(shaped, path)
			if err != nil {
				return nil, err
			}
		}
		if kwargs := gjson.GetBytes(shaped, "chat_template_kwargs"); kwargs.IsObject() && len(kwargs.Map()) == 0 {
			shaped, err = sjson.DeleteBytes(shaped, "chat_template_kwargs")
			if err != nil {
				return nil, err
			}
		}
	}

	// Moonshot clients may attach Anthropic-style budget_tokens; drop before relay.
	if gjson.GetBytes(shaped, "thinking.budget_tokens").Exists() {
		shaped, err = sjson.DeleteBytes(shaped, "thinking.budget_tokens")
		if err != nil {
			return nil, err
		}
	}

	shaped = promoteMoonshotNestedReasoningEffort(shaped)
	shaped, err = preferKimiThinkingEffort(shaped, selected)
	if err != nil {
		return nil, err
	}
	shaped, err = normalizeVolcKimiReasoningEffort(shaped, selected)
	return shaped, err
}

// normalizeVolcKimiReasoningEffort accepts Moonshot kimi-k3 reasoning_effort
// vocabulary (and common aliases) on the public surface. Effort alone must
// produce observable thinking: when the client did not explicitly disable
// thinking, inject thinking.type=enabled so intensity probes are not silent.
func normalizeVolcKimiReasoningEffort(body []byte, thinkingMode string) ([]byte, error) {
	if !gjson.GetBytes(body, "reasoning_effort").Exists() {
		return body, nil
	}
	effort := strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String())
	if volcKimiEffortDisablesThinking(effort) {
		return applyVolcKimiEffortOff(body, thinkingMode)
	}
	mapped := mapMoonshotK3ReasoningEffort(effort)
	if mapped == "" {
		return body, newOpenAIInvalidParameterError(
			"reasoning_effort",
			"kimi-k3 reasoning_effort must be one of: low, high, max",
		)
	}
	shaped := body
	var err error
	if mapped != effort {
		shaped, err = sjson.SetBytes(shaped, "reasoning_effort", mapped)
		if err != nil {
			return body, err
		}
	}
	mode := strings.ToLower(strings.TrimSpace(thinkingMode))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(gjson.GetBytes(shaped, "thinking.type").String()))
	}
	if mode == "disabled" {
		// Closing thinking must win over Moonshot effort. Leaving effort on the
		// wire is rejected upstream ("Invalid reasoning_effort when thinking.type
		// is disabled"); strip it so the public off-switch stays a clean 200.
		if gjson.GetBytes(shaped, "reasoning_effort").Exists() {
			shaped, err = sjson.DeleteBytes(shaped, "reasoning_effort")
			if err != nil {
				return body, err
			}
		}
		return shaped, nil
	}
	if mode == "enabled" || mode == "auto" {
		return shaped, nil
	}
	if !gjson.GetBytes(shaped, "thinking").IsObject() {
		shaped, err = sjson.DeleteBytes(shaped, "thinking")
		if err != nil {
			return body, err
		}
	}
	return sjson.SetBytes(shaped, "thinking.type", "enabled")
}

func normalizeVolcKimiAllowedTools(body []byte) ([]byte, error) {
	if gjson.GetBytes(body, "tool_choice.type").String() != "allowed_tools" {
		return body, nil
	}
	invalid := func(message string) ([]byte, error) {
		return nil, newOpenAIInvalidParameterError("tool_choice", message)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return invalid("invalid tool selection request")
	}
	if _, ok := root["functions"]; ok {
		return invalid("allowed_tools cannot be combined with legacy functions")
	}
	if _, ok := root["function_call"]; ok {
		return invalid("allowed_tools cannot be combined with legacy function_call")
	}
	var choice struct {
		Mode  string            `json:"mode"`
		Tools []json.RawMessage `json:"tools"`
	}
	selection := root["tool_choice"]
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(selection, &envelope); err != nil {
		return invalid("invalid allowed_tools selection")
	}
	if nested, ok := envelope["allowed_tools"]; ok {
		// Chat Completions nests this policy; accept the existing flattened
		// compatibility form only when the nested policy is absent.
		_, hasMode := envelope["mode"]
		_, hasTools := envelope["tools"]
		if hasMode || hasTools {
			return invalid("cannot combine nested and flattened allowed_tools fields")
		}
		selection = nested
	}
	if err := json.Unmarshal(selection, &choice); err != nil || (choice.Mode != "auto" && choice.Mode != "required") || len(choice.Tools) == 0 {
		return invalid("allowed_tools requires mode auto or required and a non-empty function list")
	}
	allowed := make(map[string]bool, len(choice.Tools))
	for _, raw := range choice.Tools {
		ref := gjson.ParseBytes(raw)
		name := ref.Get("function.name").String()
		flat := ref.Get("name").String()
		if name == "" {
			name = flat
		} else if flat != "" && flat != name {
			return invalid("conflicting allowed function names")
		}
		if ref.Get("type").String() != "function" || name == "" || allowed[name] {
			return invalid("allowed_tools requires unique named functions")
		}
		allowed[name] = true
	}
	var definitions []json.RawMessage
	if err := json.Unmarshal(root["tools"], &definitions); err != nil {
		return invalid("tools must be an array")
	}
	selected := make([]json.RawMessage, 0, len(allowed))
	seen := make(map[string]bool, len(allowed))
	for _, raw := range definitions {
		def := gjson.ParseBytes(raw)
		name := def.Get("function.name").String()
		if def.Get("type").String() != "function" || !allowed[name] {
			continue
		}
		if seen[name] {
			return invalid("allowed function must have exactly one definition")
		}
		seen[name] = true
		selected = append(selected, raw)
	}
	if len(seen) != len(allowed) {
		return invalid("allowed function must have exactly one definition")
	}
	var err error
	root["tools"], err = json.Marshal(selected)
	if err != nil {
		return nil, fmt.Errorf("encode allowed tools: %w", err)
	}
	root["tool_choice"], err = json.Marshal(choice.Mode)
	if err != nil {
		return nil, err
	}
	return json.Marshal(root)
}
