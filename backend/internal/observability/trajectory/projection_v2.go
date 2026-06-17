package trajectory

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/tidwall/gjson"
)

// traj v2 导出：把每条 QARecord（= 一次 /v1/messages 调用）的捕获 blob 重建为
// 富化 session/turns（见 traj/pipeline/schemas/traj_v2.json）。
//
// 核心：已验证「调用即 message」——相邻调用的 request.messages 是严格递增前缀，
// 故按 created_at 升序遍历，用增量取出新出现的 user/tool_result 消息成 turn，
// 本调用的 response 成一个带 blocks + call_meta 的 assistant turn。assistant 历史
// 消息（前一次 response）跳过，避免重复。

type TrajSessionV2 struct {
	SessionID string       `json:"session_id"`
	Meta      TrajMetaV2   `json:"meta"`
	Turns     []TrajTurnV2 `json:"turns"`
}

type TrajMetaV2 struct {
	SchemaVersion    string         `json:"schema_version"`
	DialogSynth      bool           `json:"dialog_synth"`
	AssistantModel   string         `json:"assistant_model,omitempty"`
	UserPersonaModel string         `json:"user_persona_model,omitempty"`
	EngineerLevel    string         `json:"engineer_level,omitempty"`
	SynthSessionID   string         `json:"synth_session_id,omitempty"`
	TrajectoryID     string         `json:"trajectory_id,omitempty"`
	System           any            `json:"system,omitempty"`
	Sampling         map[string]any `json:"sampling,omitempty"`
	ToolSchema       any            `json:"tool_schema,omitempty"`
}

