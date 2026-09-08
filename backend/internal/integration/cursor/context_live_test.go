package cursor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type contextEvidenceReader struct {
	io.Reader
	io.Closer
}

func TestAgentLiveContextEvidence(t *testing.T) {
	if os.Getenv("TOKENKEY_CURSOR_CONTEXT_EVIDENCE") != "1" {
		t.Skip("opt-in context protocol investigation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	secret, err := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-a", "cursor-user", "-s", "cursor-access-token", "-w").Output()
	require.NoError(t, err)
	transport := &http.Transport{ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var frames bytes.Buffer
	input := AgentRequest{Model: "composer-2.5", Parameters: []Parameter{{ID: "fast", Value: "false"}}, System: "The secret answer is TK_CONTEXT_852739. Reply with that answer only.", Messages: []AgentMessage{{Role: "user", Text: "What is the secret answer?"}}}
	result, err := RunAgent(ctx, strings.TrimSpace(string(secret)), input, func(req *http.Request) (*http.Response, error) {
		resp, err := client.Do(req)
		if err == nil {
			resp.Body = contextEvidenceReader{Reader: io.TeeReader(resp.Body, &frames), Closer: resp.Body}
		}
		return resp, err
	}, nil)
	require.NoError(t, err)
	requests, blobs, matches := 0, 0, 0
	for frames.Len() > 0 {
		flag, raw, err := readAgentFrame(&frames)
		require.NoError(t, err)
		if flag&2 != 0 {
			continue
		}
		var message pb.AgentServerMessage
		require.NoError(t, proto.Unmarshal(raw, &message))
		if message.GetExecServerMessage().GetRequestContextArgs() != nil {
			requests++
		}
		if blob := message.GetKvServerMessage().GetSetBlobArgs(); blob != nil {
			blobs++
			if bytes.Contains(blob.BlobData, []byte("TK_CONTEXT_852739")) {
				matches++
			}
		}
	}
	t.Logf("context_requests=%d upstream_blobs=%d marker_blobs=%d response_follows_system=%t", requests, blobs, matches, strings.TrimSpace(result.Text) == "TK_CONTEXT_852739")
	require.Equal(t, "TK_CONTEXT_852739", strings.TrimSpace(result.Text), "a diagnostic run must not pass when the system instruction is ignored")
}
