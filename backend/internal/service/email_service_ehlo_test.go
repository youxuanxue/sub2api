//go:build unit

package service

import (
	"bufio"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestEHLOHostFromConfig validates the EHLO host derivation order:
// From-domain > Username-domain > Host. We must never default to "localhost"
// because Google Workspace SMTP relay drops AUTH on EHLO localhost (see
// ehloHostFromConfig docstring + US-016 root cause analysis).
func TestEHLOHostFromConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  *SMTPConfig
		want string
	}{
		{
			name: "from-address-wins",
			cfg:  &SMTPConfig{From: "noreply@orbitlogic.dev", Username: "ignored@example.com", Host: "smtp-relay.gmail.com"},
			want: "orbitlogic.dev",
		},
		{
			name: "fallback-to-username-domain",
			cfg:  &SMTPConfig{From: "", Username: "admin@orbitlogic.dev", Host: "smtp-relay.gmail.com"},
			want: "orbitlogic.dev",
		},
		{
			name: "fallback-to-username-when-from-malformed",
			cfg:  &SMTPConfig{From: "not-an-email", Username: "admin@orbitlogic.dev", Host: "smtp-relay.gmail.com"},
			want: "orbitlogic.dev",
		},
		{
			name: "fallback-to-host-when-no-email",
			cfg:  &SMTPConfig{From: "", Username: "", Host: "smtp-relay.gmail.com"},
			want: "smtp-relay.gmail.com",
		},
		{
			name: "trailing-at-treated-as-malformed",
			cfg:  &SMTPConfig{From: "user@", Username: "", Host: "smtp.example.com"},
			want: "smtp.example.com",
		},
		{
			name: "leading-at-treated-as-malformed",
			cfg:  &SMTPConfig{From: "@example.com", Username: "", Host: "smtp.example.com"},
			// "@example.com" has @ at index 0, domain part is "example.com" (non-empty) → wins.
			want: "example.com",
		},
		{
			name: "host-localhost-rejected-uses-marker",
			cfg:  &SMTPConfig{From: "", Username: "", Host: "localhost"},
			want: "tokenkey.invalid",
		},
		{
			name: "trims-whitespace-around-from",
			cfg:  &SMTPConfig{From: "  noreply@orbitlogic.dev  ", Host: "smtp.example.com"},
			want: "orbitlogic.dev",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ehloHostFromConfig(tc.cfg)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestSendEmail_RejectsEHLOLocalhost is the regression test for the
// `smtp auth: EOF` bug against Google Workspace SMTP relay. It runs a tiny
// TCP server that mimics Google's anti-abuse behavior:
//   - greeting (220 OK)
//   - if first command is "EHLO localhost" → close TCP connection (no 5xx)
//   - if first command is "EHLO <real-domain>" → respond 250 OK + accept
//     subsequent AUTH/MAIL/RCPT/DATA/QUIT minimally
//
// Before the fix, sendMailPlain would EHLO with "localhost" (Go stdlib
// default) and the server would drop the connection during AUTH, surfacing as
// `smtp auth: EOF`. After the fix, sendMailPlain calls client.Hello() with the
// derived domain ("orbitlogic.dev") before AUTH, so the mock server accepts
// the session and the test returns nil.
func TestSendEmail_RejectsEHLOLocalhost(t *testing.T) {
	srv := newPickyEHLOServer(t)
	defer srv.Close()

	svc := &EmailService{}
	cfg := &SMTPConfig{
		Host:     srv.Host(),
		Port:     srv.Port(),
		Username: "admin@orbitlogic.dev",
		Password: "fake-app-password-for-mock",
		From:     "noreply@orbitlogic.dev",
		FromName: "TokenKey",
		UseTLS:   false, // mock is plaintext; we still exercise the EHLO path.
	}

	err := svc.SendEmailWithConfig(cfg, "user@example.com", "subject", "<p>body</p>")
	require.NoError(t, err, "post-fix: explicit EHLO with From-domain must succeed against picky mock server")

	require.Equal(t, "orbitlogic.dev", srv.LastEHLOHost(), "must EHLO with the From address domain, not 'localhost'")
}

// TestSendEmail_PreFixBehaviorReproduces confirms that bypassing the fix
// (sending the raw stdlib default EHLO "localhost") still triggers the
// connection drop, proving the mock server faithfully reproduces the
// Google Workspace SMTP relay behavior we observed in production.
//
// This is a guard-rail test: if someone "simplifies" the mock and removes the
// localhost-rejection branch, this test will start failing — flagging that the
// regression suite no longer protects against the original bug.
func TestSendEmail_PreFixBehaviorReproduces(t *testing.T) {
	srv := newPickyEHLOServer(t)
	defer srv.Close()

	conn, err := net.DialTimeout("tcp", srv.Addr(), 2*time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	rd := bufio.NewReader(conn)
	greeting, err := rd.ReadString('\n')
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(greeting, "220"), "expected 220 greeting, got %q", greeting)

	// Send the offending stdlib-default EHLO that triggered the prod bug.
	_, err = conn.Write([]byte("EHLO localhost\r\n"))
	require.NoError(t, err)

	// Server should drop connection: subsequent read returns io.EOF (or wrapped).
	_, err = rd.ReadString('\n')
	require.Error(t, err, "mock server must drop connection on EHLO localhost (mirrors smtp-relay.gmail.com behavior)")
	require.Equal(t, "localhost", srv.LastEHLOHost())
}

// pickyEHLOServer is a minimal TCP SMTP-ish server that drops on EHLO
// localhost and otherwise speaks just enough SMTP to let net/smtp.Client
// complete a Plain-auth send-mail flow.
type pickyEHLOServer struct {
	t        *testing.T
	listener net.Listener
	mu       sync.Mutex
	lastEHLO string
	wg       sync.WaitGroup
}

func newPickyEHLOServer(t *testing.T) *pickyEHLOServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &pickyEHLOServer{t: t, listener: ln}
	srv.wg.Add(1)
	go srv.acceptLoop()
	return srv
}

func (s *pickyEHLOServer) Addr() string { return s.listener.Addr().String() }

func (s *pickyEHLOServer) Host() string {
	host, _, _ := net.SplitHostPort(s.Addr())
	return host
}

func (s *pickyEHLOServer) Port() int {
	_, port, _ := net.SplitHostPort(s.Addr())
	var p int
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	return p
}

func (s *pickyEHLOServer) LastEHLOHost() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastEHLO
}

