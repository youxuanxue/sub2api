package trajectory

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/tidwall/gjson"
)

// OpenAI-compat traj v2 projection. Two wire shapes ride OpenAI-compatible
// routes (platforms openai / newapi / grok): Chat Completions (/v1/chat/
// completions) and the Responses API (/v1/responses, Codex). Both reconstruct
// into the SAME TrajSessionV2 vocabulary as the anthropic builder — text /
// thinking / tool_use blocks, user / assistant / tool turns — so the export
// schema is uniform across platforms. reasoning_content (chat) and reasoning
// items / summaries (responses) normalize to `thinking`; tool_calls /
// function_call normalize to `tool_use`; tool messages / function_call_output
// normalize to `tool` turns.

// ---- shared cross-platform helpers (used by openai + gemini builders) -------

// baseCallMeta seeds an assistant turn's call_meta with the fields every shape
// carries: request_id, timestamp, and the upstream-divergence marker.
func baseCallMeta(rec SourceRecord) map[string]any {
	m := map[string]any{
		"request_id": strings.TrimSpace(rec.Record.RequestID),
		"timestamp":  rec.Record.CreatedAt.UTC().Format(time.RFC3339),
	}
	if rec.Blob.Request.UpstreamDivergent {
		m["upstream_request_divergent"] = true
	}
	return m
}

// fillUsageFromRecord backfills usage fields from the QARecord's recorded token
// counts when the response body / stream did not carry them.
func fillUsageFromRecord(usage map[string]any, record *ent.QARecord) {
	if record == nil {
		return
	}
	if _, ok := usage["input_tokens"]; !ok {
		usage["input_tokens"] = record.InputTokens
	}
	if _, ok := usage["output_tokens"]; !ok {
		usage["output_tokens"] = record.OutputTokens
	}
	if _, ok := usage["cache_read_input_tokens"]; !ok {
		usage["cache_read_input_tokens"] = record.CachedTokens
	}
}

// sseDataPayloads decodes the captured base64 SSE chunks and returns every
// `data:` payload in order, plus whether any chunk carried bytes (so a stream
// with no terminal event can be flagged truncated).
func sseDataPayloads(chunks []map[string]any) (payloads []string, sawAny bool) {
	for _, chunk := range chunks {
		raw, _ := chunk["raw_b64"].(string)
		if raw == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			continue
		}
		sawAny = true
		for _, line := range strings.Split(string(decoded), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payloads = append(payloads, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return payloads, sawAny
}

// parseToolArgs decodes a tool-call arguments JSON string into a value; an
// unparseable / empty string falls back to the raw string / empty object.
func parseToolArgs(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
}

// ---- OpenAI Chat Completions ------------------------------------------------

func buildOpenAIChatSession(recs []SourceRecord, summary *ExportSummary) (TrajSessionV2, bool) {
	sid := sessionIDForGroup(recs)
	turns := make([]TrajTurnV2, 0, len(recs)*3)
	prevMsgCount := 0
	var lastReq gjson.Result
	assistantModel := ""

	for _, rec := range recs {
		reqBody := marshalToGJSON(rec.Blob.Request.Body)
		lastReq = reqBody
		msgs := reqBody.Get("messages").Array()

		prefixBreak := len(msgs) < prevMsgCount
		if prefixBreak {
			prevMsgCount = len(msgs)
		}
		for k := prevMsgCount; k < len(msgs); k++ {
			m := msgs[k]
			switch m.Get("role").String() {
			case "user":
				turns = append(turns, TrajTurnV2{Role: "user", Content: jsonValue(m.Get("content"))})
			case "tool", "function":
				turns = append(turns, TrajTurnV2{
					Role:      "tool",
					ToolUseID: strings.TrimSpace(m.Get("tool_call_id").String()),
					Content:   jsonValue(m.Get("content")),
				})
				summary.ToolResultCount++
			case "assistant":
				// prior response, already emitted from its own record — skip.
			case "system", "developer":
				// folded into meta.System — not a turn.
			}
		}
		prevMsgCount = len(msgs)

		blocks, callMeta := reconstructOpenAIChatAssistantTurn(rec)
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
		Meta:      buildOpenAIChatMeta(recs, lastReq, assistantModel),
		Turns:     turns,
	}, true
}

func reconstructOpenAIChatAssistantTurn(rec SourceRecord) ([]any, map[string]any) {
	respBody := marshalToGJSON(rec.Blob.Response.Body)
	reqBody := marshalToGJSON(rec.Blob.Request.Body)

	callMeta := baseCallMeta(rec)
	if eff := reqBody.Get("reasoning_effort"); eff.Exists() {
		callMeta["thinking_effort"] = eff.String()
	}

	var blocks []any
	msg := respBody.Get("choices.0.message")
	if msg.Exists() {
		blocks = openaiChatBlocksFromMessage(msg)
		if fr := respBody.Get("choices.0.finish_reason"); fr.Exists() {
			callMeta["stop_reason"] = fr.String()
		}
		callMeta["usage"] = openaiChatUsage(respBody, rec.Record)
	} else {
		var streamMeta map[string]any
		blocks, streamMeta = openaiChatBlocksFromStream(rec.Blob.Stream.Chunks)
		for k, v := range streamMeta {
			if _, ok := callMeta[k]; !ok {
				callMeta[k] = v
			}
		}
		if _, ok := callMeta["usage"]; !ok {
			callMeta["usage"] = openaiChatUsage(respBody, rec.Record)
		}
	}
	if _, ok := callMeta["stop_reason"]; !ok {
		callMeta["stop_reason"] = ""
	}
	callMeta["thinking_source"] = thinkingSource(blocks)
	return blocks, callMeta
}

// openaiChatBlocksFromMessage maps a Chat Completions assistant message into v2
// blocks (reasoning_content → thinking, content → text, tool_calls → tool_use).
func openaiChatBlocksFromMessage(msg gjson.Result) []any {
	out := []any{}
	if rc := msg.Get("reasoning_content"); rc.Type == gjson.String && rc.String() != "" {
		out = append(out, map[string]any{"type": "thinking", "thinking": rc.String()})
	}
	if c := msg.Get("content"); c.Type == gjson.String && c.String() != "" {
		out = append(out, map[string]any{"type": "text", "text": c.String()})
	}
	msg.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
		out = append(out, map[string]any{
			"type":  "tool_use",
			"id":    tc.Get("id").String(),
			"name":  tc.Get("function.name").String(),
			"input": parseToolArgs(tc.Get("function.arguments").String()),
		})
		return true
	})
	return out
}

