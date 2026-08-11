package trajectory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type BlobStore interface {
	Put(ctx context.Context, key string, body []byte, contentType string) (string, error)
}

// WriteLayout selects legacy flat or hourly nested DLQ paths.
type WriteLayout struct {
	Hourly    bool
	CreatedAt time.Time
}

type Writer struct {
	store  BlobStore
	dlqDir string
}

func NewWriter(store BlobStore, dlqDir string) *Writer {
	return &Writer{store: store, dlqDir: strings.TrimSpace(dlqDir)}
}

func (w *Writer) Write(ctx context.Context, key string, payload []byte, requestID string, layout *WriteLayout) (string, error) {
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
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = "unknown"
	}
	dlqPath, err := w.dlqPath(requestID, layout)
	if err != nil {
		return "", err
	}
	if dlqErr := os.MkdirAll(filepath.Dir(dlqPath), 0o755); dlqErr != nil {
		return "", dlqErr
	}
	if writeErr := os.WriteFile(dlqPath, payload, 0o644); writeErr != nil {
		return "", writeErr
	}
	RecordDLQWrite()
	return "dlq://" + dlqPath, nil
}

func (w *Writer) dlqPath(requestID string, layout *WriteLayout) (string, error) {
	if layout != nil && layout.Hourly {
		h := layout.CreatedAt.UTC()
		rel := filepath.Join(
			fmt.Sprintf("%04d", h.Year()),
			fmt.Sprintf("%02d", int(h.Month())),
			fmt.Sprintf("%02d", h.Day()),
			fmt.Sprintf("%02d", h.Hour()),
			safeDLQFileID(requestID)+".json.zst",
		)
		return filepath.Join(w.dlqDir, rel), nil
	}
	return filepath.Join(w.dlqDir, safeDLQFileID(requestID)+".json.zst"), nil
}

func safeDLQFileID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw != "" && len(raw) <= 128 && raw != "." && raw != ".." {
		safe := true
		for _, ch := range []byte(raw) {
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
				(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' {
				continue
			}
			safe = false
			break
		}
		if safe {
			return raw
		}
	}
	digest := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("dlq-%x", digest[:16])
}
