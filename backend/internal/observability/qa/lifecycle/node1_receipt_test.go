package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNode1ReceiptRequiresVerifiedHourIdentity(t *testing.T) {
	root := t.TempDir()
	receiptRoot := filepath.Join(root, "receipts")
	hour := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(receiptRoot, "prod", hour.Format("2006/01/02/15")+".json")
	if ok, err := hasNode1Receipt(receiptRoot, "prod", hour); err != nil || ok {
		t.Fatalf("missing receipt should block: ok=%v err=%v", ok, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "qa-raw", "prod", hour.Format("2006/01/02/15"))
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("payload")
	if err := os.WriteFile(filepath.Join(target, "part-000"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	fileDigest := sha256.Sum256(content)
	manifest := map[string]any{
		"schema_version": "qa-node1-hour-v1", "environment": "prod",
		"window_start": hour, "window_end": hour.Add(time.Hour),
		"file_count": 1, "total_bytes": len(content),
		"files": []map[string]any{{"path": "part-000", "bytes": len(content), "sha256": hex.EncodeToString(fileDigest[:])}},
	}
	manifestBytes, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(target, "manifest.json"), manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	valid := node1Receipt{SchemaVersion: "qa-node1-hour-v1", Environment: "prod", WindowStart: hour, WindowEnd: hour.Add(time.Hour), OK: true, FileCount: 1, TotalBytes: int64(len(content)), ManifestSHA256: hex.EncodeToString(manifestDigest[:])}
	raw, _ := json.Marshal(valid)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := hasNode1Receipt(receiptRoot, "prod", hour); err != nil || !ok {
		t.Fatalf("valid receipt rejected: ok=%v err=%v", ok, err)
	}
	if err := os.WriteFile(filepath.Join(target, "part-000"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := hasNode1Receipt(receiptRoot, "prod", hour); err == nil || ok {
		t.Fatalf("tampered archived file accepted: ok=%v err=%v", ok, err)
	}
	valid.Environment = "cn-prod"
	raw, _ = json.Marshal(valid)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := hasNode1Receipt(receiptRoot, "prod", hour); err == nil || ok {
		t.Fatalf("mismatched receipt accepted: ok=%v err=%v", ok, err)
	}
}