// openaiChatBlocksFromStream reassembles v2 blocks + call_meta from Chat
// Completions SSE chunks (choices[].delta content / reasoning_content /
// tool_calls keyed by index; finish_reason; [DONE] terminal; trailing usage).
func openaiChatBlocksFromStream(chunks []map[string]any) ([]any, map[string]any) {
	payloads, sawAny := sseDataPayloads(chunks)
	meta := map[string]any{}
	var textB, reasoningB strings.Builder
	type tcAcc struct {
		id, name string
		args     strings.Builder
	}
	tcByIndex := map[int]*tcAcc{}
	var tcOrder []int
	sawTerminal := false

	for _, data := range payloads {
		if data == "[DONE]" {
			sawTerminal = true
			continue
		}
		if !gjson.Valid(data) {
			continue
		}
		ev := gjson.Parse(data)
		if u := ev.Get("usage"); u.IsObject() {
			meta["usage"] = openaiChatStreamUsage(u, meta["usage"])
		}
		ev.Get("choices").ForEach(func(_, ch gjson.Result) bool {
			d := ch.Get("delta")
			if c := d.Get("content"); c.Type == gjson.String {
				_, _ = textB.WriteString(c.String())
			}
			if rc := d.Get("reasoning_content"); rc.Type == gjson.String {
				_, _ = reasoningB.WriteString(rc.String())
			}
			d.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
				idx := int(tc.Get("index").Int())
				a := tcByIndex[idx]
				if a == nil {
					a = &tcAcc{}
					tcByIndex[idx] = a
					tcOrder = append(tcOrder, idx)
				}
				if id := tc.Get("id").String(); id != "" {
					a.id = id
				}
				if nm := tc.Get("function.name").String(); nm != "" {
					a.name = nm
				}
				_, _ = a.args.WriteString(tc.Get("function.arguments").String())
				return true
			})
			if fr := ch.Get("finish_reason"); fr.Type == gjson.String && fr.String() != "" {
				meta["stop_reason"] = fr.String()
				sawTerminal = true
			}
			return true
		})
	}

	if sawAny && !sawTerminal {
		meta["truncated"] = true
	}

	blocks := make([]any, 0, 2+len(tcOrder))
	if reasoningB.Len() > 0 {
		blocks = append(blocks, map[string]any{"type": "thinking", "thinking": reasoningB.String()})
	}
	if textB.Len() > 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": textB.String()})
	}
	for _, idx := range tcOrder {
		a := tcByIndex[idx]
		blocks = append(blocks, map[string]any{"type": "tool_use", "id": a.id, "name": a.name, "input": parseToolArgs(a.args.String())})
	}
	return blocks, meta
}

