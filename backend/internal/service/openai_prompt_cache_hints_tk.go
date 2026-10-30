package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Only protocol-owned locations are hints. Tool schemas and argument strings
// are user data even when they contain these same property names.
func stripOpenAIExplicitPromptCacheHints(body []byte) ([]byte, bool, error) {
	paths := []string{"prompt_cache_breakpoint"}
	if gjson.GetBytes(body, "prompt_cache_options.mode").String() == "explicit" {
		paths = append(paths, "prompt_cache_options")
	}
	for _, root := range []string{"input", "messages"} {
		items := gjson.GetBytes(body, root)
		if !items.IsArray() {
			continue
		}
		for i, item := range items.Array() {
			if !item.IsObject() {
				continue
			}
			prefix := fmt.Sprintf("%s.%d", root, i)
			paths = append(paths, prefix+".prompt_cache_breakpoint")
			content := item.Get("content")
			if !content.IsArray() {
				continue
			}
			for j, part := range content.Array() {
				if part.IsObject() {
					paths = append(paths, fmt.Sprintf("%s.content.%d.prompt_cache_breakpoint", prefix, j))
				}
			}
		}
	}
	changed := false
	for _, path := range paths {
		if !gjson.GetBytes(body, path).Exists() {
			continue
		}
		next, err := sjson.DeleteBytes(body, path)
		if err != nil {
			return nil, false, err
		}
		body, changed = next, true
	}
	return body, changed, nil
}

var (
	openAICacheHintPathPattern       = regexp.MustCompile(`^(?:prompt_cache_options|prompt_cache_breakpoint|(?:input|messages)\[\d+\](?:\.content\[\d+\])?\.prompt_cache_breakpoint)(?:\.mode)?$`)
	openAICacheHintErrorPattern      = regexp.MustCompile(`(?i)(?:unknown[ _-]+parameter|unsupported[ _-]+parameter|missing required parameter)\s*:\s*["']?((?:prompt_cache_options|prompt_cache_breakpoint|(?:input|messages)\[\d+\](?:\.content\[\d+\])?\.prompt_cache_breakpoint)(?:\.mode)?)(?:["']|\s|$)`)
	openAICacheHintModelErrorPattern = regexp.MustCompile(`(?i)(?:^|[\s"'])((?:prompt_cache_options|prompt_cache_breakpoint|(?:input|messages)\[\d+\](?:\.content\[\d+\])?\.prompt_cache_breakpoint)(?:\.mode)?)["']?\s+is not supported on this model\b`)
)

// extend the shared rejected-field owner rather than adding a Chat-only retry
// policy. Older indexed-breakpoint handling stays narrow unless explicit mode
// couples it to all breakpoints or the new nested/options shape is involved.
func normalizeOpenAIExplicitCacheHintRejection(body []byte, code, message, param string) ([]byte, bool, error) {
	messageParam := ""
	for _, pattern := range []*regexp.Regexp{openAICacheHintErrorPattern, openAICacheHintModelErrorPattern} {
		if match := pattern.FindStringSubmatch(message); len(match) == 2 {
			messageParam = match[1]
			break
		}
	}
	if param != "" && messageParam != "" && param != messageParam {
		return nil, false, nil
	}
	if param == "" {
		param = messageParam
	}
	if !openAICacheHintPathPattern.MatchString(param) {
		return nil, false, nil
	}
	if messageParam == "" && code != "invalid_parameter" && code != "unsupported_parameter" && code != "unknown_parameter" {
		return nil, false, nil
	}
	explicit := gjson.GetBytes(body, "prompt_cache_options.mode").String() == "explicit"
	if !explicit && !strings.HasPrefix(param, "prompt_cache_options") && !strings.Contains(param, ".content[") && !strings.HasSuffix(param, ".mode") && (param != "prompt_cache_breakpoint" || gjson.GetBytes(body, param).Exists()) {
		return nil, false, nil
	}
	path := strings.TrimSuffix(param, ".mode")
	path = strings.ReplaceAll(strings.ReplaceAll(path, "[", "."), "]", "")
	// A bare field name may be the upstream's global rejection of nested hints.
	if !gjson.GetBytes(body, path).Exists() && param != "prompt_cache_breakpoint" {
		return nil, false, nil
	}
	next, changed, err := stripOpenAIExplicitPromptCacheHints(body)
	if err != nil {
		return nil, false, err
	}
	// A direct rejection of implicit options requires removing that field too;
	// proactive cleanup otherwise preserves implicit options.
	if strings.HasPrefix(param, "prompt_cache_options") && gjson.GetBytes(next, "prompt_cache_options").Exists() {
		next, err = sjson.DeleteBytes(next, "prompt_cache_options")
		changed = true
	}
	return next, changed, err
}
