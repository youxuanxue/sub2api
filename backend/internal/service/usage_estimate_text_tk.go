package service

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
)

// Protocol adapters for the zero-usage estimate owner
// (usage_estimate_fallback_tk.go). They only extract text; the owner alone
// decides whether to estimate, counts tokens and records evidence.
// Contract: docs/approved/usage-estimate-fallback-ssot.md.
//
// Request side: one extractor reads every billed text protocol shape
// (OpenAI chat, OpenAI responses, Anthropic messages, Gemini generateContent)
// because the fields do not collide. Images, audio, video and file parts are
// skipped here, like the Kiro and Gemini Web native estimators. K3 data-URL
// image tokens are added later by the owner (KimiK3ImageInputTokens).
//
// Output side: the extractor follows what the client actually received, so it
// dispatches on the payload shape of one JSON body or one SSE data frame.

// usageEstimateRequestText joins the prompt text the upstream sees.
func usageEstimateRequestText(body []byte) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	parts := make([]string, 0, 16)
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}
	root := gjson.ParseBytes(body)

	// Anthropic system (string or text blocks) and Responses instructions.
	usageEstimateContentText(root.Get("system"), add)
	add(root.Get("instructions").String())

	// OpenAI chat / Anthropic messages. Chat order (content, reasoning_content,
	// tool_calls) is kept identical to the former Volc estimator.
	root.Get("messages").ForEach(func(_, msg gjson.Result) bool {
		usageEstimateContentText(msg.Get("content"), add)
		if rc := msg.Get("reasoning_content"); rc.Type == gjson.String {
			add(rc.String())
		}
		msg.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
			add(call.Get("function.name").String())
			add(call.Get("function.arguments").String())
			return true
		})
		return true
	})

	// OpenAI responses input: string or items.
	input := root.Get("input")
	switch {
	case input.Type == gjson.String:
		add(input.String())
	case input.IsArray():
		input.ForEach(func(_, item gjson.Result) bool {
			usageEstimateContentText(item.Get("content"), add)
			add(item.Get("name").String())
			add(item.Get("arguments").String())
			if out := item.Get("output"); out.Type == gjson.String {
				add(out.String())
			} else if out.Exists() {
				usageEstimateContentText(out, add)
			}
			item.Get("summary").ForEach(func(_, s gjson.Result) bool {
				add(s.Get("text").String())
				return true
			})
			return true
		})
	}

	// Gemini generateContent.
	root.Get("systemInstruction.parts").ForEach(func(_, part gjson.Result) bool {
		usageEstimateGeminiPartText(part, add)
		return true
	})
	root.Get("contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			usageEstimateGeminiPartText(part, add)
			return true
		})
		return true
	})

	// Tool declarations of every protocol are counted as their raw JSON.
	if tools := root.Get("tools"); tools.IsArray() && len(tools.Array()) > 0 {
		add(tools.Raw)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// usageEstimateContentText reads a content value that is either a string or
// an array of typed parts (chat parts, Anthropic blocks, Responses parts).
func usageEstimateContentText(content gjson.Result, add func(string)) {
	switch {
	case content.Type == gjson.String:
		add(content.String())
	case content.IsArray():
		content.ForEach(func(_, part gjson.Result) bool {
			add(part.Get("text").String())
			add(part.Get("thinking").String())
			if part.Get("type").String() == "tool_use" {
				add(part.Get("name").String())
				if in := part.Get("input"); in.Exists() {
					add(in.Raw)
				}
			}
			if part.Get("type").String() == "tool_result" {
				usageEstimateContentText(part.Get("content"), add)
			}
			return true
		})
	}
}

func usageEstimateGeminiPartText(part gjson.Result, add func(string)) {
	add(strings.TrimSpace(part.Get("text").String()))
	for _, key := range []string{"functionCall", "functionResponse"} {
		if node := part.Get(key); node.Exists() {
			add(strings.TrimSpace(node.Get("name").String()))
			if key == "functionCall" {
				if args := node.Get("args"); args.Exists() {
					add(args.Raw)
				}
			} else if resp := node.Get("response"); resp.Exists() {
				add(resp.Raw)
			}
		}
	}
}

// usageEstimateDeliveredOutputText extracts the output text from the bytes the
// client received: a JSON body (object or Gemini JSON array) or an SSE stream.
func usageEstimateDeliveredOutputText(delivered []byte) string {
	trimmed := bytes.TrimSpace(delivered)
	if len(trimmed) == 0 {
		return ""
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		if gjson.ValidBytes(trimmed) {
			root := gjson.ParseBytes(trimmed)
			if root.IsArray() {
				var b strings.Builder
				root.ForEach(func(_, item gjson.Result) bool {
					_, _ = b.WriteString(usageEstimatePayloadOutputText(item, false))
					return true
				})
				return b.String()
			}
			return usageEstimatePayloadOutputText(root, false)
		}
	}
	var deltas, completed strings.Builder
	for _, line := range strings.Split(string(trimmed), "\n") {
		payload, ok := extractOpenAISSEDataLine(strings.TrimRight(line, "\r"))
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" || payload == "[DONE]" || !gjson.Valid(payload) {
			continue
		}
		root := gjson.Parse(payload)
		if root.Get("type").String() == "response.completed" {
			_, _ = completed.WriteString(usageEstimateResponsesOutputText(root.Get("response")))
			continue
		}
		_, _ = deltas.WriteString(usageEstimatePayloadOutputText(root, true))
	}
	if strings.TrimSpace(deltas.String()) != "" {
		return deltas.String()
	}
	return completed.String()
}

