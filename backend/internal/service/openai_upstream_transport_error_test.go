//go:build unit

package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/integration/cursor"
	"github.com/stretchr/testify/require"
)

// TestClassifyUpstreamTransportError pins which transport-level upstream failures
// are "persistent" (retrying the same proxy/account is pointless — evict + alert)
// versus "transient" (a blip — fail over to a healthy account but do not evict).
//
// The motivating incident: a SOCKS5 proxy whose credentials expired returned
// `username/password authentication failed`, yet the account kept being scheduled.
func TestClassifyUpstreamTransportError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		persistent bool
	}{
		// Durable — config/credential/routing problems. Retrying same proxy won't help.
		{"socks5 proxy credential rejected", errors.New(`Post "https://chatgpt.com/backend-api/codex/responses": socks connect tcp 85.255.176.68:12324->chatgpt.com:443: username/password authentication failed`), true},
		{"proxy connection refused", errors.New(`proxyconnect tcp: dial tcp 1.2.3.4:1080: connect: connection refused`), true},
		{"no route to host", errors.New(`dial tcp 1.2.3.4:443: connect: no route to host`), true},
		{"dns resolution failure", errors.New(`dial tcp: lookup proxy.example.com: no such host`), true},
		{"network unreachable", errors.New(`dial tcp 1.2.3.4:443: connect: network is unreachable`), true},

		// Transient — a temporary blip. Fail over, but do NOT evict the account.
		{"client timeout", errors.New(`Post "https://chatgpt.com/...": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`), false},
		{"i/o timeout", errors.New(`dial tcp 1.2.3.4:443: i/o timeout`), false},
		{"connection reset by peer", errors.New(`read tcp 10.0.0.1:5->2.2.2.2:443: read: connection reset by peer`), false},
		{"unexpected eof", errors.New(`unexpected EOF`), false},
		{"broken pipe", errors.New(`write tcp 10.0.0.1:5->2.2.2.2:443: write: broken pipe`), false},

		{"nil error", nil, false},

		// ── Typed-error cases ──────────────────────────────────────────────
		// ECONNREFUSED wrapped in the canonical net.OpError shape Go produces.
		{
			"ECONNREFUSED via net.OpError",
			&net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
			},
			true,
		},
		// Bare syscall error (errors.Is traverses the chain).
		{"ECONNREFUSED bare", syscall.ECONNREFUSED, true},
		{"EHOSTUNREACH bare", syscall.EHOSTUNREACH, true},
		{"ENETUNREACH bare", syscall.ENETUNREACH, true},

		// *net.DNSError with IsNotFound — permanent DNS lookup failure.
		{
			"DNS not found (IsNotFound=true)",
			&net.DNSError{Err: "no such host", Name: "proxy.example.com", IsNotFound: true},
			true,
		},
		// *net.DNSError with IsNotFound=false — transient DNS timeout (not persistent).
		{
			"DNS timeout (IsNotFound=false)",
			&net.DNSError{Err: "i/o timeout", Name: "proxy.example.com", IsTimeout: true},
			false,
		},

		// context.Canceled — client gone; NOT classified as persistent.
		{"context.Canceled", context.Canceled, false},
		// context.DeadlineExceeded — slow upstream; NOT persistent.
		{"context.DeadlineExceeded", context.DeadlineExceeded, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyUpstreamTransportError(tc.err).Persistent
			if got != tc.persistent {
				t.Fatalf("classifyUpstreamTransportError(%q).Persistent = %v, want %v", errString(tc.err), got, tc.persistent)
			}
		})
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func TestClassifyCursorTransportErrorPreservesProxyFailure(t *testing.T) {
	for _, tc := range []struct {
		message    string
		persistent bool
	}{
		{"username/password authentication failed", true},
		{"proxy authentication required", true},
		{"connection reset by peer", false},
	} {
		t.Run(tc.message, func(t *testing.T) {
			resp, err := cursor.Messages(t.Context(), "private-token", []byte(`{"model":"composer-2.5","messages":[{"role":"user","content":"hello"}]}`), nil, "composer-2.5", func(*http.Request) (*http.Response, error) {
				return nil, errors.New(tc.message)
			})
			require.Nil(t, resp)
			require.Error(t, err)
			require.NotContains(t, err.Error(), tc.message, "public error remains sanitized")
			require.Equal(t, tc.persistent, classifyUpstreamTransportError(err).Persistent)
		})
	}
}
