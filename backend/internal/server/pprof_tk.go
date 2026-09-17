package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// StartLoopbackPprof starts a dedicated net/http pprof server on a loopback address.
// Returns nil server when disabled. Caller must Shutdown the returned server.
func StartLoopbackPprof(rawListen string) (*http.Server, error) {
	addr, enabled, err := config.NormalizeServerPprofListen(rawListen)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// Profile endpoints intentionally block for seconds=N; keep write unbounded.
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("pprof listen %s: %w", addr, err)
	}

	go func() {
		log.Printf("pprof listening on http://%s/debug/pprof/ (loopback-only)", addr)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("pprof server stopped: %v", err)
		}
	}()
	return srv, nil
}

// ShutdownPprof gracefully stops a pprof server started by StartLoopbackPprof.
func ShutdownPprof(ctx context.Context, srv *http.Server) {
	if srv == nil {
		return
	}
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("pprof shutdown: %v", err)
	}
}
