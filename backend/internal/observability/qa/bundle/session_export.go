package bundle

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/Wei-Shaw/sub2api/internal/observability/trajectory"
)

// ExportVersion participates in ZIP identity, so an immutable old export is
// never reused as an artifact containing the current session projection.
const ExportVersion = trajectory.SessionSchemaVersion

type sessionExportManifest struct {
	SchemaVersion string   `json:"schema_version"`
	SessionSchema string   `json:"session_schema"`
	Source        Manifest `json:"source"`
	EvidenceFile  string   `json:"evidence_file"`
	SessionsFile  string   `json:"sessions_file"`
	RecordCount   int      `json:"record_count"`
	SessionCount  int      `json:"session_count"`
}

func sessionInput(record Record) trajectory.SessionInput {
	metadata := map[string]any{
		"captured_at": record.CapturedAt, "platform": record.Platform,
		"inbound_endpoint": record.InboundEndpoint, "requested_model": record.RequestedModel,
		"upstream_model": record.UpstreamModel, "status_code": record.StatusCode, "success": record.Success,
		"input_tokens": record.InputTokens, "output_tokens": record.OutputTokens, "cached_tokens": record.CachedTokens,
		"duration_ms": record.DurationMS, "capture_status": record.CaptureStatus,
		"redaction_version": record.RedactionVersion, "trajectory_id": record.TrajectoryID,
	}
	input := trajectory.SessionInput{RequestID: record.RequestID, UserID: record.UserID, APIKeyID: record.APIKeyID,
		Platform: record.Platform, Endpoint: record.InboundEndpoint, Evidence: record.Detail["evidence"], Metadata: metadata}
	if record.SynthSessionID != nil {
		input.StableSessionID = *record.SynthSessionID
	}
	return input
}

func newBundleSessionExporter(dir string) (*trajectory.SessionExporter, error) {
	return trajectory.NewSessionExporter(filepath.Join(dir, "sessions"))
}

func writeSessionExport(ctx context.Context, writer *zip.Writer, projector *trajectory.SessionExporter, manifest Manifest) error {
	sessions, err := writer.Create("sessions.jsonl")
	if err != nil {
		return err
	}
	count, err := projector.WriteTo(ctx, sessions)
	if err != nil {
		return fmt.Errorf("write QA sessions: %w", err)
	}
	output, err := writer.Create("export-manifest.json")
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(sessionExportManifest{
		SchemaVersion: "qa-session-export/v1", SessionSchema: ExportVersion, Source: manifest,
		EvidenceFile: "qa-records.jsonl", SessionsFile: "sessions.jsonl", RecordCount: manifest.RecordCount, SessionCount: count,
	})
}
