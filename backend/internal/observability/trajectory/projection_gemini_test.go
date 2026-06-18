package trajectory

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
)

// Gemini: 2-call tool-use conversation (contents[] / candidates[].content.parts)
// reconstructs into user / assistant(thinking+text+tool_use) / tool / assistant,
// thoughtSignature preserved, systemInstruction → meta, usage from usageMetadata.
func TestBuildTrajSessionsV2_Gemini(t *testing.T) {
	base := time.Date(2026, 6, 16, 14, 0, 0, 0, time.UTC)
	req0 := `{"systemInstruction":{"parts":[{"text":"You are Gemini"}]},"generationConfig":{"temperature":0.4},` +
		`"contents":[{"role":"user","parts":[{"text":"build X"}]}]}`
	resp0 := `{"candidates":[{"content":{"role":"model","parts":[` +
		`{"text":"planning","thought":true,"thoughtSignature":"SIG"},` +
		`{"text":"running"},` +
		`{"functionCall":{"name":"bash","args":{"cmd":"ls"},"id":"fc_1"}}` +
		`]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":20,"cachedContentTokenCount":10,"thoughtsTokenCount":5}}`
	req1 := `{"systemInstruction":{"parts":[{"text":"You are Gemini"}]},"contents":[` +
		`{"role":"user","parts":[{"text":"build X"}]},` +
		`{"role":"model","parts":[{"functionCall":{"name":"bash","args":{"cmd":"ls"},"id":"fc_1"}}]},` +
		`{"role":"user","parts":[{"functionResponse":{"name":"bash","id":"fc_1","response":{"result":"file1"}}}]}` +
		`]}`
	resp1 := `{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":80,"candidatesTokenCount":3}}`

	mk := func(id, req, resp string, dt time.Duration) SourceRecord {
		b := &EvidenceBlob{}
		b.Request.Body = mustBody(t, req)
		b.Response.Body = mustBody(t, resp)
		return SourceRecord{
			Record: &ent.QARecord{RequestID: id, CreatedAt: base.Add(dt), Platform: "gemini", InboundEndpoint: "/v1beta/models", RequestedModel: "gemini-2.5-pro", TrajectoryID: strptr("traj-gem")},
			Blob:   b,
		}
	}
	sources := []SourceRecord{mk("g0", req0, resp0, 0), mk("g1", req1, resp1, time.Second)}

	sessions, summary, err := BuildTrajSessionsV2(sources)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(sessions) != 1 || len(sessions[0].Turns) != 4 {
		t.Fatalf("want 1 session / 4 turns, got %d sessions: %+v", len(sessions), sessions)
	}
	s := sessions[0]
	a0 := s.Turns[1]
	if len(a0.Blocks) != 3 {
		t.Fatalf("call0 blocks = %d: %+v", len(a0.Blocks), a0.Blocks)
	}
	th, _ := a0.Blocks[0].(map[string]any)
	if th["type"] != "thinking" || th["thinking"] != "planning" || th["signature"] != "SIG" {
		t.Errorf("thinking block wrong: %+v", th)
	}
	if blockType(t, a0.Blocks[2]) != "tool_use" {
		t.Errorf("call0 block2 not tool_use: %+v", a0.Blocks[2])
	}
	if a0.CallMeta["stop_reason"] != "STOP" {
		t.Errorf("stop_reason = %v", a0.CallMeta["stop_reason"])
	}
	usage, _ := a0.CallMeta["usage"].(map[string]any)
	if usage["input_tokens"] != 40 || usage["output_tokens"] != 25 || usage["cache_read_input_tokens"] != 10 {
		t.Errorf("usage (want input40/output25/cache10): %+v", usage)
	}
	if s.Turns[2].Role != "tool" || s.Turns[2].ToolUseID != "fc_1" {
		t.Errorf("turn2 not tool: %+v", s.Turns[2])
	}
	if s.Turns[3].Role != "assistant" {
		t.Errorf("turn3: %+v", s.Turns[3])
	}
	if s.Meta.System != "You are Gemini" {
		t.Errorf("meta.system = %v", s.Meta.System)
	}
	if summary.ToolCallCount != 1 || summary.ToolResultCount != 1 {
		t.Errorf("summary: %+v", summary)
	}
}

