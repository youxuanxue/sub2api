package trajectory

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type decodedSession struct {
	sessionHeader
	Calls []SessionCall `json:"calls"`
	Turns []SessionTurn `json:"turns"`
}

func exportSessions(t *testing.T, inputs ...SessionInput) ([]decodedSession, string) {
	t.Helper()
	e, err := NewSessionExporter(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, e.Close()) })
	for _, input := range inputs {
		require.NoError(t, e.Add(context.Background(), input))
	}
	var b bytes.Buffer
	count, err := e.WriteTo(context.Background(), &b)
	require.NoError(t, err)
	var sessions []decodedSession
	dec := json.NewDecoder(bytes.NewReader(b.Bytes()))
	for dec.More() {
		var s decodedSession
		require.NoError(t, dec.Decode(&s))
		sessions = append(sessions, s)
	}
	require.Len(t, sessions, count)
	return sessions, b.String()
}
func sessionFixture(id, request, response string) SessionInput {
	return SessionInput{RequestID: id, UserID: 7, APIKeyID: 11, Platform: "anthropic", Endpoint: "/v1/messages",
		Evidence: rawJSON(map[string]any{"request": map[string]any{"body": json.RawMessage(request)}, "response": map[string]any{"body": json.RawMessage(response)}, "redactions": []string{"test-v1"}})}
}
func streamFixture(id string, shape WireShape, events ...string) SessionInput {
	var endpoint, request string
	switch shape {
	case WireAnthropicMessages:
		endpoint = "/v1/messages"
		request = `{"messages":[{"role":"user","content":"q"}]}`
	case WireOpenAIResponses:
		endpoint = "/v1/responses"
		request = `{"input":[{"role":"user","content":"q"}]}`
	case WireOpenAIChat:
		endpoint = "/v1/chat/completions"
		request = `{"messages":[{"role":"user","content":"q"}]}`
	case WireGemini:
		endpoint = "/v1beta/models"
		request = `{"contents":[{"role":"user","parts":[{"text":"q"}]}]}`
	}
	in := sessionFixture(id, request, `{}`)
	in.Endpoint = endpoint
	var chunks []map[string]any
	for _, e := range events {
		wire := "data: " + e + "\r\n\r\n"
		mid := len(wire) / 2
		for _, part := range []string{wire[:mid], wire[mid:]} {
			chunks = append(chunks, map[string]any{"raw_b64": base64.StdEncoding.EncodeToString([]byte(part))})
		}
	}
	blob := rawObject(in.Evidence)
	blob["stream"] = rawJSON(map[string]any{"chunks": chunks})
	in.Evidence = rawJSON(blob)
	return in
}

func TestUS055_SessionsReconstructInterleavedToolChain(t *testing.T) {
	first := sessionFixture("first", `{"system":"rules","tools":[{"name":"shell","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"q"}]}`, `{"id":"m1","role":"assistant","content":[{"type":"thinking","thinking":"","signature":"SIG"},{"type":"tool_use","id":"c1","name":"shell","input":{"command":"ls"}}],"usage":{"output_tokens":9},"stop_reason":"tool_use"}`)
	unrelated := sessionFixture("other", `{"messages":[{"role":"user","content":"other"}]}`, `{"content":[{"type":"text","text":"unrelated"}]}`)
	next := sessionFixture("next", `{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"SIG"},{"type":"tool_use","id":"c1","name":"shell","input":{"command":"ls"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"ok","is_error":false},{"type":"text","text":"also explain"}]}]}`, `{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`)
	sessions, raw := exportSessions(t, first, unrelated, next)
	require.Len(t, sessions, 2)
	s := sessions[0]
	require.Len(t, s.Calls, 2)
	require.Len(t, s.Turns, 4)
	require.Equal(t, "next", s.Calls[1].RequestID)
	require.Contains(t, string(s.Turns[2].Message), "also explain")
	link := s.Turns[2].ToolLinks[0]
	require.Equal(t, "matched", link.Status)
	require.Equal(t, "first", link.Call.RequestID)
	require.Equal(t, "/detail/evidence/response/body/content/1", link.Call.Path)
	require.Contains(t, raw, `"signature":"SIG"`)
	require.Contains(t, raw, `"output_tokens":9`)
	require.Equal(t, `"rules"`, string(s.Calls[0].Parameters["system"]))
	_, again := exportSessions(t, first, unrelated, next)
	require.Equal(t, raw, again)
}

