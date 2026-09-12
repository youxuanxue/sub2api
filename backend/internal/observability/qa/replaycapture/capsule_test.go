package replaycapture

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	require.NoError(t, err)
	raw, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	return key, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw})
}
func testRequest() Request {
	return Request{RequestID: "real-request", UserID: 1, APIKeyID: 2, Model: "m", Endpoint: "/v1/messages", Method: "POST", Path: "/v1/messages", Body: []byte(`{"model":"m","messages":[{"role":"user","content":"private business bytes"}],"api_key":"secret-in-business-body"}`), CapturedAt: time.Now().UTC()}
}
func TestCapsuleAuthenticatesOriginalBytesAndExpiry(t *testing.T) {
	key, public := testKey(t)
	_, err := PublicKey(public)
	require.NoError(t, err)
	req := testRequest()
	raw, err := Encrypt(&key.PublicKey, req)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private business bytes")
	require.NotContains(t, string(raw), "secret-in-business-body")
	got, err := Decrypt(key, raw, time.Now())
	require.NoError(t, err)
	require.Equal(t, req, got)
	var e Envelope
	require.NoError(t, json.Unmarshal(raw, &e))
	e.Ciphertext[0] ^= 1
	tampered, err := json.Marshal(e)
	require.NoError(t, err)
	_, err = Decrypt(key, tampered, time.Now())
	require.Error(t, err)
	require.NoError(t, json.Unmarshal(raw, &e))
	e.CreatedAt = e.CreatedAt.Add(time.Second)
	tampered, err = json.Marshal(e)
	require.NoError(t, err)
	_, err = Decrypt(key, tampered, time.Now())
	require.Error(t, err)
	_, err = Decrypt(key, raw, req.CapturedAt.Add(Retention))
	require.Error(t, err)
	_, err = Decrypt(key, raw, req.CapturedAt.Add(-2*time.Minute))
	require.Error(t, err)
	other, _ := testKey(t)
	_, err = Decrypt(other, raw, time.Now())
	require.Error(t, err)
	req.Body = make([]byte, MaxBodyBytes+1)
	_, err = Encrypt(&key.PublicKey, req)
	require.Error(t, err)
}
func TestStoreRetainsFirstSamplesAndConfinesEncryptedFiles(t *testing.T) {
	key, public := testKey(t)
	dir := filepath.Join(t.TempDir(), "capsules")
	s, err := Open(dir, public)
	require.NoError(t, err)
	now := time.Now()
	for _, id := range []string{"first", "second", "third"} {
		req := testRequest()
		req.RequestID = id
		require.NoError(t, s.Save(req, now))
	}
	require.NoError(t, s.Close())
	raw, err := ReadEnvelopes(dir)
	require.NoError(t, err)
	require.Len(t, raw, 2)
	var ids []string
	for _, b := range raw {
		req, err := Decrypt(key, b, now)
		require.NoError(t, err)
		ids = append(ids, req.RequestID)
	}
	require.ElementsMatch(t, []string{"first", "second"}, ids)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		info, err := entry.Info()
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
	// A partial encrypted write is never a published sample; restart cleans it.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".pending"), []byte("unfinished"), 0600))
	raw, err = ReadEnvelopes(dir)
	require.NoError(t, err)
	require.Len(t, raw, 2)
	s, err = Open(dir, public)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	require.NoError(t, s.Prune(now.Add(Retention+time.Minute)))
	raw, err = ReadEnvelopes(dir)
	require.NoError(t, err)
	require.Empty(t, raw)
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.WriteFile(outside, []byte("keep"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "escape.json")))
	require.Error(t, s.Save(testRequest(), now))
	_, err = ReadEnvelopes(dir)
	require.Error(t, err)
	kept, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "keep", string(kept))
}
func TestStoreBudgetsAndSlots(t *testing.T) {
	_, public := testKey(t)
	dir := filepath.Join(t.TempDir(), "capsules")
	s, err := Open(dir, public)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	for i := 0; i < 4; i++ {
		require.True(t, s.Acquire())
	}
	require.False(t, s.Acquire())
	s.Release()
	require.True(t, s.Acquire())
	for i := 0; i < 4; i++ {
		s.Release()
	}
	// Sparse file exhausts the byte budget without allocating a large plaintext buffer.
	f, err := os.OpenFile(filepath.Join(dir, "budget.json"), os.O_CREATE|os.O_WRONLY, 0600)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(MaxStoreBytes))
	require.NoError(t, f.Close())
	require.ErrorContains(t, s.Save(testRequest(), time.Now()), "budget")
	_, err = ReadEnvelopes(dir)
	require.Error(t, err)
}
func TestResponseObserverRejectsLateErrorsAndIncompleteFrames(t *testing.T) {
	tests := []struct {
		name, body, ctype string
		ok                bool
	}{
		{"chat", "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", "text/event-stream", true},
		{"anthropic", "data: {\"type\":\"message_stop\"}\n\n", "text/event-stream", true},
		{"late_error", strings.Repeat(": keepalive\n\n", 30000) + "data: {\"type\":\"response.completed\"}\n\nevent:error\n\n", "text/event-stream", false},
		{"no_dispatch", "data: [DONE]\n", "text/event-stream", false},
		{"multiline", "data: {\n data: ignored\n", "text/event-stream", false},
		{"multiline_valid", "data: {\n data-wrong: ignored\ndata: \"type\":\"message_stop\"}\n\n", "text/event-stream", true},
		{"partial", "data: [DONE]\n\ndata: {", "text/event-stream", false},
		{"oversize", "data: " + strings.Repeat("x", MaxBodyBytes), "text/event-stream", false},
		{"nested_error", "data: {\"type\":\"response.completed\",\"response\":{\"error\":{\"code\":\"bad\"}}}\n\n", "text/event-stream", false},
		{"json", `{"content":[{"text":"ok"}]}`, "application/json", true},
		{"json_error", `{"error":{"code":"bad"}}`, "application/json", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var o ResponseObserver
			body := []byte(tc.body)
			for len(body) > 0 {
				n := min(len(body), 31)
				o.Write(tc.ctype, body[:n])
				body = body[n:]
			}
			require.Equal(t, tc.ok, o.Successful(200))
			require.False(t, o.Successful(500))
		})
	}
	var o ResponseObserver
	o.Write("application/json", bytes.Repeat([]byte("x"), MaxBodyBytes+1))
	require.False(t, o.Successful(200))
}

