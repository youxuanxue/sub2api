package trajectory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type BlobStore interface {
	Put(ctx context.Context, key string, body []byte, contentType string) (string, error)
}

type Writer struct {
	store  BlobStore
	dlqDir string
}

func NewWriter(store BlobStore, dlqDir string) *Writer {
	return &Writer{store: store, dlqDir: strings.TrimSpace(dlqDir)}
}

func (w *Writer) Write(ctx context.Context, key string, payload []byte, requestID string) (string, error) {
	if w == nil {
		return "", fmt.Errorf("trajectory writer is not configured")
	}
	if w.store != nil {
		blobURI, err := w.store.Put(ctx, key, payload, "application/zstd")
		if err == nil {
			return blobURI, nil
		}
		if strings.TrimSpace(w.dlqDir) == "" {
			return "", err
		}
	}
	if strings.TrimSpace(w.dlqDir) == "" {
		return "", fmt.Errorf("trajectory writer is not configured")
	}
	if dlqErr := os.MkdirAll(w.dlqDir, 0o755); dlqErr != nil {
		return "", dlqErr
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = "unknown"
	}
	dlqPath := filepath.Join(w.dlqDir, requestID+".json.zst")
	if writeErr := os.WriteFile(dlqPath, payload, 0o644); writeErr != nil {
		return "", writeErr
	}
	RecordDLQWrite()
	return "dlq://" + dlqPath, nil
}
