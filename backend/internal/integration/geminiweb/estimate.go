package geminiweb

import (
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tokenestimate"
	"github.com/tidwall/gjson"
)

// EstimatedBillingTier stamps usage logs when Gemini Web Worker returns no
// usageMetadata and TokenKey settles from the shared cl100k tokenizer
// (same stack as Cursor handoff / Kiro estimate).
const EstimatedBillingTier = "gemini-web-estimated"

// Usage is the prompt/completion split fed into CalculateCostUnified.
type Usage struct {
	Input  int
	Output int
}

// EstimateUsage counts request + response text with tokenestimate.Count.
// responseBody (GenerateContentResponse JSON) and outputText are alternatives;
// outputText wins when non-empty (streaming paths that already accumulated text).
// Does not invent cache hits — Gemini Web is single-turn.
func EstimateUsage(requestBody []byte, responseBody []byte, outputText string) Usage {
	out := Usage{Input: EstimateInputTokens(requestBody)}
	if t := strings.TrimSpace(outputText); t != "" {
		out.Output = tokenestimate.Count(t)
		return out
	}
	out.Output = EstimateOutputTokens(responseBody)
	return out
}

// EstimateInputTokens counts systemInstruction, contents text parts, and tool
// declarations — mirroring what the Worker prompt actually sees. Binary/inline
// image parts are skipped (same as Kiro/Cursor).
func EstimateInputTokens(requestBody []byte) int {
	if len(requestBody) == 0 {
		return 0
	}
	parts := make([]string, 0, 8)

	gjson.GetBytes(requestBody, "systemInstruction.parts").ForEach(func(_, part gjson.Result) bool {
		if t := strings.TrimSpace(part.Get("text").String()); t != "" {
			parts = append(parts, t)
		}
		return true
	})

	gjson.GetBytes(requestBody, "contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if t := strings.TrimSpace(part.Get("text").String()); t != "" {
				parts = append(parts, t)
			}
			if fc := part.Get("functionCall"); fc.Exists() {
				if name := strings.TrimSpace(fc.Get("name").String()); name != "" {
					parts = append(parts, name)
				}
				if args := fc.Get("args"); args.Exists() {
					parts = append(parts, args.Raw)
				}
			}
			if fr := part.Get("functionResponse"); fr.Exists() {
				if name := strings.TrimSpace(fr.Get("name").String()); name != "" {
					parts = append(parts, name)
				}
				if resp := fr.Get("response"); resp.Exists() {
					parts = append(parts, resp.Raw)
				}
			}
			return true
		})
		return true
	})

	gjson.GetBytes(requestBody, "tools").ForEach(func(_, tool gjson.Result) bool {
		decls := tool.Get("functionDeclarations")
		if !decls.Exists() {
			// Some payloads nest under tools[].functionDeclarations already handled;
			// also accept a bare declaration object.
			if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
				parts = append(parts, name)
			}
			if desc := strings.TrimSpace(tool.Get("description").String()); desc != "" {
				parts = append(parts, desc)
			}
			if schema := tool.Get("parameters"); schema.Exists() {
				parts = append(parts, schema.Raw)
			}
			return true
		}
		decls.ForEach(func(_, decl gjson.Result) bool {
			if name := strings.TrimSpace(decl.Get("name").String()); name != "" {
				parts = append(parts, name)
			}
			if desc := strings.TrimSpace(decl.Get("description").String()); desc != "" {
				parts = append(parts, desc)
			}
			if schema := decl.Get("parameters"); schema.Exists() {
				parts = append(parts, schema.Raw)
			}
			return true
		})
		return true
	})

	return tokenestimate.Count(strings.Join(parts, "\n"))
}

// EstimateOutputTokens counts candidate text + functionCall payloads from a
// GenerateContentResponse (or the final aggregated SSE object).
func EstimateOutputTokens(responseBody []byte) int {
	if len(responseBody) == 0 {
		return 0
	}
	parts := make([]string, 0, 4)
	gjson.GetBytes(responseBody, "candidates").ForEach(func(_, cand gjson.Result) bool {
		cand.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
			if t := strings.TrimSpace(part.Get("text").String()); t != "" {
				parts = append(parts, t)
			}
			if fc := part.Get("functionCall"); fc.Exists() {
				if name := strings.TrimSpace(fc.Get("name").String()); name != "" {
					parts = append(parts, name)
				}
				if args := fc.Get("args"); args.Exists() {
					parts = append(parts, args.Raw)
				}
			}
			return true
		})
		return true
	})
	return tokenestimate.Count(strings.Join(parts, "\n"))
}

// CompactJSON is exported for tests that need stable serialization of tool args.
func CompactJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	s := string(b)
	if s == "null" || s == "{}" || s == "[]" {
		return ""
	}
	return s
}
