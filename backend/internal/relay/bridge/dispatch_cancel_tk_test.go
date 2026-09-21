//go:build unit

package bridge

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBindChatUpstreamRequestContext_OptsInForBoundedAndNonBounded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, bounded := range []bool{false, true} {
		t.Run(map[bool]string{false: "non_bounded", true: "bounded"}[bounded], func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			parent, cancelParent := context.WithCancel(context.Background())
			defer cancelParent()
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}")).WithContext(parent)
			attempt, cancelAttempt := context.WithCancel(context.Background())
			defer cancelAttempt()

			restore := bindChatUpstreamRequestContext(c, attempt, bounded)
			require.True(t, relaycommon.UpstreamRequestContextEnabled(c.Request.Context()),
				"chat must opt into caller-owned transport cancellation")
			if bounded {
				cancelAttempt()
			} else {
				cancelParent()
			}
			require.ErrorIs(t, c.Request.Context().Err(), context.Canceled)
			restore()
			require.False(t, relaycommon.UpstreamRequestContextEnabled(c.Request.Context()))
		})
	}
}

func TestBindChatUpstreamRequestContext_NonBoundedCancelsViaNewAPIDoRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ensureNewAPIDeps()

	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()

	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}")).WithContext(parent)
	restore := bindChatUpstreamRequestContext(c, context.Background(), false)
	defer restore()

	req, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader("{}"))
	require.NoError(t, err)
	resp, err := channel.DoRequest(c, req, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}})
	require.NoError(t, err)
	defer resp.Body.Close()

	cancel()
	_, _ = io.ReadAll(resp.Body)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("non-bounded chat cancel did not stop upstream via new-api DoRequest")
	}
}
