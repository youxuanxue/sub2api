package service

import (
	"math"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// validateVolcKimiRequestLimits rejects kimi-k3 request fields that Volc Agent
// Plan accepts with HTTP 200 but silently ignores or reinterprets (cn-prod
// benchmark, 2026-11-04). The public kimi-k3 surface follows Moonshot, which
// rejects these values; see docs/approved/volc-kimi-chat-compatibility.md.
// Valid values are forwarded unchanged.
func validateVolcKimiRequestLimits(body []byte) error {
	if err := volcKimiNumberInRange(body, "temperature", 0, 2); err != nil {
		return err
	}
	if err := volcKimiNumberInRange(body, "top_p", 0, 1); err != nil {
		return err
	}
	if n := gjson.GetBytes(body, "n"); volcKimiFieldSet(n) {
		if !volcKimiIsInteger(n) || n.Num != 1 {
			return newOpenAIInvalidParameterError("n", "kimi-k3 supports only n=1")
		}
	}
	if logprobs := gjson.GetBytes(body, "logprobs"); volcKimiFieldSet(logprobs) {
		if logprobs.Type != gjson.True && logprobs.Type != gjson.False {
			return newOpenAIInvalidParameterError("logprobs", "logprobs must be a boolean")
		}
		if logprobs.Bool() {
			return newOpenAIInvalidParameterError("logprobs", "logprobs is not supported for kimi-k3 on this service")
		}
	}
	if top := gjson.GetBytes(body, "top_logprobs"); volcKimiFieldSet(top) {
		if !volcKimiIsInteger(top) || top.Num < 0 {
			return newOpenAIInvalidParameterError("top_logprobs", "top_logprobs must be a non-negative integer")
		}
		if top.Num > 0 {
			return newOpenAIInvalidParameterError("top_logprobs", "top_logprobs requires logprobs, which is not supported for kimi-k3 on this service")
		}
	}
	for _, path := range []string{"max_tokens", "max_completion_tokens"} {
		if value := gjson.GetBytes(body, path); volcKimiFieldSet(value) {
			if !volcKimiIsInteger(value) || value.Num < 1 {
				return newOpenAIInvalidParameterError(path, path+" must be a positive integer")
			}
		}
	}
	return validateVolcKimiPrediction(gjson.GetBytes(body, "prediction"))
}

// validateVolcKimiPrediction enforces Moonshot's prediction shape: an object
// with type "content" and string or text-part content. Volc returned 200 for
// every shape and ignores the field (cn-prod direct control, 2026-11-05), so
// only the shape is checked; valid predictions are forwarded unchanged.
func validateVolcKimiPrediction(prediction gjson.Result) error {
	if !volcKimiFieldSet(prediction) {
		return nil
	}
	if !prediction.IsObject() {
		return newOpenAIInvalidParameterError("prediction", "prediction must be an object")
	}
	if prediction.Get("type").Type != gjson.String || prediction.Get("type").String() != "content" {
		return newOpenAIInvalidParameterError("prediction.type", "prediction.type must be content")
	}
	content := prediction.Get("content")
	switch {
	case content.Type == gjson.String:
		return nil
	case content.IsArray():
		for _, part := range content.Array() {
			if part.Get("type").String() != "text" || part.Get("text").Type != gjson.String {
				return newOpenAIInvalidParameterError("prediction.content", "prediction.content parts must be text")
			}
		}
		return nil
	default:
		return newOpenAIInvalidParameterError("prediction.content", "prediction.content must be a string or an array of text parts")
	}
}

func volcKimiFieldSet(value gjson.Result) bool {
	return value.Exists() && value.Type != gjson.Null
}

func volcKimiIsInteger(value gjson.Result) bool {
	return value.Type == gjson.Number && value.Num == math.Trunc(value.Num)
}

func volcKimiNumberInRange(body []byte, path string, lo, hi float64) error {
	value := gjson.GetBytes(body, path)
	if !volcKimiFieldSet(value) {
		return nil
	}
	if value.Type != gjson.Number || value.Num < lo || value.Num > hi {
		return newOpenAIInvalidParameterError(path, path+" must be a number between "+strconv.FormatFloat(lo, 'f', -1, 64)+" and "+strconv.FormatFloat(hi, 'f', -1, 64))
	}
	return nil
}

// volcKimiEffortDisablesThinking reports a reasoning_effort token that asks for
// no reasoning. Moonshot kimi-k3 has no off-switch, but this supplier honors
// thinking.type=disabled, so "none" is mapped to that instead of low effort.
func volcKimiEffortDisablesThinking(effort string) bool {
	switch normalizeEffortToken(effort) {
	case "none", "off", "disabled":
		return true
	default:
		return false
	}
}

// applyVolcKimiEffortOff turns reasoning_effort=none into thinking.type=disabled.
// An explicit enabled switch with none is contradictory and returns a 400.
func applyVolcKimiEffortOff(body []byte, thinkingMode string) ([]byte, error) {
	mode := strings.ToLower(strings.TrimSpace(thinkingMode))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String()))
	}
	if mode == "enabled" {
		return body, newOpenAIInvalidParameterError("reasoning_effort", "reasoning_effort none conflicts with enabled thinking")
	}
	shaped, err := sjson.DeleteBytes(body, "reasoning_effort")
	if err != nil {
		return body, err
	}
	if !gjson.GetBytes(shaped, "thinking").IsObject() {
		shaped, err = sjson.DeleteBytes(shaped, "thinking")
		if err != nil {
			return body, err
		}
	}
	return sjson.SetBytes(shaped, "thinking.type", "disabled")
}
