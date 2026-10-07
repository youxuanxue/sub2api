package cursor

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/stretchr/testify/require"
)

// Buffered gateway consumers may stop at the terminal delta without reading message_stop.
// Read one SSE event at a time to prevent asynchronous read-ahead from hiding premature cleanup.
func TestNativeContinuationTerminalClose(t *testing.T) {
	for _, terminal := range []string{"content_block_stop", "message_delta", "message_stop"} {
		t.Run(terminal, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(WithRunOwner(t.Context(), RunOwner{16, 23, 150}), 5*time.Second)
			defer cancel()
			var calls atomic.Int32
			transport := retainedFixture(t, []nativeFixtureStep{{exec: &pb.ExecServerMessage{Id: 1, ReadArgs: &pb.ReadArgs{Path: "/client/file"}}}}, &calls)
			body := []byte(`{"model":"composer-2.5","stream":true,"tools":[{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}}],"messages":[{"role":"user","content":"read"}]}`)
			response, err := Messages(ctx, "test-token", body, nil, "composer-2.5", transport)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			reader := bufio.NewReader(response.Body)
			var seen strings.Builder
			for {
				var event strings.Builder
				for {
					line, err := reader.ReadString('\n')
					require.NoError(t, err)
					_, _ = event.WriteString(line)
					if line == "\n" {
						break
					}
				}
				_, _ = seen.WriteString(event.String())
				if strings.Contains(event.String(), "event: "+terminal+"\n") {
					break
				}
			}
			if terminal != "content_block_stop" {
				require.Contains(t, seen.String(), `"stop_reason":"tool_use"`)
			}
			id := regexp.MustCompile(`toolu_gw_[a-f0-9]+`).FindString(seen.String())
			require.NotEmpty(t, id)
			resume := []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":"client bytes"}]}]}`, id))
			nativeRuns.mu.Lock()
			run := nativeRuns.pending[id]
			nativeRuns.mu.Unlock()
			if run != nil {
				defer nativeRuns.abort(run)
			}
			require.NoError(t, response.Body.Close())
			account, err := ContinuationAccount(16, 23, resume)
			if terminal == "content_block_stop" {
				require.ErrorIs(t, err, ErrContinuationUnavailable, "incomplete handoffs must be reclaimed")
				require.Zero(t, account)
				return
			}
			require.NoError(t, err, "a delivered terminal delta must retain the continuation")
			require.EqualValues(t, 150, account)
			resumeBody := strings.Replace(string(body), `[{"role":"user","content":"read"}]`,
				fmt.Sprintf(`[{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":"client bytes"}]}]`, id), 1)
			resumed, err := Messages(ctx, "test-token", []byte(resumeBody), nil, "composer-2.5", transport)
			require.NoError(t, err)
			finalCall, raw := readNativeResponse(t, resumed)
			require.Empty(t, finalCall.ID)
			require.Contains(t, raw, "client work acknowledged")
			require.Contains(t, raw, `"input_tokens":41`)
			require.EqualValues(t, 1, calls.Load(), "continuation must use the original upstream run")
		})
	}
}
