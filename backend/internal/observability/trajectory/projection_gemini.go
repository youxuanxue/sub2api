package trajectory

import (
	"encoding/base64"
	"strings"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/tidwall/gjson"
)

// Gemini traj v2 projection. Covers the Gemini contents[]/parts[] wire shape on
// /v1beta ...generateContent — platforms gemini and vertex (direct), newapi
// channel_type=41 (Vertex via the bridge), AND antigravity /v1beta (the
// client-facing v1beta surface; the internal v1internal envelope to cloudcode-pa
// is upstream-only and never captured client-side). antigravity /v1 records are
// Anthropic-shaped and dispatch to buildAnthropicMessagesSession instead.
//
// Normalizes into the shared TrajSessionV2 vocabulary: parts with thought=true →
// thinking (carrying thoughtSignature), functionCall → tool_use, text → text;
// user-role functionResponse parts → tool turns; model-role contents are prior
// assistant output and skipped.

func buildGeminiSession(recs []SourceRecord, summary *ExportSummary) (TrajSessionV2, bool) {
	sid := sessionIDForGroup(recs)
	turns := make([]TrajTurnV2, 0, len(recs)*3)
	prevCount := 0
	var lastReq gjson.Result
	assistantModel := ""

	for _, rec := range recs {
		reqBody := marshalToGJSON(rec.Blob.Request.Body)
		lastReq = reqBody
		contents := reqBody.Get("contents").Array()

		prefixBreak := len(contents) < prevCount
		if prefixBreak {
			prevCount = len(contents)
		}
		for k := prevCount; k < len(contents); k++ {
			c := contents[k]
			if c.Get("role").String() == "model" {
				continue // prior assistant response, already emitted
			}
			for _, turn := range geminiUserContentTurns(c) {
				turns = append(turns, turn)
				if turn.Role == "tool" {
					summary.ToolResultCount++
				}
			}
		}
		prevCount = len(contents)

		blocks, callMeta := reconstructGeminiAssistantTurn(rec)
		if prefixBreak {
			callMeta["prefix_break"] = true
		}
		turns = append(turns, TrajTurnV2{
			Role:     "assistant",
			Blocks:   blocks,
			CallMeta: callMeta,
			Content:  deriveText(blocks),
		})
		summary.ToolCallCount += countToolUse(blocks)
		if m := upstreamOrRequestedModel(rec.Record); m != "" {
			assistantModel = m
		}
	}

	if len(turns) < 2 {
		return TrajSessionV2{}, false
	}
	return TrajSessionV2{
		SessionID: sid,
		Meta:      buildGeminiMeta(recs, lastReq, assistantModel),
		Turns:     turns,
	}, true
}

// geminiUserContentTurns splits one user-role content into tool turns (its
// functionResponse parts) and, if it also carries non-functionResponse parts, a
// user turn for those parts.
func geminiUserContentTurns(content gjson.Result) []TrajTurnV2 {
	var toolTurns []TrajTurnV2
	var userParts []any
	content.Get("parts").ForEach(func(_, p gjson.Result) bool {
		if fr := p.Get("functionResponse"); fr.Exists() {
			id := strings.TrimSpace(fr.Get("id").String())
			if id == "" {
				id = strings.TrimSpace(fr.Get("name").String())
			}
			toolTurns = append(toolTurns, TrajTurnV2{
				Role:      "tool",
				ToolUseID: id,
				Content:   jsonValue(fr.Get("response")),
			})
		} else {
			userParts = append(userParts, p.Value())
		}
		return true
	})
	out := toolTurns
	if len(userParts) > 0 {
		out = append(out, TrajTurnV2{Role: "user", Content: userParts})
	}
	return out
}

