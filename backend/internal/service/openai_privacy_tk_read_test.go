//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func TestParseOpenAITrainingDisabled(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		body         string
		wantDisabled bool
		wantOK       bool
	}{
		{name: "training_allowed false -> disabled", body: `{"training_allowed":false}`, wantDisabled: true, wantOK: true},
		{name: "training_allowed true -> not disabled", body: `{"training_allowed":true}`, wantDisabled: false, wantOK: true},
		{
			name:   "field absent (other training flags only) -> inconclusive",
			body:   `{"codex_training_allowed":false,"video_training_allowed":false}`,
			wantOK: false,
		},
		{name: "empty object -> inconclusive", body: `{}`, wantOK: false},
		{name: "not json -> inconclusive", body: `<!doctype html><html>just a moment</html>`, wantOK: false},
		{name: "empty body -> inconclusive", body: ``, wantOK: false},
		{
			name:         "full settings payload, training off",
			body:         `{"training_allowed":false,"codex_training_allowed":false,"precise_location_allowed":true}`,
			wantDisabled: true,
			wantOK:       true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			disabled, ok := parseOpenAITrainingDisabled([]byte(tc.body))
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.wantDisabled, disabled)
		})
	}
}

// TestReadOpenAITrainingDisabled exercises the HTTP read path end to end against an
// httptest server: status handling, body extraction, header wiring, and the parse
// integration. It swaps the package-level openAISettingsUserURL, so it must run
// sequentially (no t.Parallel) — the package's privacy retry tests share the var.
func TestReadOpenAITrainingDisabled(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		contentType  string
		body         string
		wantDisabled bool
		wantOK       bool
	}{
		{name: "200 training off -> disabled", status: 200, contentType: "application/json", body: `{"training_allowed":false}`, wantDisabled: true, wantOK: true},
		{name: "200 training on -> not disabled", status: 200, contentType: "application/json", body: `{"training_allowed":true}`, wantDisabled: false, wantOK: true},
		{name: "200 field absent -> inconclusive", status: 200, contentType: "application/json", body: `{"codex_training_allowed":false}`, wantOK: false},
		{name: "403 cf challenge html -> inconclusive", status: 403, contentType: "text/html", body: `<!doctype html><html>just a moment</html>`, wantOK: false},
		{name: "200 non-json body -> inconclusive", status: 200, contentType: "text/html", body: `<html>nope</html>`, wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuth, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			orig := openAISettingsUserURL
			openAISettingsUserURL = srv.URL + "/backend-api/settings/user"
			defer func() { openAISettingsUserURL = orig }()

			factory := func(proxyURL string) (*req.Client, error) { return req.C(), nil }
			disabled, ok := readOpenAITrainingDisabled(context.Background(), factory, "tok-xyz", "")

			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.wantDisabled, disabled)
			require.Equal(t, "Bearer tok-xyz", gotAuth, "access token must be sent as Bearer")
			require.Equal(t, "/backend-api/settings/user", gotPath)
		})
	}
}

// TestReadOpenAITrainingDisabled_Guards covers the early-return guards that never issue
// a request (missing token or factory) and a transport failure (factory error).
func TestReadOpenAITrainingDisabled_Guards(t *testing.T) {
	okFactory := func(string) (*req.Client, error) { return req.C(), nil }

	disabled, ok := readOpenAITrainingDisabled(context.Background(), okFactory, "", "")
	require.False(t, ok)
	require.False(t, disabled)

	disabled, ok = readOpenAITrainingDisabled(context.Background(), nil, "tok", "")
	require.False(t, ok)
	require.False(t, disabled)

	errFactory := func(string) (*req.Client, error) { return nil, io.ErrUnexpectedEOF }
	disabled, ok = readOpenAITrainingDisabled(context.Background(), errFactory, "tok", "")
	require.False(t, ok)
	require.False(t, disabled)
}