func openaiChatUsage(respBody gjson.Result, record *ent.QARecord) map[string]any {
	usage := map[string]any{}
	if u := respBody.Get("usage"); u.IsObject() {
		usage = openaiChatStreamUsage(u, usage)
	}
	fillUsageFromRecord(usage, record)
	return usage
}

func openaiChatStreamUsage(u gjson.Result, prev any) map[string]any {
	usage, _ := prev.(map[string]any)
	if usage == nil {
		usage = map[string]any{}
	}
	if v := u.Get("prompt_tokens"); v.Exists() {
		usage["input_tokens"] = int(v.Int())
	}
	if v := u.Get("completion_tokens"); v.Exists() {
		usage["output_tokens"] = int(v.Int())
	}
	if v := u.Get("prompt_tokens_details.cached_tokens"); v.Exists() {
		usage["cache_read_input_tokens"] = int(v.Int())
	}
	return usage
}

func buildOpenAIChatMeta(recs []SourceRecord, lastReq gjson.Result, assistantModel string) TrajMetaV2 {
	meta := TrajMetaV2{SchemaVersion: "traj/v2", AssistantModel: assistantModel}
	if lastReq.Exists() {
		var systems []any
		lastReq.Get("messages").ForEach(func(_, m gjson.Result) bool {
			if r := m.Get("role").String(); r == "system" || r == "developer" {
				systems = append(systems, jsonValue(m.Get("content")))
			}
			return true
		})
		if len(systems) == 1 {
			meta.System = systems[0]
		} else if len(systems) > 1 {
			meta.System = systems
		}
		if tools := lastReq.Get("tools"); tools.IsArray() {
			meta.ToolSchema = jsonValue(tools)
		}
		meta.Sampling = openaiSampling(lastReq)
	}
	applySynthMeta(&meta, recs)
	return meta
}

// openaiSampling extracts the sampling block shared by chat + responses
// (responses nests effort under reasoning.effort; chat uses reasoning_effort).
func openaiSampling(req gjson.Result) map[string]any {
	sampling := map[string]any{}
	if v := req.Get("model"); v.Exists() {
		sampling["model"] = v.String()
	}
	if v := req.Get("temperature"); v.Exists() {
		sampling["temperature"] = v.Num
	}
	if v := req.Get("top_p"); v.Exists() {
		sampling["top_p"] = v.Num
	}
	if v := req.Get("reasoning_effort"); v.Exists() {
		sampling["reasoning_effort"] = v.String()
	} else if v := req.Get("reasoning.effort"); v.Exists() {
		sampling["reasoning_effort"] = v.String()
	}
	if len(sampling) == 0 {
		return nil
	}
	return sampling
}

// ---- OpenAI Responses API ---------------------------------------------------

func buildOpenAIResponsesSession(recs []SourceRecord, summary *ExportSummary) (TrajSessionV2, bool) {
	sid := sessionIDForGroup(recs)
	turns := make([]TrajTurnV2, 0, len(recs)*3)
	prevItemCount := 0
	var lastReq gjson.Result
	assistantModel := ""

	for _, rec := range recs {
		reqBody := marshalToGJSON(rec.Blob.Request.Body)
		lastReq = reqBody
		input := reqBody.Get("input")
		prefixBreak := false

		if input.Type == gjson.String {
			// Single-shot string input (HTTP rejects previous_response_id, so these
			// records never share a group — emit one user turn).
			if s := strings.TrimSpace(input.String()); s != "" {
				turns = append(turns, TrajTurnV2{Role: "user", Content: s})
			}
		} else {
			items := input.Array()
			prefixBreak = len(items) < prevItemCount
			if prefixBreak {
				prevItemCount = len(items)
			}
			for k := prevItemCount; k < len(items); k++ {
				if turn, ok := responsesInputTurn(items[k]); ok {
					turns = append(turns, turn)
					if turn.Role == "tool" {
						summary.ToolResultCount++
					}
				}
			}
			prevItemCount = len(items)
		}

		blocks, callMeta := reconstructOpenAIResponsesAssistantTurn(rec)
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
		Meta:      buildOpenAIResponsesMeta(recs, lastReq, assistantModel),
		Turns:     turns,
	}, true
}