func (s *pickyEHLOServer) Close() {
	_ = s.listener.Close()
	s.wg.Wait()
}

func (s *pickyEHLOServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.handle(conn)
	}
}

func (s *pickyEHLOServer) handle(conn net.Conn) {
	defer s.wg.Done()
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	rd := bufio.NewReader(conn)
	if _, err := conn.Write([]byte("220 mock.example.com ESMTP picky-mock\r\n")); err != nil {
		return
	}

	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd, arg := splitCmd(line)

		switch strings.ToUpper(cmd) {
		case "EHLO", "HELO":
			s.mu.Lock()
			s.lastEHLO = arg
			s.mu.Unlock()
			if strings.EqualFold(arg, "localhost") {
				// Mirror Google Workspace SMTP relay: drop connection silently.
				return
			}
			// Multi-line 250 response advertising AUTH PLAIN.
			_, _ = conn.Write([]byte("250-mock.example.com Hello " + arg + "\r\n"))
			_, _ = conn.Write([]byte("250-AUTH PLAIN LOGIN\r\n"))
			_, _ = conn.Write([]byte("250 OK\r\n"))
		case "AUTH":
			_, _ = conn.Write([]byte("235 2.7.0 Accepted\r\n"))
		case "MAIL":
			_, _ = conn.Write([]byte("250 2.1.0 OK\r\n"))
		case "RCPT":
			_, _ = conn.Write([]byte("250 2.1.5 OK\r\n"))
		case "DATA":
			_, _ = conn.Write([]byte("354 End data with <CR><LF>.<CR><LF>\r\n"))
			// Drain until "\r\n.\r\n".
			if err := drainData(rd); err != nil {
				return
			}
			_, _ = conn.Write([]byte("250 2.0.0 OK queued\r\n"))
		case "QUIT":
			_, _ = conn.Write([]byte("221 2.0.0 closing\r\n"))
			return
		case "NOOP":
			_, _ = conn.Write([]byte("250 OK\r\n"))
		case "RSET":
			_, _ = conn.Write([]byte("250 OK\r\n"))
		default:
			_, _ = conn.Write([]byte("502 5.5.2 Command not implemented\r\n"))
		}
	}
}

func splitCmd(line string) (string, string) {
	idx := strings.IndexAny(line, " \t")
	if idx < 0 {
		return line, ""
	}
	return line[:idx], strings.TrimSpace(line[idx+1:])
}

func drainData(rd *bufio.Reader) error {
	var prevDot, prevCR bool
	for {
		b, err := rd.ReadByte()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		// Look for sequence "\r\n.\r\n" terminator.
		if b == '\r' {
			prevCR = true
			continue
		}
		if b == '\n' && prevCR {
			prevCR = false
			if prevDot {
				return nil
			}
			continue
		}
		if b == '.' {
			prevDot = true
			continue
		}
		prevDot = false
		prevCR = false
	}
}
