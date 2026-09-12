package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/replaycapture"
	"github.com/stretchr/testify/require"
)

func TestKeygenAndOfflineDecrypt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	var output bytes.Buffer
	require.NoError(t, run([]string{"keygen", "--dir", dir}, &output))
	require.NotContains(t, output.String(), "PRIVATE KEY")
	require.Error(t, run([]string{"keygen", "--dir", dir}, &output))
	public, err := os.ReadFile(filepath.Join(dir, "public.pem"))
	require.NoError(t, err)
	capsules := filepath.Join(t.TempDir(), "capsules")
	s, err := replaycapture.Open(capsules, public)
	require.NoError(t, err)
	body := []byte(`{"private":"unchanged"}`)
	require.NoError(t, s.Save(replaycapture.Request{RequestID: "r", UserID: 1, APIKeyID: 2, Model: "", Endpoint: "/v1/messages", Method: "POST", Path: "/v1/messages", Body: body, CapturedAt: time.Now().UTC()}, time.Now()))
	require.NoError(t, s.Close())
	output.Reset()
	args := []string{"decrypt", "--dir", capsules, "--key", filepath.Join(dir, "private.pem")}
	require.NoError(t, run(args, &output))
	var result struct {
		Request replaycapture.Request `json:"request"`
		Hash    string                `json:"envelope_sha256"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.Equal(t, body, result.Request.Body)
	require.Len(t, result.Hash, 64)
	// Exercise the actual offline output through the Python executor, without a
	// substitute crypto implementation or plaintext fixture on disk.
	command := exec.Command("python3", "-c", `import json,sys
sys.path.insert(0, "../../../ops/stage0")
import prod_replay
item=json.load(sys.stdin)
sample=prod_replay.sample_from_capsule(item["request"],item)
assert json.loads(sample["body"]) == {"private":"unchanged"}
assert sample["source"] == "encrypted"
`)
	command.Stdin = bytes.NewReader(output.Bytes())
	require.NoError(t, command.Run())
	require.NoError(t, os.Chmod(filepath.Join(dir, "private.pem"), 0644))
	output.Reset()
	require.Error(t, run(args, &output))
	require.Empty(t, output.Bytes())
}