func TestDisabledCaptureStillPrunesWithoutPublicKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "capsules")
	require.NoError(t, PruneDirectory(dir, time.Now()))
	require.NoError(t, os.Mkdir(dir, 0700))
	file := filepath.Join(dir, "expired.json")
	require.NoError(t, os.WriteFile(file, []byte("ciphertext"), 0600))
	now := time.Now().Add(Retention + time.Minute)
	require.NoError(t, PruneDirectory(dir, now))
	_, err := os.Stat(file)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSharedColorStoresSerializePublication(t *testing.T) {
	key, public := testKey(t)
	dir := filepath.Join(t.TempDir(), "shared")
	blue, err := Open(dir, public)
	require.NoError(t, err)
	green, err := Open(dir, public)
	require.NoError(t, err)
	var wg sync.WaitGroup
	failures := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := testRequest()
			req.RequestID = fmt.Sprint(i)
			store := blue
			if i%2 == 0 {
				store = green
			}
			failures <- store.Save(req, time.Now())
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.NoError(t, blue.Close())
	require.NoError(t, green.Close())
	envelopes, err := ReadEnvelopes(dir)
	require.NoError(t, err)
	require.Len(t, envelopes, PerCombination)
	for _, raw := range envelopes {
		_, err := Decrypt(key, raw, time.Now())
		require.NoError(t, err)
	}
	require.False(t, blue.Enqueue(testRequest()))
	require.NoError(t, blue.Close())
}
