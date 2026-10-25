//go:build unit

package bridge

import (
	"net/http"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
)

// TK never calls newapi common.InitEnv(), so ensureNewAPIDeps must seed the
// transport tuning vars itself before InitHttpClient builds the shared client.
// Assert against the executed artifact (the live transport), not the package
// vars, so a future reordering that initializes the client first fails here.
func TestEnsureNewAPIDepsSeedsTransportTuning(t *testing.T) {
	ensureNewAPIDeps()

	client := service.GetHttpClient()
	if client == nil {
		t.Fatal("expected shared new-api http client to be initialized")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if transport.MaxIdleConns != 500 {
		t.Errorf("MaxIdleConns = %d, want 500 (upstream InitEnv default)", transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != 100 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 100 (upstream InitEnv default; zero value falls back to Go default 2)", transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout != 90*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 90s (upstream InitEnv default; zero value never reaps idle conns)", transport.IdleConnTimeout)
	}
}

func TestFirstTokenMs_OnlyReportsObservedStreamResponse(t *testing.T) {
	start := time.Unix(100, 0)
	first := start.Add(1250 * time.Millisecond)

	tests := []struct {
		name   string
		info   *relaycommon.RelayInfo
		stream bool
		want   *int
	}{
		{name: "nil relay", info: nil, stream: true},
		{name: "non stream", info: &relaycommon.RelayInfo{StartTime: start, FirstResponseTime: first}, stream: false},
		{name: "no response", info: &relaycommon.RelayInfo{StartTime: start}, stream: true},
		{name: "observed response", info: &relaycommon.RelayInfo{StartTime: start, FirstResponseTime: first}, stream: true, want: func() *int { v := 1250; return &v }()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstTokenMs(tt.info, tt.stream)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("firstTokenMs() = %d, want nil", *got)
				}
				return
			}
			if got == nil || *got != *tt.want {
				t.Fatalf("firstTokenMs() = %v, want %d", got, *tt.want)
			}
		})
	}
}