// Gemini streamGenerateContent?alt=sse: text/thought deltas accumulate,
// functionCall part + finishReason on the terminal chunk, usageMetadata mapped.
func TestBuildTrajSessionsV2_GeminiStream(t *testing.T) {
	base := time.Date(2026, 6, 16, 15, 0, 0, 0, time.UTC)
	events := []string{
		`{"candidates":[{"content":{"parts":[{"text":"th","thought":true}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"ink","thought":true,"thoughtSignature":"SS"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"Hel"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"lo"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"bash","args":{"cmd":"ls"},"id":"fc_9"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":6}}`,
	}
	b := &EvidenceBlob{}
	b.Request.Body = mustBody(t, `{"contents":[{"role":"user","parts":[{"text":"q"}]}]}`)
	b.Response.Body = mustBody(t, `{}`)
	b.Stream.Chunks = sseChunks(events)
	sources := []SourceRecord{{
		Record: &ent.QARecord{RequestID: "gs", CreatedAt: base, Platform: "gemini", InboundEndpoint: "/v1beta/models", RequestedModel: "gemini-2.5-pro", TrajectoryID: strptr("traj-gs")},
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
	if th["thinking"] != "think" || th["signature"] != "SS" || tx["text"] != "Hello" || tu["id"] != "fc_9" || input["cmd"] != "ls" {
		t.Errorf("stream blocks wrong: %+v", a.Blocks)
	}
	if a.CallMeta["stop_reason"] != "STOP" {
		t.Errorf("stop_reason = %v", a.CallMeta["stop_reason"])
	}
	if a.CallMeta["truncated"] == true {
		t.Errorf("should not be truncated (finishReason present)")
	}
	usage, _ := a.CallMeta["usage"].(map[string]any)
	if usage["input_tokens"] != 12 {
		t.Errorf("usage = %+v", usage)
	}
}

// Gemini stream truncated (no finishReason on any chunk) → truncated=true.
func TestBuildTrajSessionsV2_GeminiStreamTruncated(t *testing.T) {
	base := time.Date(2026, 6, 16, 15, 30, 0, 0, time.UTC)
	b := &EvidenceBlob{}
	b.Request.Body = mustBody(t, `{"contents":[{"role":"user","parts":[{"text":"q"}]}]}`)
	b.Response.Body = mustBody(t, `{}`)
	b.Stream.Chunks = sseChunks([]string{
		`{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`,
		// no finishReason
	})
	sources := []SourceRecord{{
		Record: &ent.QARecord{RequestID: "gstrunc", CreatedAt: base, Platform: "gemini", InboundEndpoint: "/v1beta/models", TrajectoryID: strptr("traj-gstrunc")},
		Blob:   b,
	}}
	sessions, _, err := BuildTrajSessionsV2(sources)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if sessions[0].Turns[1].CallMeta["truncated"] != true {
		t.Errorf("expected truncated=true, got: %v", sessions[0].Turns[1].CallMeta["truncated"])
	}
}

// H6: Gemini streamed WITHOUT alt=sse is a JSON array (not data:-framed). The
// builder must still reconstruct rather than emit empty/garbage turns.
func TestBuildTrajSessionsV2_GeminiNonSSEStream(t *testing.T) {
	base := time.Date(2026, 6, 16, 16, 0, 0, 0, time.UTC)
	arr := `[{"candidates":[{"content":{"parts":[{"text":"Hello"}]}}]},` +
		`{"candidates":[{"content":{"parts":[{"text":" world"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}]`
	// Raw bytes are NOT "data:"-framed — emulate the non-SSE JSON-array stream.
	b := &EvidenceBlob{}
	b.Request.Body = mustBody(t, `{"contents":[{"role":"user","parts":[{"text":"q"}]}]}`)
	b.Response.Body = mustBody(t, `{}`)
	b.Stream.Chunks = []map[string]any{{"t": 0, "raw_b64": base64.StdEncoding.EncodeToString([]byte(arr))}}
	sources := []SourceRecord{{
		Record: &ent.QARecord{RequestID: "gns", CreatedAt: base, Platform: "gemini", InboundEndpoint: "/v1beta/models", RequestedModel: "gemini-2.5-pro", TrajectoryID: strptr("traj-gns")},
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
	if len(a.Blocks) != 1 || blockType(t, a.Blocks[0]) != "text" {
		t.Fatalf("blocks: %+v", a.Blocks)
	}
	tx, _ := a.Blocks[0].(map[string]any)
	if tx["text"] != "Hello world" {
		t.Errorf("non-sse text reassembly = %v", tx["text"])
	}
	if a.CallMeta["stop_reason"] != "STOP" || a.CallMeta["truncated"] == true {
		t.Errorf("non-sse callMeta = %+v", a.CallMeta)
	}
}

// antigravity dispatch: a /v1 record (anthropic-shaped) reconstructs via the
// anthropic builder; a /v1beta record (gemini-shaped) via the gemini builder.
func TestBuildTrajSessionsV2_AntigravityDispatch(t *testing.T) {
	base := time.Date(2026, 6, 16, 17, 0, 0, 0, time.UTC)

	// antigravity /v1 → /v1/messages → anthropic builder.
	v1r0 := `{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hi"}]}`
	v1resp0 := `{"id":"m0","stop_reason":"tool_use","usage":{"output_tokens":2},"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}`
	v1r1 := `{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`
	v1resp1 := `{"id":"m1","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}`
	mkAnth := func(id, req, resp string, dt time.Duration) SourceRecord {
		b := &EvidenceBlob{}
		b.Request.Body = mustBody(t, req)
		b.Response.Body = mustBody(t, resp)
		return SourceRecord{Record: &ent.QARecord{RequestID: id, CreatedAt: base.Add(dt), Platform: "antigravity", InboundEndpoint: "/v1/messages", TrajectoryID: strptr("ag-v1")}, Blob: b}
	}
	v1sessions, _, err := BuildTrajSessionsV2([]SourceRecord{mkAnth("av0", v1r0, v1resp0, 0), mkAnth("av1", v1r1, v1resp1, time.Second)})
	if err != nil {
		t.Fatalf("v1 err: %v", err)
	}
	if len(v1sessions) != 1 || len(v1sessions[0].Turns) != 4 {
		t.Fatalf("antigravity /v1 should reconstruct via anthropic builder (4 turns), got %+v", v1sessions)
	}

	// antigravity /v1beta → /v1beta/models → gemini builder.
	gb := &EvidenceBlob{}
	gb.Request.Body = mustBody(t, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	gb.Response.Body = mustBody(t, `{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}`)
	v1beta := []SourceRecord{{Record: &ent.QARecord{RequestID: "agb", CreatedAt: base, Platform: "antigravity", InboundEndpoint: "/v1beta/models", TrajectoryID: strptr("ag-v1beta")}, Blob: gb}}
	gsessions, _, err := BuildTrajSessionsV2(v1beta)
	if err != nil {
		t.Fatalf("v1beta err: %v", err)
	}
	if len(gsessions) != 1 || len(gsessions[0].Turns) != 2 {
		t.Fatalf("antigravity /v1beta should reconstruct via gemini builder (2 turns), got %+v", gsessions)
	}
	if tx, _ := gsessions[0].Turns[1].Blocks[0].(map[string]any); tx["text"] != "hello" {
		t.Errorf("v1beta gemini text = %+v", gsessions[0].Turns[1].Blocks)
	}
}
