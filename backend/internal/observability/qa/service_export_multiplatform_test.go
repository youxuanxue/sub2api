//go:build unit

package qa

// Multi-platform traj export (Commit D): with the handler's anthropic pin
// removed, ExportUserTrajectoryData streams a key's records and the v2 projector
// dispatches each conversation group by wire shape. These tests prove the
// streaming export folds + dispatches correctly across shapes, never merges two
// shapes into one session, splits a single newapi key's chat-vs-gemini records,
// projects kiro via the anthropic builder, and skips non-conversation records.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// readTrajSessionsV2 reads the trajectory.jsonl out of an export zip and parses
// each line into a generic session map.
func readTrajSessionsV2(t *testing.T, store *memBlobStore, storageKey string) []map[string]any {
	t.Helper()
	blob := store.objects[storageKey]
	require.NotEmpty(t, blob, "export zip is non-empty")
	zr, err := zip.NewReader(bytes.NewReader(blob), int64(len(blob)))
	require.NoError(t, err)
	require.Len(t, zr.File, 1)
	require.Equal(t, "trajectory.jsonl", zr.File[0].Name)
	f, err := zr.File[0].Open()
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	require.NoError(t, err)

	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		out = append(out, m)
	}
	return out
}

func turnsOf(t *testing.T, session map[string]any) []any {
	t.Helper()
	turns, _ := session["turns"].([]any)
	return turns
}

// jsonToMap parses a JSON object string into the map[string]any blob bodies
// expected by encodeEvidenceBlobForTest.
func jsonToMap(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &m))
	return m
}

// assistantText returns the assistant turn's derived text content (the `content`
// field on assistant turns), used to tell shapes apart in the export.
func lastAssistantText(t *testing.T, session map[string]any) string {
	t.Helper()
	turns := turnsOf(t, session)
	for i := len(turns) - 1; i >= 0; i-- {
		turn, _ := turns[i].(map[string]any)
		if turn["role"] == "assistant" {
			if s, ok := turn["content"].(string); ok {
				return s
			}
		}
	}
	return ""
}

