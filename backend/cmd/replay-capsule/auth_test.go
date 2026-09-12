package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthCheckKeepsCredentialsOffOutputAndRefusesRedirect(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{401, `{"code":"API_KEY_DISABLED","message":"private-upstream-text"}`, "API_KEY_DISABLED"},
		{403, `{"code":"ACCESS_DENIED","message":"private-upstream-text"}`, "unexpected"},
		{429, `{"error":{"code":"insufficient_quota"}}`, "insufficient_quota"},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer secret-key", r.Header.Get("Authorization"))
				require.Equal(t, "/v1/chat/completions", r.URL.Path)
				raw, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.JSONEq(t, `{"model":"auth-rejection-check","messages":[]}`, string(raw))
				w.Header().Set("X-Request-ID", "server-id")
				w.WriteHeader(tc.status)
				_, err = io.WriteString(w, tc.body)
				require.NoError(t, err)
			}))
			defer server.Close()
			_, port, err := net.SplitHostPort(server.Listener.Addr().String())
			require.NoError(t, err)
			n, err := strconv.Atoi(port)
			require.NoError(t, err)
			input, err := json.Marshal(map[string]any{"key": "secret-key", "body": []byte(`{"model":"auth-rejection-check","messages":[]}`), "port": n, "request_id": "auth-test"})
			require.NoError(t, err)
			var out bytes.Buffer
			require.NoError(t, authCheck(bytes.NewReader(input), &out))
			require.NotContains(t, out.String(), "secret-key")
			require.NotContains(t, out.String(), "private-upstream-text")
			var result map[string]any
			require.NoError(t, json.Unmarshal(out.Bytes(), &result))
			require.Equal(t, tc.want, result["error_code"])
		})
	}
	sinkCalls := 0
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sinkCalls++; w.WriteHeader(200) }))
	defer sink.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Location", sink.URL); w.WriteHeader(302) }))
	defer redirect.Close()
	_, port, err := net.SplitHostPort(redirect.Listener.Addr().String())
	require.NoError(t, err)
	n, err := strconv.Atoi(port)
	require.NoError(t, err)
	in, err := json.Marshal(map[string]any{"key": "secret-key", "body": []byte(`{"model":"auth-rejection-check","messages":[]}`), "port": n})
	require.NoError(t, err)
	require.Error(t, authCheck(bytes.NewReader(in), io.Discard))
	require.Zero(t, sinkCalls)
}
