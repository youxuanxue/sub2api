package trajectory

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
)

// sseChunks turns SSE event JSON strings into captured base64 chunks (matches
// the qa capture format: each chunk holds one `data: {...}\n\n` frame).
func sseChunks(events []string) []map[string]any {
	chunks := make([]map[string]any, 0, len(events))
	for _, e := range events {
		raw := "data: " + e + "\n\n"
		chunks = append(chunks, map[string]any{"t": 0, "raw_b64": base64.StdEncoding.EncodeToString([]byte(raw))})
	}
	return chunks
}

func blockType(t *testing.T, b any) string {
	t.Helper()
	m, ok := b.(map[string]any)
	if !ok {
		t.Fatalf("block not a map: %T", b)
	}
	s, _ := m["type"].(string)
	return s
}

// OpenAI Chat Completions: 2-call tool-use conversation reconstructs into
// user / assistant(thinking+text+tool_use) / tool / assistant(text), with the
// system message folded into meta and usage mapped from prompt/completion_tokens.
func TestBuildTrajSessionsV2_OpenAIChat(t *testing.T) {
	base := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	req0 := `{"model":"gpt-5","temperature":0.7,"messages":[` +
		`{"role":"system","content":"You are helpful"},` +
		`{"role":"user","content":"build X"}]}`
	resp0 := `{"choices":[{"message":{"role":"assistant","reasoning_content":"planning",` +
		`"content":"Let me run it","tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"cmd\":\"ls\"}"}}]},` +
		`"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":50,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":10}}}`
	req1 := `{"model":"gpt-5","messages":[` +
		`{"role":"system","content":"You are helpful"},` +
		`{"role":"user","content":"build X"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"cmd\":\"ls\"}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"file1\nfile2"}]}`
	resp1 := `{"choices":[{"message":{"role":"assistant","content":"Done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":80,"completion_tokens":5}}`

	mk := func(id, req, resp string, dt time.Duration) SourceRecord {
		b := &EvidenceBlob{}
		b.Request.Body = mustBody(t, req)
		b.Response.Body = mustBody(t, resp)
		return SourceRecord{
			Record: &ent.QARecord{RequestID: id, CreatedAt: base.Add(dt), Platform: "openai", InboundEndpoint: "/v1/chat/completions", RequestedModel: "gpt-5", TrajectoryID: strptr("traj-oa")},
			Blob:   b,
		}
	}
	sources := []SourceRecord{mk("c0", req0, resp0, 0), mk("c1", req1, resp1, time.Second)}

	sessions, summary, err := BuildTrajSessionsV2(sources)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	s := sessions[0]
	if len(s.Turns) != 4 {
		t.Fatalf("want 4 turns, got %d: %+v", len(s.Turns), s.Turns)
	}
	if s.Turns[0].Role != "user" {
		t.Errorf("turn0 = %q", s.Turns[0].Role)
	}
	a0 := s.Turns[1]
	if a0.Role != "assistant" || len(a0.Blocks) != 3 {
		t.Fatalf("turn1 wrong: %+v", a0)
	}
	if blockType(t, a0.Blocks[0]) != "thinking" || blockType(t, a0.Blocks[1]) != "text" || blockType(t, a0.Blocks[2]) != "tool_use" {
		t.Errorf("call0 block order wrong: %+v", a0.Blocks)
	}
	if a0.CallMeta["stop_reason"] != "tool_calls" || a0.CallMeta["thinking_source"] != "present" {
		t.Errorf("call0 callMeta: %+v", a0.CallMeta)
	}
	usage, _ := a0.CallMeta["usage"].(map[string]any)
	if usage["input_tokens"] != 50 || usage["output_tokens"] != 20 || usage["cache_read_input_tokens"] != 10 {
		t.Errorf("call0 usage: %+v", usage)
	}
	if s.Turns[2].Role != "tool" || s.Turns[2].ToolUseID != "call_1" {
		t.Errorf("turn2 not tool: %+v", s.Turns[2])
	}
	if s.Turns[3].Role != "assistant" || s.Turns[3].CallMeta["stop_reason"] != "stop" {
		t.Errorf("turn3 wrong: %+v", s.Turns[3])
	}
	if s.Meta.System != "You are helpful" {
		t.Errorf("meta.system = %v", s.Meta.System)
	}
	if s.Meta.Sampling == nil || s.Meta.Sampling["model"] != "gpt-5" {
		t.Errorf("meta.sampling = %+v", s.Meta.Sampling)
	}
	if summary.ToolCallCount != 1 || summary.ToolResultCount != 1 {
		t.Errorf("summary: %+v", summary)
	}
}