func TestUS055_SessionsNeverGuessAcrossScopeRetriesOrAmbiguity(t *testing.T) {
	a := sessionFixture("a", `{"messages":[{"role":"user","content":"same"}]}`, `{"content":[{"type":"text","text":"answer"}]}`)
	b := a
	b.RequestID = "b"
	c := sessionFixture("c", `{"messages":[{"role":"user","content":"same"},{"role":"assistant","content":[{"type":"text","text":"answer"}]},{"role":"user","content":"next"}]}`, `{"content":[{"type":"text","text":"done"}]}`)
	sessions, _ := exportSessions(t, a, b, c)
	require.Len(t, sessions, 3)
	require.Contains(t, sessions[2].Calls[0].Issues, "ambiguous_history")
	for _, change := range []func(*SessionInput){func(in *SessionInput) { in.APIKeyID++ }, func(in *SessionInput) { in.UserID++ }, func(in *SessionInput) { in.Endpoint = "/v1/chat/completions" }} {
		other := c
		change(&other)
		sessions, _ = exportSessions(t, a, other)
		require.Len(t, sessions, 2)
	}
}

func TestUS055_StableSessionRetainsRewrittenHistory(t *testing.T) {
	a := sessionFixture("a", `{"messages":[{"role":"user","content":"before"}]}`, `{"content":[{"type":"text","text":"answer"}]}`)
	a.StableSessionID = "stable"
	b := sessionFixture("b", `{"messages":[{"role":"assistant","content":"compacted summary"},{"role":"user","content":"after"}]}`, `{"content":[{"type":"text","text":"done"}]}`)
	b.StableSessionID = "stable"
	sessions, raw := exportSessions(t, a, b)
	require.Len(t, sessions, 1)
	require.Len(t, sessions[0].Turns, 5)
	require.Contains(t, sessions[0].Calls[1].Issues, "history_boundary")
	require.Contains(t, raw, "compacted summary")
}

func TestUS055_ResponsesReasoningAndNativeFieldsSurvive(t *testing.T) {
	output := `[{"type":"reasoning","id":"rs1","summary":[{"type":"summary_text","text":"visible"}],"encrypted_content":"CIPHER"},{"type":"message","id":"m1","phase":"final_answer","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[]}]}]`
	body := sessionFixture("body", `{"input":[{"role":"user","content":"q"}],"reasoning":{"effort":"high"}}`, `{"status":"completed","output":`+output+`}`)
	body.Endpoint = "/v1/responses"
	stream := streamFixture("stream", WireOpenAIResponses,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs1"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs1","summary":[{"type":"summary_text","text":"visible"}],"encrypted_content":"CIPHER"}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"m1","phase":"final_answer","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[]}]}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[]}}`)
	sessions, raw := exportSessions(t, body, stream)
	require.Len(t, sessions, 2)
	for i := 1; i <= 2; i++ {
		require.JSONEq(t, string(sessions[0].Turns[i].Message), string(sessions[1].Turns[i].Message))
	}
	for _, value := range []string{"CIPHER", "visible", "final_answer", "rs1", "annotations"} {
		require.Contains(t, raw, value)
	}
	require.NotContains(t, sessions[1].Calls[0].Issues, "incomplete_stream")
	require.Equal(t, "/detail/evidence/stream/chunks", sessions[1].Turns[1].Source.Path)
}