// A key carrying an openai-chat conversation + a gemini conversation + a
// non-conversation (embeddings) record exports into exactly two sessions — one
// per shape, correctly reconstructed, with no cross-shape merge and the
// embeddings record skipped.
func TestExportUserTrajectoryData_MultiPlatformShapeDispatch(t *testing.T) {
	svc, client, store := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	chatBlob := func(req, resp string) map[string]any {
		return map[string]any{
			"request":  map[string]any{"path": "/v1/chat/completions", "body": jsonToMap(t, req)},
			"response": map[string]any{"status_code": 200, "headers": map[string]any{}, "body": jsonToMap(t, resp)},
			"stream":   map[string]any{"chunks": []any{}},
		}
	}
	// openai chat: 2-call tool-use conversation under key 1.
	mustInsertQARecordWithBlob(t, ctx, client, store,
		qaRecordBuilder{requestID: "oa0", userID: 7, apiKeyID: 1, createdAt: now, platform: "openai", inboundEndpoint: "/v1/chat/completions"},
		chatBlob(
			`{"model":"gpt-5","messages":[{"role":"system","content":"sys"},{"role":"user","content":"build X"}]}`,
			`{"choices":[{"message":{"role":"assistant","content":"step1","tool_calls":[{"id":"c1","type":"function","function":{"name":"bash","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		))
	mustInsertQARecordWithBlob(t, ctx, client, store,
		qaRecordBuilder{requestID: "oa1", userID: 7, apiKeyID: 1, createdAt: now.Add(time.Second), platform: "openai", inboundEndpoint: "/v1/chat/completions"},
		chatBlob(
			`{"model":"gpt-5","messages":[{"role":"system","content":"sys"},{"role":"user","content":"build X"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"bash","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"out"}]}`,
			`{"choices":[{"message":{"role":"assistant","content":"OA_DONE"},"finish_reason":"stop"}]}`,
		))
	// gemini: single-shot conversation under the same key.
	mustInsertQARecordWithBlob(t, ctx, client, store,
		qaRecordBuilder{requestID: "gm0", userID: 7, apiKeyID: 1, createdAt: now.Add(2 * time.Second), platform: "gemini", inboundEndpoint: "/v1beta/models"},
		map[string]any{
			"request":  map[string]any{"path": "/v1beta/models", "body": jsonToMap(t, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)},
			"response": map[string]any{"status_code": 200, "headers": map[string]any{}, "body": jsonToMap(t, `{"candidates":[{"content":{"parts":[{"text":"GM_HELLO"}]},"finishReason":"STOP"}]}`)},
			"stream":   map[string]any{"chunks": []any{}},
		})
	// embeddings: non-conversation record → Unknown shape → skipped.
	mustInsertQARecordWithBlob(t, ctx, client, store,
		qaRecordBuilder{requestID: "emb0", userID: 7, apiKeyID: 1, createdAt: now.Add(3 * time.Second), platform: "openai", inboundEndpoint: "/v1/embeddings"},
		map[string]any{
			"request":  map[string]any{"path": "/v1/embeddings", "body": jsonToMap(t, `{"input":"x"}`)},
			"response": map[string]any{"status_code": 200, "headers": map[string]any{}, "body": jsonToMap(t, `{"data":[{"embedding":[0.1]}]}`)},
			"stream":   map[string]any{"chunks": []any{}},
		})

	key1 := int64(1)
	res, err := svc.ExportUserTrajectoryData(ctx, 7, ExportFilter{APIKeyID: &key1, Format: "v2"})
	require.NoError(t, err)

	sessions := readTrajSessionsV2(t, store, res.StorageKey)
	require.Len(t, sessions, 2, "openai + gemini = 2 sessions; embeddings skipped, no cross-shape merge")

	var openaiSession, geminiSession map[string]any
	for _, s := range sessions {
		switch lastAssistantText(t, s) {
		case "OA_DONE":
			openaiSession = s
		case "GM_HELLO":
			geminiSession = s
		}
	}
	require.NotNil(t, openaiSession, "openai session present")
	require.NotNil(t, geminiSession, "gemini session present (not merged into openai)")
	require.Len(t, turnsOf(t, openaiSession), 4, "openai: user/assistant/tool/assistant")
	require.Len(t, turnsOf(t, geminiSession), 2, "gemini: user/assistant")
}

// A single newapi key whose records span /v1/chat/completions and /v1beta/models
// (newapi ch41 vertex) splits into two sessions of different shapes — wire shape
// is per record, not per platform.
func TestExportUserTrajectoryData_NewapiSplitsChatVsGemini(t *testing.T) {
	svc, client, store := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mustInsertQARecordWithBlob(t, ctx, client, store,
		qaRecordBuilder{requestID: "np-chat", userID: 9, apiKeyID: 3, createdAt: now, platform: "newapi", inboundEndpoint: "/v1/chat/completions"},
		map[string]any{
			"request":  map[string]any{"path": "/v1/chat/completions", "body": jsonToMap(t, `{"model":"deepseek","messages":[{"role":"user","content":"q"}]}`)},
			"response": map[string]any{"status_code": 200, "headers": map[string]any{}, "body": jsonToMap(t, `{"choices":[{"message":{"role":"assistant","content":"NP_CHAT"},"finish_reason":"stop"}]}`)},
			"stream":   map[string]any{"chunks": []any{}},
		})
	mustInsertQARecordWithBlob(t, ctx, client, store,
		qaRecordBuilder{requestID: "np-gem", userID: 9, apiKeyID: 3, createdAt: now.Add(time.Second), platform: "newapi", inboundEndpoint: "/v1beta/models"},
		map[string]any{
			"request":  map[string]any{"path": "/v1beta/models", "body": jsonToMap(t, `{"contents":[{"role":"user","parts":[{"text":"q"}]}]}`)},
			"response": map[string]any{"status_code": 200, "headers": map[string]any{}, "body": jsonToMap(t, `{"candidates":[{"content":{"parts":[{"text":"NP_GEM"}]},"finishReason":"STOP"}]}`)},
			"stream":   map[string]any{"chunks": []any{}},
		})

	key3 := int64(3)
	res, err := svc.ExportUserTrajectoryData(ctx, 9, ExportFilter{APIKeyID: &key3, Format: "v2"})
	require.NoError(t, err)
	sessions := readTrajSessionsV2(t, store, res.StorageKey)
	require.Len(t, sessions, 2, "same newapi key, two wire shapes → two sessions")

	texts := map[string]bool{}
	for _, s := range sessions {
		texts[lastAssistantText(t, s)] = true
	}
	require.True(t, texts["NP_CHAT"], "newapi chat session reconstructed")
	require.True(t, texts["NP_GEM"], "newapi gemini session reconstructed (not merged with chat)")
}

// Kiro records relay the anthropic /v1/messages shape and project via the
// anthropic builder.
func TestExportUserTrajectoryData_KiroProjectsViaAnthropic(t *testing.T) {
	svc, client, store := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mustInsertQARecordWithBlob(t, ctx, client, store,
		qaRecordBuilder{requestID: "kiro0", userID: 11, apiKeyID: 5, createdAt: now, platform: "kiro", inboundEndpoint: "/v1/messages"},
		map[string]any{
			"request":  map[string]any{"path": "/v1/messages", "body": jsonToMap(t, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}]}`)},
			"response": map[string]any{"status_code": 200, "headers": map[string]any{}, "body": jsonToMap(t, `{"id":"m","stop_reason":"end_turn","content":[{"type":"text","text":"KIRO_OK"}]}`)},
			"stream":   map[string]any{"chunks": []any{}},
		})

	key5 := int64(5)
	res, err := svc.ExportUserTrajectoryData(ctx, 11, ExportFilter{APIKeyID: &key5, Format: "v2"})
	require.NoError(t, err)
	sessions := readTrajSessionsV2(t, store, res.StorageKey)
	require.Len(t, sessions, 1)
	require.Equal(t, "KIRO_OK", lastAssistantText(t, sessions[0]))
}