// responsesInputTurn maps one Responses input item into a user/tool turn, or
// ok=false for items that belong to a prior assistant turn (function_call,
// reasoning) or to meta (system/developer messages).
func responsesInputTurn(item gjson.Result) (TrajTurnV2, bool) {
	switch item.Get("type").String() {
	case "function_call_output":
		return TrajTurnV2{
			Role:      "tool",
			ToolUseID: strings.TrimSpace(item.Get("call_id").String()),
			Content:   jsonValue(item.Get("output")),
		}, true
	case "function_call", "reasoning":
		return TrajTurnV2{}, false // prior assistant output, already emitted
	}
	switch item.Get("role").String() {
	case "user":
		return TrajTurnV2{Role: "user", Content: jsonValue(item.Get("content"))}, true
	default:
		// system / developer (→ meta) and assistant (prior) are not turns.
		return TrajTurnV2{}, false
	}
}

func reconstructOpenAIResponsesAssistantTurn(rec SourceRecord) ([]any, map[string]any) {
	respBody := marshalToGJSON(rec.Blob.Response.Body)
	reqBody := marshalToGJSON(rec.Blob.Request.Body)

	callMeta := baseCallMeta(rec)
	if eff := reqBody.Get("reasoning.effort"); eff.Exists() {
		callMeta["thinking_effort"] = eff.String()
	}

	var blocks []any
	output := respBody.Get("output")
	if output.IsArray() && len(output.Array()) > 0 {
		blocks = openaiResponsesBlocksFromOutput(output)
		if st := respBody.Get("status"); st.Exists() {
			callMeta["stop_reason"] = st.String()
		}
		callMeta["usage"] = openaiResponsesUsage(respBody, rec.Record)
	} else {
		var streamMeta map[string]any
		blocks, streamMeta = openaiResponsesBlocksFromStream(rec.Blob.Stream.Chunks)
		for k, v := range streamMeta {
			if _, ok := callMeta[k]; !ok {
				callMeta[k] = v
			}
		}
		if _, ok := callMeta["usage"]; !ok {
			callMeta["usage"] = openaiResponsesUsage(respBody, rec.Record)
		}
	}
	if _, ok := callMeta["stop_reason"]; !ok {
		callMeta["stop_reason"] = ""
	}
	callMeta["thinking_source"] = thinkingSource(blocks)
	return blocks, callMeta
}

// openaiResponsesBlocksFromOutput maps response.output[] items into v2 blocks
// (reasoning → thinking / redacted_thinking, message output_text → text,
// function_call → tool_use).
func openaiResponsesBlocksFromOutput(output gjson.Result) []any {
	out := []any{}
	output.ForEach(func(_, item gjson.Result) bool {
		switch item.Get("type").String() {
		case "reasoning":
			var sb strings.Builder
			item.Get("summary").ForEach(func(_, s gjson.Result) bool {
				_, _ = sb.WriteString(s.Get("text").String())
				return true
			})
			if sb.Len() > 0 {
				out = append(out, map[string]any{"type": "thinking", "thinking": sb.String()})
			} else if enc := item.Get("encrypted_content").String(); enc != "" {
				out = append(out, map[string]any{"type": "redacted_thinking", "data": enc})
			}
		case "message":
			item.Get("content").ForEach(func(_, c gjson.Result) bool {
				if c.Get("type").String() == "output_text" {
					out = append(out, map[string]any{"type": "text", "text": c.Get("text").String()})
				}
				return true
			})
		case "function_call":
			out = append(out, map[string]any{
				"type":  "tool_use",
				"id":    item.Get("call_id").String(),
				"name":  item.Get("name").String(),
				"input": parseToolArgs(item.Get("arguments").String()),
			})
		}
		return true
	})
	return out
}