type TrajTurnV2 struct {
	Role      string         `json:"role"`
	Content   any            `json:"content,omitempty"`
	Blocks    []any          `json:"blocks,omitempty"`
	CallMeta  map[string]any `json:"call_meta,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

// BuildTrajSessionsV2 把多条记录聚合为按 session 的 v2 轨迹（每 session 一个对象）。
func BuildTrajSessionsV2(sources []SourceRecord) ([]TrajSessionV2, ExportSummary, error) {
	// Group by conversation via message-prefix chaining, NOT by trajectory_id.
	// middleware.TrajectoryID() mints a fresh uuid per HTTP request, so grouping
	// by trajectory_id (resolveSessionID) put every /v1/messages call in its own
	// "session" and defeated the prefix-dedup below — a conversation's N calls
	// exploded into N redundant nested-prefix lines. groupByConversation folds
	// them back into one session. See groupByConversation.
	groups := groupByConversation(sources)

	sessions := make([]TrajSessionV2, 0, len(groups))
	summary := ExportSummary{}
	for _, recs := range groups {
		sid := sessionIDForGroup(recs)

		turns := make([]TrajTurnV2, 0, len(recs)*3)
		prevMsgCount := 0
		var lastReq gjson.Result
		assistantModel := ""

		for _, rec := range recs {
			reqBody := marshalToGJSON(rec.Blob.Request.Body)
			lastReq = reqBody
			msgs := reqBody.Get("messages").Array()

			// 前缀递增假设被打破（如长会话 context compaction 重写/缩短历史）时，
			// 增量无法对齐——显式标记并从新基线续走，绝不静默跳过当作没发生。
			prefixBreak := len(msgs) < prevMsgCount
			if prefixBreak {
				prevMsgCount = len(msgs)
			}

			for k := prevMsgCount; k < len(msgs); k++ {
				m := msgs[k]
				switch m.Get("role").String() {
				case "user":
					if trs := toolResultTurns(m); len(trs) > 0 {
						turns = append(turns, trs...)
						summary.ToolResultCount += len(trs)
					} else {
						turns = append(turns, TrajTurnV2{Role: "user", Content: jsonValue(m.Get("content"))})
					}
				case "assistant":
					// 前一次调用的 response，已由其记录的 response 发出，跳过避免重复。
				}
			}
			prevMsgCount = len(msgs)

			blocks, callMeta := reconstructAssistantTurn(rec)
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
			continue
		}
		sessions = append(sessions, TrajSessionV2{
			SessionID: sid,
			Meta:      buildMetaV2(recs, lastReq, assistantModel),
			Turns:     turns,
		})
		summary.SessionCount++
	}
	summary.RecordCount = len(sources)
	return sessions, summary, nil
}

// groupByConversation 把记录按「会话」聚类——同一会话的连续 /v1/messages 调用，其
// request.messages 是严格递增前缀。按 created_at 升序遍历：当前记录的 messages 若是
// 前一条的前缀扩展（messages[:len(prev)] 深度相等），或更短（context compaction 收缩
// 历史），则续同会话；否则起新会话。这取代了原先按 per-request trajectory_id 分组——
// 那让每次调用各成一个 session，把投影器的前缀去重逻辑架空。
//
// 局限：仅与「前一条」比较。若两个不同会话恰好共享开头前缀，可能被并到一起（罕见，
// 且远好于今天「一个会话碎成 N 行」）。彻底解法是客户端传稳定会话 id，超出本函数范围。
func groupByConversation(sources []SourceRecord) [][]SourceRecord {
	valid := make([]SourceRecord, 0, len(sources))
	for _, s := range sources {
		if s.Record == nil || s.Blob == nil {
			continue
		}
		valid = append(valid, s)
	}
	// 导出查询已按 created_at 升序，但单测可能直接传切片——防御性再排一次。
	sort.SliceStable(valid, func(i, j int) bool {
		if !valid[i].Record.CreatedAt.Equal(valid[j].Record.CreatedAt) {
			return valid[i].Record.CreatedAt.Before(valid[j].Record.CreatedAt)
		}
		return valid[i].Record.RequestID < valid[j].Record.RequestID
	})

	var groups [][]SourceRecord
	var prevMsgs []gjson.Result
	for _, s := range valid {
		msgs := marshalToGJSON(s.Blob.Request.Body).Get("messages").Array()
		continued := len(groups) > 0 && messagesContinue(prevMsgs, msgs)
		if continued {
			groups[len(groups)-1] = append(groups[len(groups)-1], s)
		} else {
			groups = append(groups, []SourceRecord{s})
		}
		prevMsgs = msgs
	}
	return groups
}

// messagesContinue 报告 cur 是否延续 prev 所在的会话：cur 是 prev 的前缀扩展，
// 或更短（context compaction 收缩了历史）。
func messagesContinue(prev, cur []gjson.Result) bool {
	return len(cur) < len(prev) || messagesEqualPrefix(prev, cur)
}

// RequestMessagesContinue 是 messagesContinue 的导出包装，供流式导出按会话边界增量
// flush（无需把整批记录的 blob 同时读进内存）。入参是两条记录各自的 request.body。
func RequestMessagesContinue(prevReqBody, curReqBody any) bool {
	prev := marshalToGJSON(prevReqBody).Get("messages").Array()
	cur := marshalToGJSON(curReqBody).Get("messages").Array()
	return messagesContinue(prev, cur)
}

// messagesEqualPrefix 报告 cur 的前 len(prev) 条消息是否与 prev 逐条深度相等。
func messagesEqualPrefix(prev, cur []gjson.Result) bool {
	if len(cur) < len(prev) {
		return false
	}
	for i := range prev {
		if canonicalJSON(prev[i].Raw) != canonicalJSON(cur[i].Raw) {
			return false
		}
	}
	return true
}

// canonicalJSON 把一段 JSON 规范化（map key 排序、数字归一），使网关重新 marshal
// 导致的 key 顺序差异不会让逻辑相同的消息被判为不同。非法 JSON 回退到 trim 后的原文。
func canonicalJSON(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return strings.TrimSpace(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return string(b)
}

// sessionIDForGroup 取会话首条记录的 trajectory_id 作为稳定 session_id（重复导出不变、
// 每会话唯一）；缺失则回退 request_id。
func sessionIDForGroup(group []SourceRecord) string {
	for _, s := range group {
		if s.Record == nil {
			continue
		}
		if id := strings.TrimSpace(derefString(s.Record.TrajectoryID)); id != "" {
			return id
		}
		if id := strings.TrimSpace(s.Record.RequestID); id != "" {
			return id
		}
		return "unknown"
	}
	return "unknown"
}

func buildMetaV2(recs []SourceRecord, lastReq gjson.Result, assistantModel string) TrajMetaV2 {
	meta := TrajMetaV2{
		SchemaVersion:  "traj/v2",
		AssistantModel: assistantModel,
	}
	if lastReq.Exists() {
		if sys := lastReq.Get("system"); sys.Exists() {
			meta.System = jsonValue(sys)
		}
		if tools := lastReq.Get("tools"); tools.IsArray() {
			meta.ToolSchema = jsonValue(tools)
		}
		sampling := map[string]any{}
		if v := lastReq.Get("model"); v.Exists() {
			sampling["model"] = v.String()
		}
		if v := lastReq.Get("temperature"); v.Exists() {
			sampling["temperature"] = v.Num
		}
		if v := lastReq.Get("top_p"); v.Exists() {
			sampling["top_p"] = v.Num
		}
		if v := lastReq.Get("thinking"); v.Exists() {
			sampling["thinking"] = jsonValue(v)
		}
		if len(sampling) > 0 {
			meta.Sampling = sampling
		}
	}
	// synth/opt-in 字段（若有）。
	for _, rec := range recs {
		r := rec.Record
		if r == nil {
			continue
		}
		if r.DialogSynth {
			meta.DialogSynth = true
		}
		if meta.SynthSessionID == "" && r.SynthSessionID != nil {
			meta.SynthSessionID = strings.TrimSpace(*r.SynthSessionID)
		}
		if meta.EngineerLevel == "" && r.SynthEngineerLevel != nil {
			meta.EngineerLevel = strings.TrimSpace(*r.SynthEngineerLevel)
		}
		if meta.TrajectoryID == "" && r.TrajectoryID != nil {
			meta.TrajectoryID = strings.TrimSpace(*r.TrajectoryID)
		}
	}
	return meta
}

// reconstructAssistantTurn 从一条记录的 blob 重建 assistant turn 的 blocks 与 call_meta。
// 优先用 response.body.content（非流式）；为空则从 SSE chunks 重建（流式）。
func reconstructAssistantTurn(rec SourceRecord) ([]any, map[string]any) {
	respBody := marshalToGJSON(rec.Blob.Response.Body)
	reqBody := marshalToGJSON(rec.Blob.Request.Body)

	callMeta := map[string]any{
		"request_id": strings.TrimSpace(rec.Record.RequestID),
		"timestamp":  rec.Record.CreatedAt.UTC().Format(time.RFC3339),
	}
	if eff := reqBody.Get("thinking.type"); eff.Exists() {
		callMeta["thinking_effort"] = eff.String()
	}
	// 网关在转发前改写过请求体（非 passthrough 路径的 normalize / signature-preempt
	// 剥 thinking 等）：本调用的「捕获请求」≠「产生该响应的真实上游请求」。显式标记
	// 而非静默——traj 消费侧据此决定丢弃或降级使用该样本。
	if rec.Blob.Request.UpstreamDivergent {
		callMeta["upstream_request_divergent"] = true
	}

	var blocks []any
	content := respBody.Get("content")
	if content.IsArray() && len(content.Array()) > 0 {
		blocks = blocksFromContentArray(content)
		if sr := respBody.Get("stop_reason"); sr.Exists() {
			callMeta["stop_reason"] = sr.String()
		}
		callMeta["usage"] = usageFromBody(respBody, rec.Record)
	} else {
		var streamMeta map[string]any
		blocks, streamMeta = blocksFromStreamChunks(rec.Blob.Stream.Chunks)
		for k, v := range streamMeta {
			if _, ok := callMeta[k]; !ok {
				callMeta[k] = v
			}
		}
		if _, ok := callMeta["usage"]; !ok {
			callMeta["usage"] = usageFromBody(respBody, rec.Record)
		}
	}
	if _, ok := callMeta["stop_reason"]; !ok {
		callMeta["stop_reason"] = ""
	}
	callMeta["thinking_source"] = thinkingSource(blocks)
	return blocks, callMeta
}

func usageFromBody(respBody gjson.Result, record *ent.QARecord) map[string]any {
	usage := map[string]any{}
	if u := respBody.Get("usage"); u.IsObject() {
		if v := u.Get("input_tokens"); v.Exists() {
			usage["input_tokens"] = int(v.Int())
		}
		if v := u.Get("output_tokens"); v.Exists() {
			usage["output_tokens"] = int(v.Int())
		}
		if v := u.Get("cache_read_input_tokens"); v.Exists() {
			usage["cache_read_input_tokens"] = int(v.Int())
		}
	}
	if record != nil {
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
	return usage
}

// blocksFromContentArray 把 Anthropic content[] 映射成 v2 blocks（只保留 v2 关心的字段）。
func blocksFromContentArray(content gjson.Result) []any {
	out := []any{}
	content.ForEach(func(_, b gjson.Result) bool {
		switch b.Get("type").String() {
		case "thinking":
			block := map[string]any{"type": "thinking", "thinking": b.Get("thinking").String()}
			if sig := b.Get("signature").String(); sig != "" {
				block["signature"] = sig
			}
			out = append(out, block)
		case "redacted_thinking":
			out = append(out, map[string]any{"type": "redacted_thinking", "data": b.Get("data").String()})
		case "text":
			out = append(out, map[string]any{"type": "text", "text": b.Get("text").String()})
		case "tool_use":
			out = append(out, map[string]any{
				"type":  "tool_use",
				"id":    b.Get("id").String(),
				"name":  b.Get("name").String(),
				"input": jsonValue(b.Get("input")),
			})
		}
		return true
	})
	return out
}

// blocksFromStreamChunks 从 Anthropic SSE chunks 重建 blocks 与 call_meta（stop_reason/usage）。
func blocksFromStreamChunks(chunks []map[string]any) ([]any, map[string]any) {
	type acc struct {
		typ       string
		text      strings.Builder
		thinking  strings.Builder
		signature string
		partial   strings.Builder // tool_use input json
		id        string
		name      string
	}
	byIndex := map[int]*acc{}
	order := []int{}
	meta := map[string]any{}
	sawTerminal := false
	sawAnyChunk := false

	ensure := func(i int) *acc {
		if a, ok := byIndex[i]; ok {
			return a
		}
		a := &acc{}
		byIndex[i] = a
		order = append(order, i)
		return a
	}

	for _, chunk := range chunks {
		raw, _ := chunk["raw_b64"].(string)
		if raw == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			continue
		}
		sawAnyChunk = true
		for _, line := range strings.Split(string(decoded), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if !gjson.Valid(data) {
				continue
			}
			ev := gjson.Parse(data)
			switch ev.Get("type").String() {
			case "content_block_start":
				i := int(ev.Get("index").Int())
				a := ensure(i)
				cb := ev.Get("content_block")
				a.typ = cb.Get("type").String()
				if a.typ == "tool_use" {
					a.id = cb.Get("id").String()
					a.name = cb.Get("name").String()
				}
			case "content_block_delta":
				i := int(ev.Get("index").Int())
				a := ensure(i)
				d := ev.Get("delta")
				switch d.Get("type").String() {
				case "text_delta":
					_, _ = a.text.WriteString(d.Get("text").String())
					if a.typ == "" {
						a.typ = "text"
					}
				case "thinking_delta":
					_, _ = a.thinking.WriteString(d.Get("thinking").String())
					if a.typ == "" {
						a.typ = "thinking"
					}
				case "signature_delta":
					a.signature += d.Get("signature").String()
				case "input_json_delta":
					_, _ = a.partial.WriteString(d.Get("partial_json").String())
				}
			case "message_delta":
				if sr := ev.Get("delta.stop_reason"); sr.Exists() && sr.String() != "" {
					meta["stop_reason"] = sr.String()
					sawTerminal = true
				}
				if u := ev.Get("usage"); u.IsObject() {
					meta["usage"] = streamUsage(u, meta["usage"])
				}
			case "message_stop":
				sawTerminal = true
			case "message_start":
				if u := ev.Get("message.usage"); u.IsObject() {
					meta["usage"] = streamUsage(u, meta["usage"])
				}
			}
		}
	}

	// 流被捕获截断（如撞上 body 上限）时没有终止事件——显式标 truncated，
	// 避免「不完整的 assistant turn」被下游当成完整。非 silent 是确定性原则要求。
	if sawAnyChunk && !sawTerminal {
		meta["truncated"] = true
	}

	blocks := make([]any, 0, len(order))
	for _, i := range order {
		a := byIndex[i]
		switch a.typ {
		case "thinking":
			block := map[string]any{"type": "thinking", "thinking": a.thinking.String()}
			if a.signature != "" {
				block["signature"] = a.signature
			}
			blocks = append(blocks, block)
		case "text":
			blocks = append(blocks, map[string]any{"type": "text", "text": a.text.String()})
		case "tool_use":
			var input any = map[string]any{}
			if s := a.partial.String(); s != "" {
				_ = json.Unmarshal([]byte(s), &input)
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": a.id, "name": a.name, "input": input})
		}
	}
	return blocks, meta
}

func streamUsage(u gjson.Result, prev any) map[string]any {
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
	if v := u.Get("cache_read_input_tokens"); v.Exists() {
		usage["cache_read_input_tokens"] = int(v.Int())
	}
	return usage
}

// toolResultTurns 把一个携带 tool_result 块的 user 消息拆成若干 tool turn。
func toolResultTurns(msg gjson.Result) []TrajTurnV2 {
	content := msg.Get("content")
	if !content.IsArray() {
		return nil
	}
	out := []TrajTurnV2{}
	content.ForEach(func(_, b gjson.Result) bool {
		if b.Get("type").String() != "tool_result" {
			return true
		}
		out = append(out, TrajTurnV2{
			Role:      "tool",
			ToolUseID: b.Get("tool_use_id").String(),
			Content:   jsonValue(b.Get("content")),
			IsError:   b.Get("is_error").Bool(),
		})
		return true
	})
	return out
}

func deriveText(blocks []any) string {
	var sb strings.Builder
	for _, b := range blocks {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "text" {
			if t, ok := m["text"].(string); ok {
				_, _ = sb.WriteString(t)
			}
		}
	}
	return sb.String()
}

func thinkingSource(blocks []any) string {
	for _, b := range blocks {
		if m, ok := b.(map[string]any); ok {
			if m["type"] == "thinking" || m["type"] == "redacted_thinking" {
				return "present"
			}
		}
	}
	return "absent"
}

func countToolUse(blocks []any) int {
	n := 0
	for _, b := range blocks {
		if m, ok := b.(map[string]any); ok && m["type"] == "tool_use" {
			n++
		}
	}
	return n
}

func upstreamOrRequestedModel(record *ent.QARecord) string {
	if record == nil {
		return ""
	}
	if record.UpstreamModel != nil && strings.TrimSpace(*record.UpstreamModel) != "" {
		return strings.TrimSpace(*record.UpstreamModel)
	}
	return strings.TrimSpace(record.RequestedModel)
}

func marshalToGJSON(v any) gjson.Result {
	if v == nil {
		return gjson.Result{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return gjson.Result{}
	}
	return gjson.ParseBytes(b)
}

// jsonValue 把 gjson.Result 转回普通 Go 值（map/[]any/标量），供 JSON 序列化。
func jsonValue(r gjson.Result) any {
	if !r.Exists() {
		return nil
	}
	return r.Value()
}