// OpenAI Chat streaming: content/reasoning/tool_call argument deltas reassemble
// into thinking+text+tool_use, finish_reason → stop_reason, trailing usage chunk.
func TestBuildTrajSessionsV2_OpenAIChatStream(t *testing.T) {
	base := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)
	events := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning_content":"hmm"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"Hel"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"lo"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"bash","arguments":"{\"cmd\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"usage":{"prompt_tokens":10,"completion_tokens":5},"choices":[]}`,
		`[DONE]`,
	}
	b := &EvidenceBlob{}
	b.Request.Body = mustBody(t, `{"model":"gpt-5","messages":[{"role":"user","content":"q"}]}`)
	b.Response.Body = mustBody(t, `{}`)
	b.Stream.Chunks = sseChunks(events)
	sources := []SourceRecord{{
		Record: &ent.QARecord{RequestID: "cs", CreatedAt: base, Platform: "newapi", InboundEndpoint: "/v1/chat/completions", RequestedModel: "gpt-5", TrajectoryID: strptr("traj-cs")},
		Blob:   b,
	}}

	sessions, _, err := BuildTrajSessionsV2(sources)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(sessions) != 1 || len(sessions[0].Turns) != 2 {
		t.Fatalf("unexpected: %+v", sessions)
	}
	a := sessions[0].Turns[1]
	if len(a.Blocks) != 3 {
		t.Fatalf("blocks = %d: %+v", len(a.Blocks), a.Blocks)
	}
	th, _ := a.Blocks[0].(map[string]any)
	if th["thinking"] != "hmm" {
		t.Errorf("thinking = %v", th["thinking"])
	}
	tx, _ := a.Blocks[1].(map[string]any)
	if tx["text"] != "Hello" {
		t.Errorf("text = %v", tx["text"])
	}
	tu, _ := a.Blocks[2].(map[string]any)
	input, _ := tu["input"].(map[string]any)
	if tu["id"] != "call_9" || input["cmd"] != "ls" {
		t.Errorf("tool_use = %+v", tu)
	}
	if a.CallMeta["stop_reason"] != "tool_calls" {
		t.Errorf("stop_reason = %v", a.CallMeta["stop_reason"])
	}
	usage, _ := a.CallMeta["usage"].(map[string]any)
	if usage["input_tokens"] != 10 {
		t.Errorf("usage = %+v", usage)
	}
}

// OpenAI Responses (Codex): input[] items + output[] items reconstruct into
// user / assistant(thinking+text+tool_use) / tool / assistant(text), instructions
// → meta, usage from input/output_tokens.
func TestBuildTrajSessionsV2_OpenAIResponses(t *testing.T) {
	base := time.Date(2026, 6, 16, 11, 0, 0, 0, time.UTC)
	req0 := `{"model":"gpt-5-codex","instructions":"You are Codex","reasoning":{"effort":"high"},"input":[` +
		`{"type":"message","role":"user","content":"build X"}]}`
	resp0 := `{"status":"completed","output":[` +
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"planning"}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"running"}]},` +
		`{"type":"function_call","call_id":"fc_1","name":"bash","arguments":"{\"cmd\":\"ls\"}"}` +
		`],"usage":{"input_tokens":40,"output_tokens":15,"input_tokens_details":{"cached_tokens":5}}}`
	req1 := `{"model":"gpt-5-codex","instructions":"You are Codex","input":[` +
		`{"type":"message","role":"user","content":"build X"},` +
		`{"type":"function_call","call_id":"fc_1","name":"bash","arguments":"{\"cmd\":\"ls\"}"},` +
		`{"type":"function_call_output","call_id":"fc_1","output":"file1"}]}`
	resp1 := `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":60,"output_tokens":3}}`

	mk := func(id, req, resp string, dt time.Duration) SourceRecord {
		b := &EvidenceBlob{}
		b.Request.Body = mustBody(t, req)
		b.Response.Body = mustBody(t, resp)
		return SourceRecord{
			Record: &ent.QARecord{RequestID: id, CreatedAt: base.Add(dt), Platform: "openai", InboundEndpoint: "/v1/responses", RequestedModel: "gpt-5-codex", TrajectoryID: strptr("traj-rsp")},
			Blob:   b,
		}
	}
	sources := []SourceRecord{mk("r0", req0, resp0, 0), mk("r1", req1, resp1, time.Second)}

	sessions, summary, err := BuildTrajSessionsV2(sources)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(sessions) != 1 || len(sessions[0].Turns) != 4 {
		t.Fatalf("want 1 session / 4 turns, got %d sessions: %+v", len(sessions), sessions)
	}
	s := sessions[0]
	a0 := s.Turns[1]
	if len(a0.Blocks) != 3 || blockType(t, a0.Blocks[0]) != "thinking" || blockType(t, a0.Blocks[2]) != "tool_use" {
		t.Errorf("call0 blocks: %+v", a0.Blocks)
	}
	if a0.CallMeta["stop_reason"] != "completed" || a0.CallMeta["thinking_effort"] != "high" {
		t.Errorf("call0 callMeta: %+v", a0.CallMeta)
	}
	usage, _ := a0.CallMeta["usage"].(map[string]any)
	if usage["input_tokens"] != 40 || usage["output_tokens"] != 15 || usage["cache_read_input_tokens"] != 5 {
		t.Errorf("usage: %+v", usage)
	}
	if s.Turns[2].Role != "tool" || s.Turns[2].ToolUseID != "fc_1" {
		t.Errorf("turn2 not tool: %+v", s.Turns[2])
	}
	if s.Turns[3].Role != "assistant" {
		t.Errorf("turn3: %+v", s.Turns[3])
	}
	if s.Meta.System != "You are Codex" {
		t.Errorf("meta.system = %v", s.Meta.System)
	}
	if summary.ToolCallCount != 1 || summary.ToolResultCount != 1 {
		t.Errorf("summary: %+v", summary)
	}
}

