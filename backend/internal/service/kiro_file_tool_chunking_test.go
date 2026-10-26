//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kiro "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUS070_FileToolChunkingAccountOptIn(t *testing.T) {
	require.False(t, (*Account)(nil).IsKiroFileToolChunkingEnabled())
	for _, platform := range []string{PlatformKiro, PlatformAnthropic, PlatformOpenAI} {
		for _, flag := range []any{nil, false, true, "true", 1, float64(1)} {
			t.Run(fmt.Sprintf("%s/%T/%v", platform, flag, flag), func(t *testing.T) {
				a := &Account{Platform: platform, Extra: map[string]any{"kiro_file_tool_chunking": flag}}
				require.Equal(t, platform == PlatformKiro && flag == true, a.IsKiroFileToolChunkingEnabled())
			})
		}
	}
	require.False(t, (&Account{Platform: PlatformKiro}).IsKiroFileToolChunkingEnabled())
}

func TestKiroAutoModeTranscriptTrimAccountOptIn(t *testing.T) {
	require.False(t, (*Account)(nil).IsKiroAutoModeTranscriptTrimEnabled())
	for _, platform := range []string{PlatformKiro, PlatformAnthropic} {
		for _, flag := range []any{nil, false, true, "true", 1} {
			a := &Account{Platform: platform, Extra: map[string]any{"kiro_auto_mode_transcript_trim": flag}}
			require.Equal(t, platform == PlatformKiro && flag == true, a.IsKiroAutoModeTranscriptTrimEnabled())
		}
	}
	require.False(t, (&Account{Platform: PlatformKiro}).IsKiroAutoModeTranscriptTrimEnabled())
}

func TestUS070_FileToolChunkingForward(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, flag := range []any{nil, false, true, "true"} {
			for _, tool := range []string{"Write", "Edit", "MultiEdit", "Read"} {
				t.Run(fmt.Sprintf("%t/%v/%s", stream, flag, tool), func(t *testing.T) {
					input := map[string]any{"file_path": "corpus.txt", "content": "中文🙂\nnext"}
					req := newKiroToolRequestForTest(stream, true, tool)
					var clientRequest kiro.ClaudeRequest
					require.NoError(t, json.Unmarshal(req.Body.Bytes(), &clientRequest))
					// Use the name actually advertised upstream (MultiEdit becomes multiEdit).
					advertised := kiro.ClaudeToKiro(&clientRequest, false).ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools[0].ToolSpecification.Name
					upstream := &kiroSequenceUpstream{bodies: [][]byte{kiroToolUseStream("file-1", advertised, input)}}
					svc := NewKiroGatewayService(upstream, nil, nil)
					a := newKiroAccountForTest()
					a.Extra = map[string]any{"kiro_file_tool_chunking": flag}
					original := append([]byte(nil), req.Body.Bytes()...)
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					result, err := svc.Forward(context.Background(), c, a, req, time.Now())
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, 1, upstream.calls)
					require.Equal(t, original, req.Body.Bytes())
					expected := 0
					if flag == true && tool != "Read" {
						expected = 1
					}
					require.Equal(t, expected, strings.Count(string(upstream.requests[0]), "6000 UTF-8 bytes"))
					var actual kiro.ClaudeContentBlock
					if !stream {
						var response kiro.ClaudeResponse
						require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
						require.Len(t, response.Content, 1)
						require.Equal(t, "tool_use", response.StopReason)
						actual = response.Content[0]
					} else {
						var args string
						for _, line := range strings.Split(rec.Body.String(), "\n") {
							if !strings.HasPrefix(line, "data: ") {
								continue
							}
							var event struct {
								Type  string                  `json:"type"`
								Block kiro.ClaudeContentBlock `json:"content_block"`
								Delta struct {
									PartialJSON string `json:"partial_json"`
								} `json:"delta"`
							}
							require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
							if event.Type == "content_block_start" {
								actual = event.Block
							}
							if event.Type == "content_block_delta" {
								args += event.Delta.PartialJSON
							}
						}
						require.NoError(t, json.Unmarshal([]byte(args), &actual.Input))
						require.Contains(t, rec.Body.String(), `"stop_reason":"tool_use"`)
						require.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_stop"))
					}
					require.Equal(t, "file-1", actual.ID)
					require.Equal(t, tool, actual.Name)
					require.Equal(t, input, actual.Input)
				})
			}
		}
	}
}

func TestUS070_FileToolChunkingDoesNotContinueOrReplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			a := newKiroAccountForTest()
			a.Extra = map[string]any{"kiro_file_tool_chunking": true}
			upstream := &kiroSequenceUpstream{bodies: [][]byte{kiroTextStopStream("Waiting for your instructions.", "END_TURN"), kiroToolUseStream("unauthorized", "write", map[string]any{"content": "wrong"})}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			_, err := NewKiroGatewayService(upstream, nil, nil).Forward(context.Background(), c, a, newKiroToolRequestForTest(stream, true, "Write"), time.Now())
			require.NoError(t, err)
			require.Equal(t, 1, upstream.calls)
			require.Contains(t, rec.Body.String(), `"stop_reason":"end_turn"`)
			require.NotContains(t, rec.Body.String(), "unauthorized")
		})
	}
	a := newKiroAccountForTest()
	a.Extra = map[string]any{"kiro_file_tool_chunking": true}
	upstream := &kiroSequenceUpstream{bodies: [][]byte{buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"Starting the file."}`)), kiroTextStopStream("Wrong retry", "END_TURN")}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	result, err := NewKiroGatewayService(upstream, nil, nil).Forward(context.Background(), c, a, newKiroToolRequestForTest(true, true, "Write"), time.Now())
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, 1, upstream.calls)
	require.Contains(t, rec.Body.String(), "event: error")
	require.NotContains(t, rec.Body.String(), "event: message_stop")
	require.NotContains(t, rec.Body.String(), "Wrong retry")
}

func TestUS070_FileToolChunkingAdminPatch(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		a := newKiroAccountForTest()
		a.Type = AccountTypeOAuth
		a.Extra = map[string]any{"mixed_scheduling": true, "kiro_file_tool_chunking": !enabled}
		repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{a}}
		svc := &adminServiceImpl{accountRepo: repo}
		result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{a.ID}, Extra: map[string]any{"kiro_file_tool_chunking": enabled}})
		require.NoError(t, err)
		require.Equal(t, []int64{a.ID}, result.SuccessIDs)
		require.Equal(t, map[string]any{"kiro_file_tool_chunking": enabled}, repo.lastBulkUpdate.Extra)
		require.Nil(t, repo.lastBulkUpdate.Priority)
		require.Nil(t, repo.lastBulkUpdate.Credentials)
		require.Equal(t, []int64{a.ID}, repo.bulkUpdateIDs)
	}
}
