//go:build unit

package bridge

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestNewAPIVideoPollingRetainsPluginSubmitState(t *testing.T) {
	ensureNewAPIDeps()
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":10000,"data":{"status":"in_queue"}}`)
	}))
	defer server.Close()
	task := newAPIVideoPollTask(VideoFetchInput{UpstreamTaskID: "task-test", OriginModel: "jimeng-v30", APIKey: "sk-test", PluginState: json.RawMessage(`{"req_key":"jimeng_i2v_first_tail_v30"}`)})
	adaptor := taskAdaptorForChannel(constant.ChannelTypeJimeng, server.URL)
	require.NotNil(t, adaptor)
	response, err := adaptor.FetchTask(server.URL, "sk-test", task, "")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, "jimeng_i2v_first_tail_v30", received["req_key"])
	require.Equal(t, "task-test", received["task_id"])
}
