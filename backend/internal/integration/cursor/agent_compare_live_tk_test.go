package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type agentTraceBody struct {
	io.Reader
	io.Closer
}

// A bounded native comparison probe. Credential/catalog files and optional wire
// traces stay outside the repository; no production account settings are changed.
func TestAgentLiveToolComparison(t *testing.T) {
	path := os.Getenv("TOKENKEY_CURSOR_ACCOUNT_FILE")
	if path == "" {
		t.Skip("requires an explicit protected account file")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var saved struct {
		Credentials struct {
			Token      string                 `json:"api_key"`
			Mapping    map[string]string      `json:"model_mapping"`
			Wire       map[string]string      `json:"cursor_wire_models"`
			Parameters map[string][]Parameter `json:"cursor_model_parameters"`
		} `json:"credentials"`
	}
	require.NoError(t, json.Unmarshal(raw, &saved))
	model := os.Getenv("TOKENKEY_CURSOR_LIVE_MODEL")
	if model == "" {
		model = "claude-opus-5"
	}
	resolved := saved.Credentials.Mapping[model]
	require.NotEmpty(t, resolved)
	wire := saved.Credentials.Wire[resolved]
	require.NotEmpty(t, wire)
	params, ok := saved.Credentials.Parameters[resolved]
	require.True(t, ok)
	input := AgentRequest{Model: resolved, WireModel: wire, Parameters: params,
		Tools:    []AgentTool{{Name: "read_fixture", Description: "Read a local fixture.", Schema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []any{"path"}, "additionalProperties": false}}},
		Messages: []AgentMessage{{Role: "user", Text: "Call read_fixture with path fixture.txt. After receiving its result, reply with only that exact result. Do not guess."}},
	}
	call := func(turn int) AgentResult {
		t.Helper()
		transport := &http.Transport{ForceAttemptHTTP2: true}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		do := client.Do
		if dir := os.Getenv("TOKENKEY_CURSOR_TRACE_DIR"); dir != "" {
			require.NoError(t, os.MkdirAll(dir, 0700))
			do = func(req *http.Request) (*http.Response, error) {
				out, err := os.OpenFile(filepath.Join(dir, uuid.NewString()+"-request.bin"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					return nil, err
				}
				t.Cleanup(func() { _ = out.Close() })
				req.Body = &agentTraceBody{Reader: io.TeeReader(req.Body, out), Closer: req.Body}
				resp, err := client.Do(req)
				if err != nil {
					return nil, err
				}
				incoming, err := os.OpenFile(filepath.Join(dir, uuid.NewString()+"-response.bin"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					_ = resp.Body.Close()
					return nil, err
				}
				t.Cleanup(func() { _ = incoming.Close() })
				resp.Body = &agentTraceBody{Reader: io.TeeReader(resp.Body, incoming), Closer: resp.Body}
				return resp, nil
			}
		}

		result, err := RunAgent(ctx, saved.Credentials.Token, input, do, nil)
		if err != nil {
			var rejection *AgentRejection
			if errors.As(err, &rejection) {
				t.Logf("native_request_id=%s diagnostic=%s metadata=%s details=%s", rejection.RequestID, rejection.Diagnostic, rejection.Metadata, rejection.DetailInventory)
			}
		}
		require.NoError(t, err, "turn=%d", turn)
		t.Logf("turn=%d handoff=%t calls=%d usage_present=%t", turn, result.ToolHandoff, len(result.ToolCalls), result.Usage != nil)
		return result
	}
	first := call(1)
	require.True(t, first.ToolHandoff)
	require.Len(t, first.ToolCalls, 1)
	require.Equal(t, "read_fixture", first.ToolCalls[0].Name)
	require.Equal(t, "fixture.txt", first.ToolCalls[0].Arguments["path"])
	nonce := "CURSOR_TOOL_" + uuid.NewString()
	t.Logf("tool_call_id=%q", first.ToolCalls[0].ID)
	input.Messages = append(input.Messages, AgentMessage{Role: "assistant", Text: first.Text, ToolCalls: first.ToolCalls}, AgentMessage{Role: "tool", ToolCallID: first.ToolCalls[0].ID, Text: nonce})
	final := call(2)
	require.False(t, final.ToolHandoff)
	require.NotNil(t, final.Usage)
	require.True(t, bytes.Contains([]byte(final.Text), []byte(nonce)))
}