func reconstructGeminiAssistantTurn(rec SourceRecord) ([]any, map[string]any) {
	respBody := marshalToGJSON(rec.Blob.Response.Body)

	callMeta := baseCallMeta(rec)

	var blocks []any
	parts := respBody.Get("candidates.0.content.parts")
	if parts.IsArray() && len(parts.Array()) > 0 {
		blocks = geminiBlocksFromParts(parts)
		if fr := respBody.Get("candidates.0.finishReason"); fr.Exists() {
			callMeta["stop_reason"] = fr.String()
		}
		callMeta["usage"] = geminiUsage(respBody, rec.Record)
	} else {
		var streamMeta map[string]any
		blocks, streamMeta = geminiBlocksFromStream(rec.Blob.Stream.Chunks)
		for k, v := range streamMeta {
			if _, ok := callMeta[k]; !ok {
				callMeta[k] = v
			}
		}
		if _, ok := callMeta["usage"]; !ok {
			callMeta["usage"] = geminiUsage(respBody, rec.Record)
		}
	}
	if _, ok := callMeta["stop_reason"]; !ok {
		callMeta["stop_reason"] = ""
	}
	callMeta["thinking_source"] = thinkingSource(blocks)
	return blocks, callMeta
}

// geminiBlocksFromParts maps candidate content parts into v2 blocks (thought →
// thinking with signature, functionCall → tool_use, text → text).
func geminiBlocksFromParts(parts gjson.Result) []any {
	out := []any{}
	parts.ForEach(func(_, p gjson.Result) bool {
		switch {
		case p.Get("thought").Bool():
			block := map[string]any{"type": "thinking", "thinking": p.Get("text").String()}
			if sig := p.Get("thoughtSignature").String(); sig != "" {
				block["signature"] = sig
			}
			out = append(out, block)
		case p.Get("functionCall").Exists():
			fc := p.Get("functionCall")
			out = append(out, map[string]any{
				"type":  "tool_use",
				"id":    fc.Get("id").String(),
				"name":  fc.Get("name").String(),
				"input": jsonValue(fc.Get("args")),
			})
		case p.Get("text").Exists():
			out = append(out, map[string]any{"type": "text", "text": p.Get("text").String()})
		}
		return true
	})
	return out
}

// geminiBlocksFromStream reassembles v2 blocks + call_meta from streamed Gemini
// chunks. The client-facing stream is streamGenerateContent?alt=sse (`data:`
// frames, each a partial GeminiResponse); when the client omits alt=sse the
// stream is a JSON array instead — geminiNonSSEPayloads handles that framing so
// non-SSE captures still reconstruct rather than projecting empty/garbage turns.
func geminiBlocksFromStream(chunks []map[string]any) ([]any, map[string]any) {
	payloads, sawAny := sseDataPayloads(chunks)
	if len(payloads) == 0 && sawAny {
		payloads = geminiNonSSEPayloads(chunks)
	}
	meta := map[string]any{}
	var textB, thinkingB strings.Builder
	signature := ""
	var funcCalls []any
	sawFinish := false

	for _, data := range payloads {
		if !gjson.Valid(data) {
			continue
		}
		ev := gjson.Parse(data)
		cand := ev.Get("candidates.0")
		cand.Get("content.parts").ForEach(func(_, p gjson.Result) bool {
			switch {
			case p.Get("thought").Bool():
				_, _ = thinkingB.WriteString(p.Get("text").String())
				if s := p.Get("thoughtSignature").String(); s != "" {
					signature = s
				}
			case p.Get("functionCall").Exists():
				fc := p.Get("functionCall")
				funcCalls = append(funcCalls, map[string]any{
					"type":  "tool_use",
					"id":    fc.Get("id").String(),
					"name":  fc.Get("name").String(),
					"input": jsonValue(fc.Get("args")),
				})
			case p.Get("text").Exists():
				_, _ = textB.WriteString(p.Get("text").String())
			}
			return true
		})
		if fr := cand.Get("finishReason"); fr.Exists() && fr.String() != "" {
			meta["stop_reason"] = fr.String()
			sawFinish = true
		}
		if um := ev.Get("usageMetadata"); um.IsObject() {
			meta["usage"] = geminiUsageFromMetadata(um, meta["usage"])
		}
	}

	if sawAny && !sawFinish {
		meta["truncated"] = true
	}

	blocks := make([]any, 0, 2+len(funcCalls))
	if thinkingB.Len() > 0 {
		block := map[string]any{"type": "thinking", "thinking": thinkingB.String()}
		if signature != "" {
			block["signature"] = signature
		}
		blocks = append(blocks, block)
	}
	if textB.Len() > 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": textB.String()})
	}
	blocks = append(blocks, funcCalls...)
	return blocks, meta
}