// openaiResponsesBlocksFromStream reassembles v2 blocks + call_meta from
// Responses SSE events (output_item.added declares item kinds; output_text /
// reasoning_summary_text / function_call_arguments deltas accumulate by
// output_index; response.completed / failed / incomplete are terminal).
func openaiResponsesBlocksFromStream(chunks []map[string]any) ([]any, map[string]any) {
	payloads, sawAny := sseDataPayloads(chunks)
	meta := map[string]any{}
	type acc struct {
		typ              string
		text, think, arg strings.Builder
		callID, name     string
	}
	byIdx := map[int]*acc{}
	var order []int
	ensure := func(i int) *acc {
		if a, ok := byIdx[i]; ok {
			return a
		}
		a := &acc{}
		byIdx[i] = a
		order = append(order, i)
		return a
	}
	sawTerminal := false

	for _, data := range payloads {
		if data == "[DONE]" {
			sawTerminal = true
			continue
		}
		if !gjson.Valid(data) {
			continue
		}
		ev := gjson.Parse(data)
		switch ev.Get("type").String() {
		case "response.output_item.added":
			a := ensure(int(ev.Get("output_index").Int()))
			it := ev.Get("item")
			switch it.Get("type").String() {
			case "function_call":
				a.typ = "tool_use"
				a.callID = it.Get("call_id").String()
				a.name = it.Get("name").String()
			case "reasoning":
				a.typ = "thinking"
			case "message":
				a.typ = "text"
			}
		case "response.output_text.delta":
			a := ensure(int(ev.Get("output_index").Int()))
			if a.typ == "" {
				a.typ = "text"
			}
			_, _ = a.text.WriteString(ev.Get("delta").String())
		case "response.reasoning_summary_text.delta":
			a := ensure(int(ev.Get("output_index").Int()))
			if a.typ == "" {
				a.typ = "thinking"
			}
			_, _ = a.think.WriteString(ev.Get("delta").String())
		case "response.function_call_arguments.delta":
			a := ensure(int(ev.Get("output_index").Int()))
			if a.typ == "" {
				a.typ = "tool_use"
			}
			_, _ = a.arg.WriteString(ev.Get("delta").String())
			if cid := ev.Get("call_id").String(); cid != "" && a.callID == "" {
				a.callID = cid
			}
			if nm := ev.Get("name").String(); nm != "" && a.name == "" {
				a.name = nm
			}
		case "response.completed", "response.incomplete":
			sawTerminal = true
			if st := ev.Get("response.status"); st.Exists() {
				meta["stop_reason"] = st.String()
			}
			if u := ev.Get("response.usage"); u.IsObject() {
				meta["usage"] = openaiResponsesStreamUsage(u, meta["usage"])
			}
		case "response.failed":
			sawTerminal = true
			meta["stop_reason"] = "failed"
		}
	}

	if sawAny && !sawTerminal {
		meta["truncated"] = true
	}

	blocks := make([]any, 0, len(order))
	for _, i := range order {
		a := byIdx[i]
		switch a.typ {
		case "thinking":
			if a.think.Len() > 0 {
				blocks = append(blocks, map[string]any{"type": "thinking", "thinking": a.think.String()})
			}
		case "text":
			if a.text.Len() > 0 {
				blocks = append(blocks, map[string]any{"type": "text", "text": a.text.String()})
			}
		case "tool_use":
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": a.callID, "name": a.name, "input": parseToolArgs(a.arg.String())})
		}
	}
	return blocks, meta
}

func openaiResponsesUsage(respBody gjson.Result, record *ent.QARecord) map[string]any {
	usage := map[string]any{}
	if u := respBody.Get("usage"); u.IsObject() {
		usage = openaiResponsesStreamUsage(u, usage)
	}
	fillUsageFromRecord(usage, record)
	return usage
}

func openaiResponsesStreamUsage(u gjson.Result, prev any) map[string]any {
	usage, _ := prev.(map[string]any)
	if usage == nil {
		usage = map[string]any{}
	}
	if v := u.Get("input_tokens"); v.Exists() {
		usage["input_tokens"] = int(v.Int())
	}
	if v := u.Get("output_tokens"); v.Exists() {
		usage["output_tokens"] = int(v.Int())
	}
	if v := u.Get("input_tokens_details.cached_tokens"); v.Exists() {
		usage["cache_read_input_tokens"] = int(v.Int())
	}
	return usage
}

func buildOpenAIResponsesMeta(recs []SourceRecord, lastReq gjson.Result, assistantModel string) TrajMetaV2 {
	meta := TrajMetaV2{SchemaVersion: "traj/v2", AssistantModel: assistantModel}
	if lastReq.Exists() {
		if instr := lastReq.Get("instructions"); instr.Type == gjson.String && instr.String() != "" {
			meta.System = instr.String()
		}
		if tools := lastReq.Get("tools"); tools.IsArray() {
			meta.ToolSchema = jsonValue(tools)
		}
		meta.Sampling = openaiSampling(lastReq)
	}
	applySynthMeta(&meta, recs)
	return meta
}
