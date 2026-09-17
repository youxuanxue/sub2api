package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStartLoopbackPprofServesIndex(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	srv, err := StartLoopbackPprof(addr)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if srv == nil {
		t.Fatal("expected server")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		ShutdownPprof(ctx, srv)
	}()

	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for {
		resp, err = http.Get("http://" + addr + "/debug/pprof/")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET pprof index: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if !strings.Contains(string(body), "types:") && !strings.Contains(string(body), "profile") {
		t.Fatalf("unexpected pprof index body: %q", string(body))
	}
}

func TestStartLoopbackPprofDisabled(t *testing.T) {
	t.Parallel()
	srv, err := StartLoopbackPprof("off")
	if err != nil {
		t.Fatal(err)
	}
	if srv != nil {
		t.Fatal("expected nil server when disabled")
	}
}

func TestStartLoopbackPprofRejectsPublicBind(t *testing.T) {
	t.Parallel()
	_, err := StartLoopbackPprof("0.0.0.0:6060")
	if err == nil {
		t.Fatal("expected error for non-loopback bind")
	}
}
