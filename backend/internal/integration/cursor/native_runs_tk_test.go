package cursor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type nativeFixtureStep struct {
	exec      *pb.ExecServerMessage
	check     func(*pb.ExecClientMessage) error
	wantThrow *pb.ExecClientThrow
}

// A duplex fixture refuses to advance until the ORIGINAL exec ID receives a
// result. Cancellation/history replay cannot satisfy this fixture.
func retainedFixture(t *testing.T, steps []nativeFixtureStep, calls *atomic.Int32) func(*http.Request) (*http.Response, error) {
	t.Helper()
	return func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		reader, writer := io.Pipe()
		go func() {
			defer func() { _ = writer.Close() }()
			stop := context.AfterFunc(req.Context(), func() { _ = writer.CloseWithError(req.Context().Err()) })
			defer stop()
			read := func() (*pb.AgentClientMessage, error) {
				_, raw, err := readAgentFrame(req.Body)
				if err != nil {
					return nil, err
				}
				var message pb.AgentClientMessage
				err = proto.Unmarshal(raw, &message)
				return &message, err
			}
			initial, err := read()
			if err != nil || initial.RunRequest == nil {
				_ = writer.CloseWithError(fmt.Errorf("missing initial run: %v", err))
				return
			}
			for _, step := range steps {
				if err := writeAgentFrame(writer, &pb.AgentServerMessage{ExecServerMessage: step.exec}); err != nil {
					return
				}
				matched := false
				for {
					message, err := read()
					if err != nil {
						_ = writer.CloseWithError(err)
						return
					}
					if message.ConversationAction != nil {
						_ = writer.CloseWithError(fmt.Errorf("upstream was canceled during handoff"))
						return
					}
					if reply := message.ExecClientMessage; reply != nil {
						if step.wantThrow != nil {
							_ = writer.CloseWithError(fmt.Errorf("tool result received instead of expected throw"))
							return
						}
						if reply.Id != step.exec.Id || reply.ExecId != step.exec.ExecId {
							_ = writer.CloseWithError(fmt.Errorf("wrong exec correlation"))
							return
						}
						if step.check != nil {
							if err := step.check(reply); err != nil {
								_ = writer.CloseWithError(err)
								return
							}
						}
						matched = true
					}
					if control := message.ExecClientControlMessage; control != nil && control.Throw != nil {
						if step.wantThrow == nil || !proto.Equal(control.Throw, step.wantThrow) {
							_ = writer.CloseWithError(fmt.Errorf("unexpected tool throw"))
							return
						}
						matched = true
					}
					if control := message.ExecClientControlMessage; control != nil && control.StreamClose != nil {
						if !matched || control.StreamClose.Id != step.exec.Id {
							_ = writer.CloseWithError(fmt.Errorf("exec closed without a matching result"))
							return
						}
						break
					}
				}
			}
			_ = writeAgentFrame(writer, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "client work acknowledged"}}})
			_ = writeAgentFrame(writer, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TurnEnded: &pb.TurnEndedUpdate{InputTokens: proto.Int64(41), OutputTokens: proto.Int64(7), CacheReadTokens: proto.Int64(0), CacheWriteTokens: proto.Int64(0)}}})
		}()
		return &http.Response{StatusCode: 200, ProtoMajor: 2, Header: http.Header{}, Body: reader}, nil
	}
}