// OpenAI Responses single-shot string input → one user turn + assistant.
func TestBuildTrajSessionsV2_OpenAIResponsesStringInput(t *testing.T) {
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	b := &EvidenceBlob{}
	b.Request.Body = mustBody(t, `{"model":"gpt-5","instructions":"sys","input":"hello there"}`)
	b.Response.Body = mustBody(t, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":3,"output_tokens":2}}`)
	sources := []SourceRecord{{
		Record: &ent.QARecord{RequestID: "rs", CreatedAt: base, Platform: "openai", InboundEndpoint: "/v1/responses", RequestedModel: "gpt-5", TrajectoryID: strptr("traj-str")},
		Blob:   b,
	}}
	sessions, _, err := BuildTrajSessionsV2(sources)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(sessions) != 1 || len(sessions[0].Turns) != 2 {
		t.Fatalf("want 1 session / 2 turns, got %+v", sessions)
	}
	if sessions[0].Turns[0].Role != "user" || sessions[0].Turns[0].Content != "hello there" {
		t.Errorf("user turn: %+v", sessions[0].Turns[0])
	}
}

// OpenAI Responses streaming: output_item.added declares kinds; text/reasoning/
// function-args deltas reassemble; response.completed is terminal with usage.
func TestBuildTrajSessionsV2_OpenAIResponsesStream(t *testing.T) {
	base := time.Date(2026, 6, 16, 13, 0, 0, 0, time.UTC)
	events := []string{
		`{"type":"response.created","response":{"id":"resp_1"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning"}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"think"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","role":"assistant"}}`,
		`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"Hel"}`,
		`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"lo"}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","call_id":"fc_9","name":"bash"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"call_id":"fc_9","name":"bash","delta":"{\"cmd\":\"ls\"}"}`,
		`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":12,"output_tokens":6}}}`,
	}
	b := &EvidenceBlob{}
	b.Request.Body = mustBody(t, `{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"q"}]}`)
	b.Response.Body = mustBody(t, `{}`)
	b.Stream.Chunks = sseChunks(events)
	sources := []SourceRecord{{
		Record: &ent.QARecord{RequestID: "rstream", CreatedAt: base, Platform: "openai", InboundEndpoint: "/v1/responses", RequestedModel: "gpt-5-codex", TrajectoryID: strptr("traj-rstr")},
		Blob:   b,
	}}
	sessions, _, err := BuildTrajSessionsV2(sources)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(sessions) != 1 || len(sessions[0].Turns) != 2 {
		t.Fatalf("unexpected: %+v", sessions)
	}
	a := sessions[0].Turns[1]
	if len(a.Blocks) != 3 {
		t.Fatalf("blocks = %d: %+v", len(a.Blocks), a.Blocks)
	}
	th, _ := a.Blocks[0].(map[string]any)
	tx, _ := a.Blocks[1].(map[string]any)
	tu, _ := a.Blocks[2].(map[string]any)
	input, _ := tu["input"].(map[string]any)
	if th["thinking"] != "think" || tx["text"] != "Hello" || tu["id"] != "fc_9" || input["cmd"] != "ls" {
		t.Errorf("stream blocks wrong: %+v", a.Blocks)
	}
	if a.CallMeta["stop_reason"] != "completed" {
		t.Errorf("stop_reason = %v", a.CallMeta["stop_reason"])
	}
	usage, _ := a.CallMeta["usage"].(map[string]any)
	if usage["input_tokens"] != 12 {
		t.Errorf("usage = %+v", usage)
	}
}
