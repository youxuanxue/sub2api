package service

import (
	"context"
	"encoding/json"
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
	shaped, err := normalizeVolcKimiInboundMessages(body)
	if err != nil {
		return nil, err
	}
	shaped, err = normalizeVolcKimiThinking(shaped)
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
		return nil, newOpenAIInvalidParameterError("tool_choice", err.Error())
	}
	return sjson.SetBytes(shaped, "tool_choice", "required")
}

// normalizeVolcKimiInboundMessages rewrites only the message and tool shapes
// that Volc Agent Plan kimi-k3 rejected on cn-prod (2026-10-23) and that a
// direct replay then accepted:
//   - a JSON object in messages.content is either one content part (wrap it in
//     an array) or an ordinary object (send its JSON text as a string);
//   - a tool message whose tool_call_id is absent from assistant tool_calls
//     becomes a user message, keeping its content;
//   - function tools with an empty name are removed. If none remain, tool_choice
//     is removed with them.
//
// Matched tool results, string content, and content-part arrays are unchanged.
// Video parts are not stripped. Reachability is a separate preflight:
// rejectUnreachableVolcKimiVideoURLs.
func normalizeVolcKimiInboundMessages(body []byte) ([]byte, error) {
	shaped, err := normalizeVolcKimiMessageContents(body)
	if err != nil {
		return nil, err
	}
	shaped, err = normalizeVolcKimiOrphanToolMessages(shaped)
	if err != nil {
		return nil, err
	}
	return normalizeVolcKimiEmptyFunctionNames(shaped)
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
	return shaped, nil
}

func volcKimiContentPartType(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "text", "image_url", "video_url", "input_audio", "file":
		return true
	default:
		return false
	}
}

const volcKimiVideoUnreachableMessage = "video_url is not reachable; provide a video URL this service can access"

// probeVolcKimiVideoURL reports whether one video_url can be read. Tests replace
// it; production uses a public-client HEAD so private and link-local targets are
// refused before dial.
var probeVolcKimiVideoURL = defaultProbeVolcKimiVideoURL

// rejectUnreachableVolcKimiVideoURLs turns a video_url the service cannot read
// into a field-level 400. Reachable http(s) URLs and inline data URLs are left
// for the supplier. This avoids Volc's unnamed InvalidParameter when the URL
// itself cannot be fetched.
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
		_, payload, ok := strings.Cut(raw, ",")
		if !ok || strings.TrimSpace(payload) == "" {
			return fmt.Errorf("empty data video_url")
		}
		return nil
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

// probeVolcKimiHTTPVideo checks one already-classified public http(s) URL.
// HEAD is enough when the server answers; 401/403/405/501 fall back to a
// one-byte ranged GET because some stores reject HEAD while still serving the file.
func probeVolcKimiHTTPVideo(ctx context.Context, client *http.Client, raw string) error {
	status, err := volcKimiVideoStatus(ctx, client, http.MethodHead, raw)
	if err != nil {
		return err
	}
	reachable, retryGET := interpretVolcKimiVideoStatus(status)
	if reachable {
		return nil
	}
	if !retryGET {
		return fmt.Errorf("video_url returned %d", status)
	}
	status, err = volcKimiVideoStatus(ctx, client, http.MethodGet, raw)
	if err != nil {
		return err
	}
	if ok, _ := interpretVolcKimiVideoStatus(status); !ok {
		return fmt.Errorf("video_url returned %d", status)
	}
	return nil
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
			id := strings.TrimSpace(call.Get("id").String())
			if id != "" {
				ids[id] = struct{}{}
			}
		}
	}
	shaped := body
	var err error
	for i, msg := range msgs.Array() {
		if !strings.EqualFold(msg.Get("role").String(), "tool") {
			continue
		}
		if _, ok := ids[strings.TrimSpace(msg.Get("tool_call_id").String())]; ok {
			continue
		}
		shaped, err = sjson.SetBytes(shaped, fmt.Sprintf("messages.%d.role", i), "user")
		if err != nil {
			return nil, err
		}
		shaped, err = sjson.DeleteBytes(shaped, fmt.Sprintf("messages.%d.tool_call_id", i))
		if err != nil {
			return nil, err
		}
	}
	return shaped, nil
}

func normalizeVolcKimiEmptyFunctionNames(body []byte) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body, nil
	}
	kept := make([]json.RawMessage, 0, len(tools.Array()))
	dropped := false
	for _, tool := range tools.Array() {
		name := strings.TrimSpace(tool.Get("function.name").String())
		if tool.Get("function").Exists() && name == "" {
			dropped = true
			continue
		}
		kept = append(kept, json.RawMessage(tool.Raw))
	}
	if !dropped {
		return body, nil
	}
	if len(kept) == 0 {
		shaped, err := sjson.DeleteBytes(body, "tools")
		if err != nil {
			return nil, err
		}
		if gjson.GetBytes(shaped, "tool_choice").Exists() {
			shaped, err = sjson.DeleteBytes(shaped, "tool_choice")
			if err != nil {
				return nil, err
			}
		}
		return shaped, nil
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "tools", encoded)
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
