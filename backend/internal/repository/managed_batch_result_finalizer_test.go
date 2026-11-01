//go:build unit

package repository

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/batchcontract"
	"github.com/stretchr/testify/require"
)

func TestEncodeBatchResultsSplitsSuccessAndErrorsDeterministically(t *testing.T) {
	items := []ManagedBatchItem{
		{ID: 2, CustomID: "failed", Status: batchcontract.ItemStatusFailed, Error: map[string]any{"code": "bad_request", "message": "invalid"}},
		{ID: 1, CustomID: "ok", Status: batchcontract.ItemStatusSucceeded, Response: map[string]any{"status_code": float64(200), "request_id": "req-1", "body": map[string]any{"id": "resp-1"}}},
	}
	output, failed, err := encodeBatchResults(items)
	require.NoError(t, err)
	require.Equal(t, `{"id":"1","custom_id":"ok","response":{"status_code":200,"request_id":"req-1","body":{"id":"resp-1"}}}`+"\n", string(output))
	require.Equal(t, `{"id":"2","custom_id":"failed","error":{"code":"bad_request","message":"invalid","param":null,"line":null}}`+"\n", string(failed))
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		var decoded batchcontract.ResultLine
		require.NoError(t, json.Unmarshal([]byte(line), &decoded))
	}
}

func TestEncodeBatchResultsRejectsMissingSuccessBody(t *testing.T) {
	_, _, err := encodeBatchResults([]ManagedBatchItem{{ID: 1, CustomID: "broken", Status: batchcontract.ItemStatusSucceeded, Response: map[string]any{}}})
	require.Error(t, err)
}
