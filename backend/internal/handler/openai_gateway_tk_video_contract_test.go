//go:build unit

package handler

import (
	"testing"

	newapimodel "github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

// Upstream-merge overwrite-protection gates (contract tests against the
// PINNED new-api, .new-api-ref). Both assumptions below are TK-owned billing
// behavior built on top of upstream types whose drift would be SILENT —
// refunds stop firing or the video-input price guard stops seeing input —
// with zero compile errors. These tests run in the standard test-unit job,
// so any .new-api-ref bump (or upstream merge) that changes the contracts
// turns the PR red instead.

// TestVideoTerminalOutcome_NewAPITaskStatusContract locks the fetch path's
// terminal/failed classification to new-api's model.TaskStatus* constants
// (TaskInfo.Status reaches the handler as string(model.TaskStatus)). If
// upstream renames or adds terminal statuses, this must be revisited together
// with videoTerminalOutcome — the refund trigger depends on it.
func TestVideoTerminalOutcome_NewAPITaskStatusContract(t *testing.T) {
	terminal, failed := videoTerminalOutcome(string(newapimodel.TaskStatusFailure))
	require.True(t, terminal, "FAILURE must be terminal (stops the client poll)")
	require.True(t, failed, "FAILURE must trigger the registry cleanup + submit-charge refund")

	terminal, failed = videoTerminalOutcome(string(newapimodel.TaskStatusSuccess))
	require.True(t, terminal, "SUCCESS must be terminal (stops the client poll)")
	// NOTE: only FAILURE deletes the registry entry now; SUCCESS is kept until
	// TTL so an aborted large-body fetch can re-fetch (see VideoFetch).
	require.False(t, failed, "SUCCESS must NOT trigger a refund or registry cleanup")

	for _, nonTerminal := range []string{
		string(newapimodel.TaskStatusNotStart),
		string(newapimodel.TaskStatusSubmitted),
		string(newapimodel.TaskStatusQueued),
		string(newapimodel.TaskStatusInProgress),
		string(newapimodel.TaskStatusUnknown),
	} {
		terminal, failed = videoTerminalOutcome(nonTerminal)
		require.False(t, terminal, "%s must keep the registry entry (client will re-poll)", nonTerminal)
		require.False(t, failed, "%s must not refund", nonTerminal)
	}
}

// TestVideoSubmitHasVideoInput_ParityWithNewAPIMetadataDecoding feeds raw
// bodies through new-api's actual TaskSubmitReq.UnmarshalJSON (the same
// decoding the doubao adaptor sees) and requires: whenever the decoded
// metadata carries a video_url content part, TK's submit guard must fire.
// This pins the guard to upstream's accepted carrier shapes — notably
// metadata sent as a JSON-ENCODED STRING, the form that bypassed the first
// guard implementation. A new upstream-accepted shape that decodes video
// input out of one of these bodies without the guard firing fails here.
func TestVideoSubmitHasVideoInput_ParityWithNewAPIMetadataDecoding(t *testing.T) {
	metadataHasVideo := func(md map[string]interface{}) bool {
		content, ok := md["content"].([]interface{})
		if !ok {
			return false
		}
		for _, part := range content {
			m, ok := part.(map[string]interface{})
			if !ok {
				continue
			}
			if m["type"] == "video_url" {
				return true
			}
			if _, hasURL := m["video_url"]; hasURL {
				return true
			}
		}
		return false
	}

	bodies := []string{
		`{"model":"m","prompt":"p","seconds":"5"}`,
		`{"model":"m","prompt":"p","metadata":{"content":[{"type":"image_url","image_url":{"url":"u"}}]}}`,
		`{"model":"m","prompt":"p","metadata":{"content":[{"type":"video_url","video_url":{"url":"v"}}]}}`,
		`{"model":"m","prompt":"p","metadata":"{\"content\":[{\"type\":\"video_url\",\"video_url\":{\"url\":\"v\"}}]}"}`,
		`{"model":"m","prompt":"p","metadata":"{\"content\":[{\"type\":\"image_url\",\"image_url\":{\"url\":\"u\"}}]}"}`,
		`{"model":"m","prompt":"p","metadata":{"content":[{"video_url":{"url":"v"}}]}}`,
	}
	for _, body := range bodies {
		var req relaycommon.TaskSubmitReq
		require.NoError(t, req.UnmarshalJSON([]byte(body)), "new-api must accept the corpus body: %s", body)
		upstreamSeesVideo := metadataHasVideo(req.Metadata)
		if upstreamSeesVideo {
			require.True(t, videoSubmitHasVideoInput([]byte(body)),
				"guard must fire for every body whose decoded metadata carries video input: %s", body)
		}
		// The guard may be STRICTER than upstream decoding (it also rejects
		// top-level "content" video parts that the JSON path would silently
		// drop) — so no assertion on the false direction beyond the image /
		// no-input bodies below.
		if !upstreamSeesVideo && videoSubmitHasVideoInput([]byte(body)) {
			t.Fatalf("guard misfired on a body upstream decodes WITHOUT video input: %s", body)
		}
	}
}
