package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/trajectory"
	"github.com/klauspost/compress/zstd"
)

func (s *OpsService) persistPreparedErrorFallback(ctx context.Context, entry *OpsInsertErrorLogInput, reason string) error {
	if s == nil || entry == nil {
		return nil
	}
	dir := opsErrorFallbackDLQDir()
	allowed, err := prepareOpsDLQSpill(dir, time.Now().UTC())
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	payload, requestID, err := buildOpsErrorFallbackPayload(entry, reason)
	if err != nil {
		return err
	}
	writer := trajectory.NewWriter(nil, dir)
	key := trajectory.BlobKey(entry.CreatedAt.Year(), int(entry.CreatedAt.Month()), entry.CreatedAt.Day(), requestID)
	_, err = writer.Write(ctx, key, payload, requestID, nil)
	if err != nil {
		return err
	}
	_, err = pruneOpsDLQDir(dir, time.Now().UTC(), loadOpsDLQSpillLimits())
	return err
}

func buildOpsErrorFallbackPayload(entry *OpsInsertErrorLogInput, reason string) ([]byte, string, error) {
	if entry == nil {
		return nil, "", nil
	}
	requestID := strings.TrimSpace(entry.RequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(entry.ClientRequestID)
	}
	if requestID == "" {
		requestID = strings.TrimSpace(entry.TrajectoryID)
	}
	if requestID == "" {
		requestID = fmt.Sprintf("ops-%d", time.Now().UTC().UnixNano())
	}
	payload := map[string]any{
		"kind":         "ops_error_fallback",
		"fallback_at":  time.Now().UTC().Format(time.RFC3339Nano),
		"fallback_for": strings.TrimSpace(reason),
		"entry":        entry,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		return nil, "", err
	}
	compressed := enc.EncodeAll(raw, make([]byte, 0, len(raw)))
	if err := enc.Close(); err != nil {
		return nil, "", err
	}
	return compressed, requestID, nil
}

func opsErrorFallbackDLQDir() string {
	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "/app/data"
	}
	return filepath.Join(dataDir, "ops_dlq")
}
