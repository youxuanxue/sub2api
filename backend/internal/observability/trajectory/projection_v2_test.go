package trajectory

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
)

func mustBody(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("bad test body: %v", err)
	}
	return v
}

func strptr(s string) *string { return &s }

// 两次调用的会话：初始用户指令 → thinking+text+tool_use（call0）→ tool_result →
// 最终 text（call1）。验证 v2 重建出 user/assistant/tool/assistant 四个 turn，
// thinking signature 保留、usage/stop_reason/blocks 顺序齐备、meta 携带 system/tools/sampling。
func TestBuildTrajSessionsV2_Reconstruct(t *testing.T) {
	base := time.Date(2026, 5, 14, 11, 0, 0, 0, time.UTC)

	call0Req := `{"model":"claude-opus-4-6","temperature":1.0,"top_p":0.95,` +
		`"thinking":{"type":"adaptive"},` +
		`"system":[{"type":"text","text":"You are Claude Code"}],` +
		`"tools":[{"name":"Bash","description":"run shell","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],` +
		`"messages":[{"role":"user","content":"build X"}]}`
	call0Resp := `{"id":"msg_01","model":"claude-opus-4-6","stop_reason":"tool_use",` +
		`"usage":{"input_tokens":49,"output_tokens":89,"cache_read_input_tokens":13148},` +
		`"content":[` +
		`{"type":"thinking","thinking":"let me look","signature":"REAL_SIG_123"},` +
		`{"type":"text","text":"Looking at the project."},` +
		`{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls"}}` +
		`]}`

	call1Req := `{"model":"claude-opus-4-6","thinking":{"type":"adaptive"},` +
		`"system":[{"type":"text","text":"You are Claude Code"}],` +
		`"tools":[{"name":"Bash","description":"run shell","input_schema":{"type":"object"}}],` +
		`"messages":[` +
		`{"role":"user","content":"build X"},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"let me look","signature":"REAL_SIG_123"},{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"file1\nfile2"}]}` +
		`]}`
	call1Resp := `{"id":"msg_02","model":"claude-opus-4-6","stop_reason":"end_turn",` +
		`"usage":{"input_tokens":120,"output_tokens":30,"cache_read_input_tokens":13148},` +
		`"content":[{"type":"text","text":"Done."}]}`

	mkBlob := func(req, resp string) *EvidenceBlob {
		b := &EvidenceBlob{}
		b.Request.Body = mustBody(t, req)
		b.Response.Body = mustBody(t, resp)
		return b
	}
	sources := []SourceRecord{
		{
			Record: &ent.QARecord{RequestID: "req_0", CreatedAt: base, Platform: "anthropic", RequestedModel: "claude-opus-4-6", UpstreamModel: strptr("claude-opus-4-6-20260501"), TrajectoryID: strptr("traj-abc"), DialogSynth: true},
			Blob:   mkBlob(call0Req, call0Resp),
		},
		{
			Record: &ent.QARecord{RequestID: "req_1", CreatedAt: base.Add(2 * time.Second), Platform: "anthropic", RequestedModel: "claude-opus-4-6", UpstreamModel: strptr("claude-opus-4-6-20260501"), TrajectoryID: strptr("traj-abc"), DialogSynth: true},
			Blob:   mkBlob(call1Req, call1Resp),
		},
	}

	sessions, summary, err := BuildTrajSessionsV2(sources)
	if err != nil {
		t.Fatalf("BuildTrajSessionsV2: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	s := sessions[0]
	if s.SessionID != "traj-abc" {
		t.Errorf("session id = %q", s.SessionID)
	}
	if s.Meta.SchemaVersion != "traj/v2" {
		t.Errorf("schema_version = %q", s.Meta.SchemaVersion)
	}
	if s.Meta.AssistantModel != "claude-opus-4-6-20260501" {
		t.Errorf("assistant_model = %q", s.Meta.AssistantModel)
	}
	if s.Meta.System == nil {
		t.Errorf("meta.system missing")
	}
	if s.Meta.ToolSchema == nil {
		t.Errorf("meta.tool_schema missing")
	}
	if s.Meta.Sampling == nil || s.Meta.Sampling["thinking"] == nil {
		t.Errorf("meta.sampling.thinking missing: %+v", s.Meta.Sampling)
	}

	if len(s.Turns) != 4 {
		t.Fatalf("want 4 turns, got %d: %+v", len(s.Turns), s.Turns)
	}
	if s.Turns[0].Role != "user" {
		t.Errorf("turn0 role = %q", s.Turns[0].Role)
	}

	// assistant call0
	a0 := s.Turns[1]
	if a0.Role != "assistant" {
		t.Fatalf("turn1 role = %q", a0.Role)
	}
	if len(a0.Blocks) != 3 {
		t.Fatalf("call0 blocks = %d", len(a0.Blocks))
	}
	tb, _ := a0.Blocks[0].(map[string]any)
	if tb["type"] != "thinking" || tb["signature"] != "REAL_SIG_123" {
		t.Errorf("thinking block signature not preserved: %+v", tb)
	}
	if a0.CallMeta["stop_reason"] != "tool_use" {
		t.Errorf("call0 stop_reason = %v", a0.CallMeta["stop_reason"])
	}
	if a0.CallMeta["thinking_source"] != "present" {
		t.Errorf("call0 thinking_source = %v", a0.CallMeta["thinking_source"])
	}
	if a0.CallMeta["thinking_effort"] != "adaptive" {
		t.Errorf("call0 thinking_effort = %v", a0.CallMeta["thinking_effort"])
	}
	usage, _ := a0.CallMeta["usage"].(map[string]any)
	if usage["output_tokens"] != 89 {
		t.Errorf("call0 usage.output_tokens = %v", usage["output_tokens"])
	}

	// tool turn
	if s.Turns[2].Role != "tool" || s.Turns[2].ToolUseID != "tu_1" {
		t.Errorf("turn2 not tool_result: %+v", s.Turns[2])
	}

	// assistant call1
	a1 := s.Turns[3]
	if a1.Role != "assistant" || a1.CallMeta["stop_reason"] != "end_turn" {
		t.Errorf("turn3 wrong: %+v", a1)
	}
	if a1.CallMeta["thinking_source"] != "absent" {
		t.Errorf("call1 thinking_source = %v", a1.CallMeta["thinking_source"])
	}

	if summary.ToolCallCount != 1 || summary.ToolResultCount != 1 {
		t.Errorf("summary counts: %+v", summary)
	}
}

// 回归：真实 prod 里 middleware.TrajectoryID() 每请求一个新 uuid，所以同一会话的多次
// 调用 trajectory_id 各不相同。折叠必须靠 messages 前缀链，而非 trajectory_id —— 两条
// trajectory_id 不同、但 messages 前缀递增的记录，必须合成 ONE session。
func TestBuildTrajSessionsV2_FoldsAcrossPerRequestTrajectoryIDs(t *testing.T) {
	base := time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC)
	call0Req := `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"build X"}]}`
	call0Resp := `{"id":"m0","stop_reason":"tool_use","usage":{"output_tokens":5},` +
		`"content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls"}}]}`
	call1Req := `{"model":"claude-opus-4-8","messages":[` +
		`{"role":"user","content":"build X"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"ok"}]}` +
		`]}`
	call1Resp := `{"id":"m1","stop_reason":"end_turn","usage":{"output_tokens":3},"content":[{"type":"text","text":"done"}]}`

	mkBlob := func(req, resp string) *EvidenceBlob {
		b := &EvidenceBlob{}
		b.Request.Body = mustBody(t, req)
		b.Response.Body = mustBody(t, resp)
		return b
	}
	sources := []SourceRecord{
		{Record: &ent.QARecord{RequestID: "req_0", CreatedAt: base, Platform: "anthropic", TrajectoryID: strptr("traj-AAAA")}, Blob: mkBlob(call0Req, call0Resp)},
		{Record: &ent.QARecord{RequestID: "req_1", CreatedAt: base.Add(2 * time.Second), Platform: "anthropic", TrajectoryID: strptr("traj-BBBB")}, Blob: mkBlob(call1Req, call1Resp)},
	}

	sessions, _, err := BuildTrajSessionsV2(sources)
	if err != nil {
		t.Fatalf("BuildTrajSessionsV2: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("per-request trajectory_ids must still fold into 1 session, got %d", len(sessions))
	}
	if len(sessions[0].Turns) != 4 {
		t.Fatalf("want 4 turns (user, assistant, tool, assistant), got %d", len(sessions[0].Turns))
	}
	if sessions[0].SessionID != "traj-AAAA" {
		t.Errorf("session id should be the first record's trajectory_id, got %q", sessions[0].SessionID)
	}
}

// 反例：两条 request 各自独立、首条 user 消息不同（无前缀关系）→ 必须保持 2 个 session，
// 即使它们 trajectory_id 相同也不能误并。
func TestBuildTrajSessionsV2_DistinctConversationsStaySeparate(t *testing.T) {
	base := time.Date(2026, 6, 15, 8, 0, 0, 0, time.UTC)
	reqA := `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"question A"}]}`
	reqB := `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"unrelated question B"}]}`
	resp := `{"id":"m","stop_reason":"end_turn","usage":{"output_tokens":1},"content":[{"type":"text","text":"ok"}]}`
	mkBlob := func(req string) *EvidenceBlob {
		b := &EvidenceBlob{}
		b.Request.Body = mustBody(t, req)
		b.Response.Body = mustBody(t, resp)
		return b
	}
	sources := []SourceRecord{
		{Record: &ent.QARecord{RequestID: "a", CreatedAt: base, Platform: "anthropic", TrajectoryID: strptr("same")}, Blob: mkBlob(reqA)},
		{Record: &ent.QARecord{RequestID: "b", CreatedAt: base.Add(time.Second), Platform: "anthropic", TrajectoryID: strptr("same")}, Blob: mkBlob(reqB)},
	}
	sessions, _, err := BuildTrajSessionsV2(sources)
	if err != nil {
		t.Fatalf("BuildTrajSessionsV2: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("distinct conversations must stay separate, got %d sessions", len(sessions))
	}
}

// 流式：response.body 为空，从 SSE chunks 重建 thinking+signature+text+tool_use 与 stop_reason。
func TestBuildTrajSessionsV2_StreamReconstruct(t *testing.T) {
	base := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	events := []string{
		`{"type":"message_start","message":{"id":"msg_s","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"ok"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"STREAM_SIG"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tu_9","name":"Bash"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}`,
	}
	chunks := make([]map[string]any, 0, len(events))
	for _, e := range events {
		raw := "event: x\ndata: " + e + "\n\n"
		chunks = append(chunks, map[string]any{
			"t":       0,
			"raw_b64": base64.StdEncoding.EncodeToString([]byte(raw)),
		})
	}
	blob := &EvidenceBlob{}
	blob.Request.Body = mustBody(t, `{"model":"claude-opus-4-6","messages":[{"role":"user","content":"q"}]}`)
	blob.Response.Body = mustBody(t, `{}`)
	blob.Stream.Chunks = chunks

	sources := []SourceRecord{{
		Record: &ent.QARecord{RequestID: "req_s", CreatedAt: base, Platform: "anthropic", RequestedModel: "claude-opus-4-6", TrajectoryID: strptr("traj-stream")},
		Blob:   blob,
	}}

	sessions, _, err := BuildTrajSessionsV2(sources)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(sessions) != 1 || len(sessions[0].Turns) != 2 {
		t.Fatalf("unexpected sessions/turns: %+v", sessions)
	}
	a := sessions[0].Turns[1]
	if len(a.Blocks) != 3 {
		t.Fatalf("stream blocks = %d: %+v", len(a.Blocks), a.Blocks)
	}
	tb, _ := a.Blocks[0].(map[string]any)
	if tb["type"] != "thinking" || tb["thinking"] != "hmm ok" || tb["signature"] != "STREAM_SIG" {
		t.Errorf("thinking reconstruct wrong: %+v", tb)
	}
	tu, _ := a.Blocks[2].(map[string]any)
	if tu["type"] != "tool_use" || tu["id"] != "tu_9" {
		t.Errorf("tool_use reconstruct wrong: %+v", tu)
	}
	input, _ := tu["input"].(map[string]any)
	if input["command"] != "ls" {
		t.Errorf("tool_use input not assembled: %+v", tu["input"])
	}
	if a.CallMeta["stop_reason"] != "tool_use" {
		t.Errorf("stream stop_reason = %v", a.CallMeta["stop_reason"])
	}
}

// 流被截断（无 message_delta / message_stop 终止事件）→ call_meta.truncated=true。
func TestBuildTrajSessionsV2_StreamTruncatedSignal(t *testing.T) {
	base := time.Date(2026, 5, 14, 13, 0, 0, 0, time.UTC)
	events := []string{
		`{"type":"message_start","message":{"id":"msg_t","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
		// 没有 message_delta(stop_reason) / message_stop —— 模拟捕获被截断
	}
	chunks := make([]map[string]any, 0, len(events))
	for _, e := range events {
		raw := "event: x\ndata: " + e + "\n\n"
		chunks = append(chunks, map[string]any{"t": 0, "raw_b64": base64.StdEncoding.EncodeToString([]byte(raw))})
	}
	blob := &EvidenceBlob{}
	blob.Request.Body = mustBody(t, `{"model":"claude-opus-4-6","messages":[{"role":"user","content":"q"}]}`)
	blob.Response.Body = mustBody(t, `{}`)
	blob.Stream.Chunks = chunks

	sources := []SourceRecord{{
		Record: &ent.QARecord{RequestID: "req_t", CreatedAt: base, Platform: "anthropic", RequestedModel: "claude-opus-4-6", TrajectoryID: strptr("traj-trunc")},
		Blob:   blob,
	}}
	sessions, _, err := BuildTrajSessionsV2(sources)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	a := sessions[0].Turns[1]
	if a.CallMeta["truncated"] != true {
		t.Errorf("expected truncated=true for incomplete stream, got: %v", a.CallMeta["truncated"])
	}
}

// 前缀递增被打破（context compaction 缩短历史）→ 不静默丢消息，标 prefix_break 并从新基线续走。
func TestBuildTrajSessionsV2_PrefixBreakSignal(t *testing.T) {
	base := time.Date(2026, 5, 14, 14, 0, 0, 0, time.UTC)
	mkBlob := func(req, resp string) *EvidenceBlob {
		b := &EvidenceBlob{}
		b.Request.Body = mustBody(t, req)
		b.Response.Body = mustBody(t, resp)
		return b
	}
	// call0: 3 messages; call1: compacted to 1 message (prefix shrink)
	call0Req := `{"model":"m","messages":[` +
		`{"role":"user","content":"q1"},` +
		`{"role":"assistant","content":[{"type":"text","text":"a1"}]},` +
		`{"role":"user","content":"q2"}]}`
	call0Resp := `{"id":"msg_p0","stop_reason":"end_turn","content":[{"type":"text","text":"a2"}]}`
	call1Req := `{"model":"m","messages":[{"role":"user","content":"compacted summary"}]}`
	call1Resp := `{"id":"msg_p1","stop_reason":"end_turn","content":[{"type":"text","text":"a3"}]}`

	sources := []SourceRecord{
		{Record: &ent.QARecord{RequestID: "req_p0", CreatedAt: base, Platform: "anthropic", RequestedModel: "m", TrajectoryID: strptr("traj-pb")}, Blob: mkBlob(call0Req, call0Resp)},
		{Record: &ent.QARecord{RequestID: "req_p1", CreatedAt: base.Add(time.Second), Platform: "anthropic", RequestedModel: "m", TrajectoryID: strptr("traj-pb")}, Blob: mkBlob(call1Req, call1Resp)},
	}
	sessions, _, err := BuildTrajSessionsV2(sources)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	turns := sessions[0].Turns
	last := turns[len(turns)-1]
	if last.Role != "assistant" || last.CallMeta["prefix_break"] != true {
		t.Errorf("expected prefix_break=true on post-compaction assistant turn, got: %+v", last.CallMeta)
	}
	// 第一条记录的 turns 不受影响（user q1, q2 + assistant a2）
	if turns[0].Role != "user" || turns[1].Role != "user" {
		t.Errorf("pre-break user turns wrong: %+v %+v", turns[0], turns[1])
	}
}

// 网关改写过请求体的记录（blob 标 upstream_divergent）→ assistant turn 的
// call_meta 显式携带 upstream_request_divergent=true；未改写记录不带该键。
func TestBuildTrajSessionsV2_UpstreamDivergentFlag(t *testing.T) {
	base := time.Date(2026, 5, 14, 15, 0, 0, 0, time.UTC)
	mkBlob := func(divergent bool) *EvidenceBlob {
		b := &EvidenceBlob{}
		b.Request.Body = mustBody(t, `{"model":"m","messages":[{"role":"user","content":"q"}]}`)
		b.Response.Body = mustBody(t, `{"id":"msg_d","stop_reason":"end_turn","content":[{"type":"text","text":"a"}]}`)
		if divergent {
			b.Request.UpstreamBody = mustBody(t, `{"model":"m","messages":[{"role":"user","content":"q"}],"tool_choice":{"type":"auto"}}`)
			b.Request.UpstreamDivergent = true
		}
		return b
	}
	sources := []SourceRecord{
		{Record: &ent.QARecord{RequestID: "req_d0", CreatedAt: base, Platform: "anthropic", RequestedModel: "m", TrajectoryID: strptr("traj-div")}, Blob: mkBlob(true)},
		{Record: &ent.QARecord{RequestID: "req_d1", CreatedAt: base.Add(time.Second), Platform: "anthropic", RequestedModel: "m", TrajectoryID: strptr("traj-div")}, Blob: mkBlob(false)},
	}
	sessions, _, err := BuildTrajSessionsV2(sources)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	var assistants []TrajTurnV2
	for _, turn := range sessions[0].Turns {
		if turn.Role == "assistant" {
			assistants = append(assistants, turn)
		}
	}
	if len(assistants) != 2 {
		t.Fatalf("want 2 assistant turns, got %d", len(assistants))
	}
	if assistants[0].CallMeta["upstream_request_divergent"] != true {
		t.Errorf("divergent call missing upstream_request_divergent: %+v", assistants[0].CallMeta)
	}
	if _, ok := assistants[1].CallMeta["upstream_request_divergent"]; ok {
		t.Errorf("non-divergent call must not carry the key: %+v", assistants[1].CallMeta)
	}
}

func TestMergeInternalThinkingBlocks_ReplacesRedacted(t *testing.T) {
	wire := []any{
		map[string]any{"type": "redacted_thinking", "data": "opaque-hash"},
		map[string]any{"type": "text", "text": "hello"},
	}
	internal := []any{`{"type":"thinking","thinking":"plain reasoning"}`}

	got := mergeInternalThinkingBlocks(wire, internal)
	if len(got) != 2 {
		t.Fatalf("want 2 blocks, got %d: %+v", len(got), got)
	}
	tb, _ := got[0].(map[string]any)
	if tb["type"] != "thinking" || tb["thinking"] != "plain reasoning" {
		t.Errorf("thinking block wrong: %+v", tb)
	}
}

func TestBuildTrajSessionsV2_KiroInternalThinkingFromBlob(t *testing.T) {
	base := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	req := `{"model":"claude-sonnet-4-6","thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hi"}]}`
	resp := `{"id":"msg_k","stop_reason":"end_turn","usage":{"output_tokens":3},"content":[{"type":"redacted_thinking","data":"hash"},{"type":"text","text":"ok"}]}`

	blob := &EvidenceBlob{}
	blob.Request.Body = mustBody(t, req)
	blob.Response.Body = mustBody(t, resp)
	blob.Response.InternalThinkingBlocks = []any{
		`{"type":"thinking","thinking":"kiro plain chain"}`,
	}

	sources := []SourceRecord{{
		Record: &ent.QARecord{
			RequestID:      "req_kiro",
			CreatedAt:      base,
			Platform:       "anthropic",
			RequestedModel: "claude-sonnet-4-6",
			TrajectoryID:   strptr("traj-kiro"),
			DialogSynth:    true,
		},
		Blob: blob,
	}}

	sessions, _, err := BuildTrajSessionsV2(sources)
	if err != nil {
		t.Fatalf("BuildTrajSessionsV2: %v", err)
	}
	if len(sessions) != 1 || len(sessions[0].Turns) != 2 {
		t.Fatalf("unexpected session: %+v", sessions)
	}
	a := sessions[0].Turns[1]
	if len(a.Blocks) != 2 {
		t.Fatalf("blocks = %d: %+v", len(a.Blocks), a.Blocks)
	}
	tb, _ := a.Blocks[0].(map[string]any)
	if tb["type"] != "thinking" || tb["thinking"] != "kiro plain chain" {
		t.Errorf("thinking export wrong: %+v", tb)
	}
	if a.CallMeta["thinking_source"] != "present" {
		t.Errorf("thinking_source = %v", a.CallMeta["thinking_source"])
	}
}