func readNativeResponse(t *testing.T, response *http.Response) (AgentToolCall, string) {
	t.Helper()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, 200, response.StatusCode, string(raw))
	var object struct {
		Content []struct {
			Type, ID, Name string
			Input          map[string]any
		}
	}
	if json.Unmarshal(raw, &object) == nil {
		for _, block := range object.Content {
			if block.Type == "tool_use" {
				return AgentToolCall{ID: block.ID, Name: block.Name, Arguments: block.Input}, string(raw)
			}
		}
	} else {
		var call AgentToolCall
		var arguments strings.Builder
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event struct {
				ContentBlock struct {
					Type, ID, Name string
					Input          map[string]any
				} `json:"content_block"`
				Delta struct {
					Partial string `json:"partial_json"`
				} `json:"delta"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
				continue
			}
			if event.ContentBlock.Type == "tool_use" {
				call = AgentToolCall{ID: event.ContentBlock.ID, Name: event.ContentBlock.Name, Arguments: event.ContentBlock.Input}
			}
			_, _ = arguments.WriteString(event.Delta.Partial)
		}
		if call.ID != "" {
			require.NoError(t, json.Unmarshal([]byte(arguments.String()), &call.Arguments))
			return call, string(raw)
		}
	}
	return AgentToolCall{}, string(raw)
}

func TestNativeToolsRetainedRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/missing=%t", stream, missing), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(WithRunOwner(t.Context(), RunOwner{16, 23, 150}), 5*time.Second)
				defer cancel()
				directory := t.TempDir()
				path := filepath.Join(directory, "private")
				marker := filepath.Join(directory, "marker")
				require.NoError(t, os.WriteFile(path, []byte("GATEWAY_SECRET"), 0600))
				nonce := "CLIENT_" + uuid.NewString()
				steps := []nativeFixtureStep{
					{exec: &pb.ExecServerMessage{Id: 71, ExecId: "read-before-edit", ReadArgs: &pb.ReadArgs{Path: path, ToolCallId: "edit"}}, check: func(reply *pb.ExecClientMessage) error {
						if missing {
							if reply.GetReadResult().GetFileNotFound().GetPath() != path {
								return fmt.Errorf("missing-file result was lost")
							}
						} else if reply.GetReadResult().GetSuccess().GetContent() != nonce {
							return fmt.Errorf("client bytes did not reach original read")
						}
						return nil
					}},
					{exec: &pb.ExecServerMessage{Id: 72, ExecId: "write-phase", WriteArgs: &pb.WriteArgs{Path: path, FileText: nonce, ToolCallId: "edit", ReturnFileContentAfterWrite: true}}, check: func(reply *pb.ExecClientMessage) error {
						if reply.GetWriteResult().GetSuccess().GetFileContentAfterWrite() != nonce {
							return fmt.Errorf("write acknowledgement missing")
						}
						return nil
					}},
					{exec: clientBashExec(t, 73, "touch "+marker), check: func(reply *pb.ExecClientMessage) error {
						if reply.GetMcpResult().GetSuccess().GetContent()[0].GetText().GetText() != nonce {
							return fmt.Errorf("shell result lost")
						}
						return nil
					}},
					{exec: &pb.ExecServerMessage{Id: 74, GrepArgs: &pb.GrepArgs{Pattern: nonce, Path: &path}}, check: func(reply *pb.ExecClientMessage) error {
						value := reply.GetGrepResult().GetSuccess().GetWorkspaceResults()[path]
						if value == nil || len(value.GetContent().GetMatches()) != 1 {
							return fmt.Errorf("grep result lost")
						}
						return nil
					}},
				}
				var calls atomic.Int32
				transport := retainedFixture(t, steps, &calls)
				tools := []any{}
				for _, tool := range []AgentTool{nativeTestTool("Read", "file_path"), nativeTestTool("Write", "file_path", "content"), nativeTestTool("Bash", "command"), nativeTestTool("Grep", "pattern", "path", "output_mode")} {
					tools = append(tools, map[string]any{"name": tool.Name, "input_schema": tool.Schema})
				}
				messages := []any{map[string]any{"role": "user", "content": "complete client work"}}
				var lastBody []byte
				for index := 0; index <= len(steps); index++ {
					body, err := json.Marshal(map[string]any{"model": "composer-2.5", "stream": stream, "tools": tools, "messages": messages})
					require.NoError(t, err)
					response, err := Messages(ctx, "test-token", body, nil, "composer-2.5", transport)
					require.NoError(t, err)
					call, raw := readNativeResponse(t, response)
					require.NotContains(t, raw, "GATEWAY_SECRET")
					if index == len(steps) {
						require.Empty(t, call.ID)
						require.Contains(t, raw, `"input_tokens":41`)
						require.Contains(t, raw, "client work acknowledged")
						break
					}
					require.True(t, strings.HasPrefix(call.ID, pendingToolPrefix))
					require.Contains(t, raw, `"input_tokens":0`)
					value := nonce
					failed := false
					if index == 0 && missing {
						value = "File not found"
						failed = true
					}
					if index == 3 {
						value = path + ":1:" + nonce
					}
					messages = append(messages, map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": call.Arguments}}}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": call.ID, "content": value, "is_error": failed}}})
					lastBody, _ = json.Marshal(map[string]any{"model": "composer-2.5", "stream": stream, "tools": tools, "messages": messages})
					account, err := ContinuationAccount(16, 23, lastBody)
					require.NoError(t, err)
					require.EqualValues(t, 150, account)
					_, err = ContinuationAccount(17, 23, lastBody)
					require.ErrorIs(t, err, ErrContinuationUnavailable)
				}
				require.EqualValues(t, 1, calls.Load(), "all tool phases must use one upstream connection")
				retry, err := Messages(ctx, "test-token", lastBody, nil, "composer-2.5", transport)
				require.NoError(t, err)
				require.Equal(t, 400, retry.StatusCode)
				_ = retry.Body.Close()
				require.EqualValues(t, 1, calls.Load())
				actual, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, "GATEWAY_SECRET", string(actual))
				_, err = os.Stat(marker)
				require.True(t, os.IsNotExist(err))
			})
		}
	}
}

func TestNativeClientResultSemantics(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		ok          bool
	}{
		{"plain\ntext\n", "plain\ntext\n", true}, {"1→hello\n2→world\n\n<system-reminder>CLI metadata</system-reminder>", "hello\nworld", true},
		{"1→<system-reminder>file data</system-reminder>", "<system-reminder>file data</system-reminder>", true}, {"     1→hello\n     2→world\n", "hello\nworld\n", true}, {"1\thello\n3\tworld", "", false}, {"[Output truncated at 3 lines]", "", false},
	} {
		actual, ok := clientReadText(tc.input)
		require.Equal(t, tc.ok, ok)
		require.Equal(t, tc.want, actual)
	}
	read := &pb.ExecServerMessage{Id: 9, ReadArgs: &pb.ReadArgs{Path: "/client/path"}}
	empty := nativeClientResults(read, AgentMessage{Text: ""})[0].ExecClientMessage.ReadResult
	raw, err := proto.Marshal(empty)
	require.NoError(t, err)
	var decoded pb.ReadResult
	require.NoError(t, proto.Unmarshal(raw, &decoded))
	require.NotNil(t, decoded.Success.Content, "empty file must retain the content oneof's presence")
	missing := nativeClientResults(read, AgentMessage{IsError: true, Text: "<tool_use_error>File does not exist. Note: your current working directory is /client.</tool_use_error>"})
	require.Equal(t, "/client/path", missing[0].ExecClientMessage.ReadResult.FileNotFound.Path)
	replies := nativeClientResults(read, AgentMessage{IsError: true, Text: "permission denied"})
	require.Equal(t, "permission denied", replies[0].ExecClientMessage.ReadResult.Error.Error)
	require.Nil(t, replies[0].ExecClientMessage.ReadResult.FileNotFound)
	write := &pb.ExecServerMessage{Id: 10, WriteArgs: &pb.WriteArgs{Path: "/client/path", FileText: "new"}}
	replies = nativeClientResults(write, AgentMessage{IsError: true, Text: "user refused"})
	require.Nil(t, replies[0].ExecClientMessage.WriteResult.Success)
	require.Equal(t, "user refused", replies[0].ExecClientMessage.WriteResult.Error.Error)
}

func TestNativeRunIsolationExpiryAndCancellation(t *testing.T) {
	for _, mode := range []string{"expiry", "disconnect", "duplicate", "changed-schema", "changed-history", "wrong-key", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			m := newNativeRunManager()
			m.waitTTL = 80 * time.Millisecond
			m.lifetime = time.Second
			m.maxOwner = 1
			ctx, cancel := context.WithTimeout(WithRunOwner(t.Context(), RunOwner{16, 23, 150}), 2*time.Second)
			defer cancel()
			input := AgentRequest{Model: "composer-2.5", Messages: []AgentMessage{{Role: "user", Text: "read"}}, Tools: []AgentTool{nativeTestTool("Read", "file_path")}}
			var calls atomic.Int32
			transport := retainedFixture(t, []nativeFixtureStep{{exec: &pb.ExecServerMessage{Id: 1, ReadArgs: &pb.ReadArgs{Path: "/client/file"}}}}, &calls)
			first, run, err := m.segment(ctx, "test", input, transport, nil)
			require.NoError(t, err)
			require.True(t, first.ToolHandoff)
			require.Len(t, first.ToolCalls, 1)
			resume := input
			resume.Messages = append(append([]AgentMessage(nil), input.Messages...), AgentMessage{Role: "assistant", ToolCalls: first.ToolCalls}, AgentMessage{Role: "tool", ToolCallID: first.ToolCalls[0].ID, Text: "client result"})
			switch mode {
			case "expiry":
				select {
				case <-run.done:
				case <-ctx.Done():
					t.Fatal("expiry did not close the upstream run")
				}
				_, _, err = m.segment(ctx, "test", resume, transport, nil)
				require.ErrorIs(t, err, ErrContinuationUnavailable)
			case "disconnect":
				m.abort(run)
				select {
				case <-run.done:
				case <-ctx.Done():
					t.Fatal("cancel did not join the run")
				}
			case "duplicate":
				final, _, err := m.segment(ctx, "test", resume, transport, nil)
				require.NoError(t, err)
				require.EqualValues(t, 41, final.Usage.Input)
				_, _, err = m.segment(ctx, "test", resume, transport, nil)
				require.ErrorIs(t, err, ErrContinuationUnavailable)
			case "wrong-key":
				other := WithRunOwner(ctx, RunOwner{16, 24, 150})
				_, _, err = m.segment(other, "test", resume, transport, nil)
				require.ErrorIs(t, err, ErrContinuationUnavailable)
				m.abort(run)
			case "changed-history":
				resume.Messages[0].Text = "different instruction"
				_, _, err = m.segment(ctx, "test", resume, transport, nil)
				require.ErrorIs(t, err, ErrContinuationUnavailable)
				m.abort(run)
			case "changed-schema":
				resume.Tools = []AgentTool{nativeTestTool("Read", "file_path", "offset")}
				_, _, err = m.segment(ctx, "test", resume, transport, nil)
				require.ErrorIs(t, err, ErrContinuationUnavailable)
				m.abort(run)
			case "capacity":
				_, _, err = m.segment(ctx, "test", input, transport, nil)
				require.ErrorIs(t, err, errRunCapacity)
				m.abort(run)
			}
			require.EqualValues(t, 1, calls.Load(), "rejection must not open another upstream")
			m.mu.Lock()
			require.Empty(t, m.pending)
			require.Empty(t, m.runs)
			m.mu.Unlock()
		})
	}
}

func TestPendingToolIDsOnlyLatestResultTurn(t *testing.T) {
	id := newPendingToolID()
	for _, tc := range []struct {
		body string
		want bool
	}{
		{fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":%q}]}]}`, id), true},
		{fmt.Sprintf(`{"messages":[{"role":"tool","tool_call_id":%q}]}`, id), true},
		{fmt.Sprintf(`{"input":[{"type":"function_call_output","call_id":%q}]}`, id), true},
		{fmt.Sprintf(`{"messages":[{"role":"tool","tool_call_id":%q},{"role":"user","content":"next question"}]}`, id), true},
		{fmt.Sprintf(`{"messages":[{"role":"tool","tool_call_id":%q},{"role":"assistant","content":"done"},{"role":"user","content":"next question"}]}`, id), false},
		{fmt.Sprintf(`{"messages":[{"role":"tool","tool_call_id":%q},{"role":"assistant","content":"done"},{"role":"system","content":"rules"},{"role":"user","content":"next question"}]}`, id), false},
		{fmt.Sprintf(`{"input":[{"type":"function_call_output","call_id":%q},{"role":"assistant","content":"done"},{"role":"developer","content":"rules"},{"role":"user","content":"next question"}]}`, id), false},
	} {
		ids := PendingToolIDs([]byte(tc.body))
		if tc.want {
			require.Equal(t, []string{id}, ids)
		} else {
			require.Empty(t, ids)
		}
	}
}