// geminiNonSSEPayloads concatenates the captured chunk bytes and, if they form a
// JSON array (or object) — the non-alt=sse streaming framing — returns each
// element's raw JSON so the SSE reassembler can process it uniformly. Returns
// nil for unparseable bytes (the caller then marks the turn truncated).
func geminiNonSSEPayloads(chunks []map[string]any) []string {
	var sb strings.Builder
	for _, c := range chunks {
		raw, _ := c["raw_b64"].(string)
		if raw == "" {
			continue
		}
		if dec, err := base64.StdEncoding.DecodeString(raw); err == nil {
			_, _ = sb.Write(dec)
		}
	}
	s := strings.TrimSpace(sb.String())
	if !gjson.Valid(s) {
		return nil
	}
	parsed := gjson.Parse(s)
	if !parsed.IsArray() {
		return []string{s}
	}
	var out []string
	parsed.ForEach(func(_, el gjson.Result) bool {
		out = append(out, el.Raw)
		return true
	})
	return out
}

func geminiUsage(respBody gjson.Result, record *ent.QARecord) map[string]any {
	usage := map[string]any{}
	if um := respBody.Get("usageMetadata"); um.IsObject() {
		usage = geminiUsageFromMetadata(um, usage)
	}
	fillUsageFromRecord(usage, record)
	return usage
}

// geminiUsageFromMetadata maps Gemini usageMetadata into the v2 usage shape.
// input_tokens excludes cached tokens (matching the anthropic input semantics the
// gateway already uses for gemini), output_tokens sums candidate + thought tokens.
func geminiUsageFromMetadata(um gjson.Result, prev any) map[string]any {
	usage, _ := prev.(map[string]any)
	if usage == nil {
		usage = map[string]any{}
	}
	prompt := um.Get("promptTokenCount").Int()
	cached := um.Get("cachedContentTokenCount").Int()
	input := prompt - cached
	if input < 0 {
		input = prompt
	}
	usage["input_tokens"] = int(input)
	usage["output_tokens"] = int(um.Get("candidatesTokenCount").Int() + um.Get("thoughtsTokenCount").Int())
	usage["cache_read_input_tokens"] = int(cached)
	return usage
}

func buildGeminiMeta(recs []SourceRecord, lastReq gjson.Result, assistantModel string) TrajMetaV2 {
	meta := TrajMetaV2{SchemaVersion: "traj/v2", AssistantModel: assistantModel}
	if lastReq.Exists() {
		if si := lastReq.Get("systemInstruction"); si.Exists() {
			meta.System = geminiSystemText(si)
		}
		if tools := lastReq.Get("tools"); tools.IsArray() {
			meta.ToolSchema = jsonValue(tools)
		}
		sampling := map[string]any{}
		if v := lastReq.Get("model"); v.Exists() {
			sampling["model"] = v.String()
		}
		gc := lastReq.Get("generationConfig")
		if v := gc.Get("temperature"); v.Exists() {
			sampling["temperature"] = v.Num
		}
		if v := gc.Get("topP"); v.Exists() {
			sampling["top_p"] = v.Num
		}
		if tc := gc.Get("thinkingConfig"); tc.Exists() {
			sampling["thinking"] = jsonValue(tc)
		}
		if len(sampling) > 0 {
			meta.Sampling = sampling
		}
	}
	applySynthMeta(&meta, recs)
	return meta
}

// geminiSystemText joins the systemInstruction parts' text; falls back to the
// raw value when the parts aren't simple text.
func geminiSystemText(si gjson.Result) any {
	var sb strings.Builder
	si.Get("parts").ForEach(func(_, p gjson.Result) bool {
		_, _ = sb.WriteString(p.Get("text").String())
		return true
	})
	if sb.Len() > 0 {
		return sb.String()
	}
	return jsonValue(si)
}