func TestUS055_EncryptedOnlyAndAnthropicSignatureOnlyStreams(t *testing.T) {
	encrypted := streamFixture("enc", WireOpenAIResponses,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs1","summary":[],"encrypted_content":"CIPHER_ONLY"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`)
	sig := streamFixture("sig", WireAnthropicMessages,
		`{"type":"message_start","message":{"role":"assistant","id":"m","content":[]}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG_ONLY"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"OPAQUE"}}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"c","name":"shell","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"x\":1}"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`,
		`{"type":"message_stop"}`)
	sessions, raw := exportSessions(t, encrypted, sig)
	require.Len(t, sessions[0].Turns, 2)
	require.Len(t, sessions[1].Turns, 2)
	for _, value := range []string{"CIPHER_ONLY", "SIG_ONLY", "redacted_thinking", "OPAQUE", `"x":1`} {
		require.Contains(t, raw, value)
	}
	require.Equal(t, "c", sessions[1].Turns[1].ToolLinks[0].ID)
}

func TestUS055_MissingTruncatedAndSidecarEvidenceIsAccounted(t *testing.T) {
	missing := SessionInput{RequestID: "missing", UserID: 7, APIKeyID: 11, Endpoint: "/v1/images/generations"}
	truncated := streamFixture("truncated", WireOpenAIResponses, `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"r"}}`)
	sidecar := sessionFixture("sidecar", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"missing-call","content":"x"}]}]}`, `{"content":[{"type":"redacted_thinking","data":"WIRE"}]}`)
	blob := rawObject(sidecar.Evidence)
	resp := rawObject(blob["response"])
	resp["internal_thinking_blocks"] = rawJSON([]string{`{"type":"thinking","thinking":"internal","signature":"SIDE_SIG"}`})
	resp["encrypted_reasoning"] = rawJSON([]string{`{"item_id":"r","encrypted_content":"SIDE_ENC"}`})
	blob["response"] = rawJSON(resp)
	req := rawObject(blob["request"])
	req["upstream_divergent"] = json.RawMessage(`true`)
	blob["request"] = rawJSON(req)
	sidecar.Evidence = rawJSON(blob)
	sessions, raw := exportSessions(t, missing, truncated, sidecar)
	require.Len(t, sessions, 3)
	require.Contains(t, sessions[0].Calls[0].Issues, "missing_or_invalid_evidence")
	require.Contains(t, sessions[0].Calls[0].Issues, "unsupported_wire_shape")
	require.Contains(t, sessions[1].Calls[0].Issues, "incomplete_stream")
	require.Contains(t, sessions[2].Calls[0].Issues, "upstream_request_divergent")
	require.Equal(t, "unresolved", sessions[2].Turns[0].ToolLinks[0].Status)
	for _, s := range []string{"SIDE_SIG", "SIDE_ENC", "WIRE"} {
		require.Contains(t, raw, s)
	}
	require.Len(t, sessions[2].Calls[0].Sidecars, 2)
}

func TestUS055_ChatAndGeminiNativeToolPayloads(t *testing.T) {
	chat := streamFixture("chat", WireOpenAIChat,
		`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"think","tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"shell","arguments":"{\"x\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":"tool_calls"}]}`, `[DONE]`)
	gemini := streamFixture("gemini", WireGemini, `{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thought":true,"text":"reason","thoughtSignature":"GSIG"},{"functionCall":{"id":"g1","name":"shell","args":{"x":1}}}]},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":5}}`)
	sessions, raw := exportSessions(t, chat, gemini)
	require.Equal(t, "c", sessions[0].Turns[1].ToolLinks[0].ID)
	require.Equal(t, "g1", sessions[1].Turns[1].ToolLinks[0].ID)
	for _, value := range []string{"GSIG", "thoughtSignature", "reasoning_content", "totalTokenCount"} {
		require.Contains(t, raw, value)
	}
}

func TestUS055_SessionExportRejectsDuplicatesCancellationAndWriteFailure(t *testing.T) {
	dir := t.TempDir()
	e, err := NewSessionExporter(dir)
	require.NoError(t, err)
	in := sessionFixture("same", `{"messages":[]}`, `{}`)
	require.NoError(t, e.Add(context.Background(), in))
	require.Error(t, e.Add(context.Background(), in))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in.RequestID = "next"
	require.ErrorIs(t, e.Add(ctx, in), context.Canceled)
	_, err = e.WriteTo(ctx, &bytes.Buffer{})
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, e.Close())
	require.NoError(t, os.RemoveAll(dir))
	require.Error(t, e.Add(context.Background(), in))
	failed, err := NewSessionExporter(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, failed.Close()) })
	failed.store.limit = 0
	require.NoError(t, os.RemoveAll(failed.store.dir))
	require.Error(t, failed.Add(context.Background(), in))
}

func TestUS055_StreamToolLinkReferencesSourceAndProjectionSeparately(t *testing.T) {
	first := streamFixture("stream-call", WireAnthropicMessages,
		`{"type":"message_start","message":{"role":"assistant"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"stream-tool","name":"shell","input":{"x":1}}}`,
		`{"type":"message_stop"}`)
	next := sessionFixture("tool-result", `{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[{"type":"tool_use","id":"stream-tool","name":"shell","input":{"x":1}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"stream-tool","content":"ok"}]}]}`, `{"content":[{"type":"text","text":"done"}]}`)
	sessions, _ := exportSessions(t, first, next)
	require.Len(t, sessions, 1)
	source := sessions[0].Turns[2].ToolLinks[0].Call
	require.Equal(t, "stream-call", source.RequestID)
	require.Equal(t, "/detail/evidence/stream/chunks", source.Path)
	require.Equal(t, "/content/0", source.ProjectionPath)
}

func TestUS055_IncompleteNativeResponseDoesNotEstablishContinuity(t *testing.T) {
	in := sessionFixture("incomplete", `{"input":[{"role":"user","content":"q"}]}`, `{"status":"incomplete","output":[{"type":"reasoning","id":"r","encrypted_content":"partial"}]}`)
	in.Endpoint = "/v1/responses"
	sessions, raw := exportSessions(t, in)
	require.Contains(t, sessions[0].Calls[0].Issues, "response_incomplete")
	require.Contains(t, raw, "partial")
}

func TestUS055_SessionStoreSpillPreservesBytesAndBoundsFiles(t *testing.T) {
	first := sessionFixture("first", `{"messages":[{"role":"user","content":"q"}]}`, `{"content":[{"type":"tool_use","id":"c","name":"shell","input":{}}]}`)
	next := sessionFixture("next", `{"messages":[{"content":"q","role":"user"},{"content":[{"input":{},"name":"shell","id":"c","type":"tool_use"}],"role":"assistant"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":"ok"}]}]}`, `{"content":[{"type":"text","text":"done"}]}`)
	_, want := exportSessions(t, first, next)
	dir := t.TempDir()
	e, err := NewSessionExporter(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, e.Close()) })
	require.NoError(t, e.Add(context.Background(), first))
	// Force a transition with existing indexes and fragments, then append a continuation.
	e.store.limit = e.store.used + 1
	require.NoError(t, e.Add(context.Background(), next))
	require.NotNil(t, e.store.tx)
	require.Nil(t, e.store.values)
	require.Nil(t, e.store.fragments)
	var output bytes.Buffer
	count, err := e.WriteTo(context.Background(), &output)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, want, output.String())
	require.ErrorContains(t, e.Add(context.Background(), first), "duplicate")
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, "sessions.sqlite", files[0].Name())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = e.WriteTo(ctx, &bytes.Buffer{})
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, e.Close())
	require.ErrorContains(t, e.Add(context.Background(), next), "closed")
}