// usageEstimatePayloadOutputText dispatches one payload to its protocol
// adapter. stream=true means the payload is one SSE frame.
func usageEstimatePayloadOutputText(root gjson.Result, stream bool) string {
	switch {
	case root.Get("choices").Exists():
		return usageEstimateChatOutputText(root)
	case root.Get("candidates").Exists():
		return usageEstimateGeminiOutputText(root)
	case stream && strings.HasPrefix(root.Get("type").String(), "response."):
		return usageEstimateResponsesStreamText(root)
	case stream:
		return usageEstimateMessagesStreamText(root)
	case root.Get("object").String() == "response" || root.Get("output").IsArray():
		return usageEstimateResponsesOutputText(root)
	case root.Get("type").String() == "message":
		var b strings.Builder
		usageEstimateContentText(root.Get("content"), func(s string) { _, _ = b.WriteString(s) })
		return b.String()
	}
	return ""
}

// usageEstimateChatOutputText reads a chat.completion body or one
// chat.completion.chunk (same fields as the former Volc estimator).
func usageEstimateChatOutputText(payload gjson.Result) string {
	parts := make([]string, 0, 4)
	payload.Get("choices").ForEach(func(_, choice gjson.Result) bool {
		for _, node := range []gjson.Result{choice.Get("message"), choice.Get("delta")} {
			if !node.Exists() {
				continue
			}
			parts = append(parts, node.Get("reasoning_content").String(), node.Get("content").String())
			node.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
				parts = append(parts, call.Get("function.name").String(), call.Get("function.arguments").String())
				return true
			})
		}
		return true
	})
	return strings.Join(parts, "")
}

func usageEstimateGeminiOutputText(payload gjson.Result) string {
	var b strings.Builder
	payload.Get("candidates").ForEach(func(_, cand gjson.Result) bool {
		cand.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
			if part.Get("thought").Bool() || part.Get("text").Exists() {
				_, _ = b.WriteString(part.Get("text").String())
			}
			if fc := part.Get("functionCall"); fc.Exists() {
				_, _ = b.WriteString(fc.Get("name").String())
				if args := fc.Get("args"); args.Exists() {
					_, _ = b.WriteString(args.Raw)
				}
			}
			return true
		})
		return true
	})
	return b.String()
}

// usageEstimateResponsesOutputText reads a Responses object's output items.
func usageEstimateResponsesOutputText(resp gjson.Result) string {
	var b strings.Builder
	resp.Get("output").ForEach(func(_, item gjson.Result) bool {
		item.Get("content").ForEach(func(_, part gjson.Result) bool {
			_, _ = b.WriteString(part.Get("text").String())
			return true
		})
		item.Get("summary").ForEach(func(_, s gjson.Result) bool {
			_, _ = b.WriteString(s.Get("text").String())
			return true
		})
		if item.Get("type").String() == "function_call" {
			_, _ = b.WriteString(item.Get("name").String())
			_, _ = b.WriteString(item.Get("arguments").String())
		}
		return true
	})
	return b.String()
}

// usageEstimateResponsesStreamText counts delta events only; *.done and
// response.completed repeat the same text.
func usageEstimateResponsesStreamText(root gjson.Result) string {
	switch root.Get("type").String() {
	case "response.output_text.delta", "response.reasoning_summary_text.delta",
		"response.reasoning_text.delta", "response.function_call_arguments.delta",
		"response.refusal.delta":
		return root.Get("delta").String()
	case "response.output_item.added":
		if item := root.Get("item"); item.Get("type").String() == "function_call" {
			return item.Get("name").String()
		}
	}
	return ""
}

// usageEstimateMessagesStreamText reads Anthropic Messages SSE deltas.
func usageEstimateMessagesStreamText(root gjson.Result) string {
	switch root.Get("type").String() {
	case "content_block_delta":
		delta := root.Get("delta")
		return delta.Get("text").String() + delta.Get("thinking").String() + delta.Get("partial_json").String()
	case "content_block_start":
		block := root.Get("content_block")
		return block.Get("text").String() + block.Get("thinking").String() + block.Get("name").String()
	}
	return ""
}