func TestUS055_HistoryCacheRemainsBounded(t *testing.T) {
	e, err := NewSessionExporter(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, e.Close()) })
	for n := 0; n < 200; n++ {
		raw := rawJSON(map[string]any{"role": "user", "content": strings.Repeat("x", 16384), "index": n})
		require.Equal(t, canonicalRaw(raw), e.canonicalMessage(raw))
		require.LessOrEqual(t, e.historyCacheBytes, sessionHistoryCacheBytes)
	}
	require.NoError(t, e.Close())
	require.Empty(t, e.historyCache)
}

func TestUS055_CompactFragmentsKeepBudgetAndRejectCorruption(t *testing.T) {
	store := newSessionStore(t.TempDir())
	t.Cleanup(func() { require.NoError(t, store.close()) })
	store.limit = 2048
	original := []byte(strings.Repeat("preserve this native message ", 1024))
	require.NoError(t, store.append(context.Background(), "turns", original))
	require.Nil(t, store.tx, "compressible fragment should fit the fixed budget")
	require.LessOrEqual(t, store.used, store.limit)
	require.Less(t, cap(store.fragments["turns"][0]), len(original))
	check := func(value []byte) error { require.Equal(t, original, value); return nil }
	require.NoError(t, store.each(context.Background(), "turns", check))
	require.NoError(t, store.put(context.Background(), "force-spill", make([]byte, 4096)))
	require.NotNil(t, store.tx)
	require.NoError(t, store.each(context.Background(), "turns", check))
	_, err := store.tx.Exec("UPDATE fragments SET v=? WHERE k=?", []byte{0xff}, "turns")
	require.NoError(t, err)
	called := false
	require.Error(t, store.each(context.Background(), "turns", func([]byte) error { called = true; return nil }))
	require.False(t, called, "corrupt temporary data must not reach the export writer")
}
