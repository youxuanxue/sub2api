package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNode1ReceiptRequiresVerifiedHourIdentity(t *testing.T) {
	root := t.TempDir()
	hour := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(root, "receipts", "prod", hour.Format("2006/01/02/15")+".json")
	if ok, err := hasNode1Receipt(root, "prod", hour); err != nil || ok {
		t.Fatalf("missing receipt should block: ok=%v err=%v", ok, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	valid := node1Receipt{SchemaVersion: "qa-node1-hour-v1", Environment: "prod", WindowStart: hour, WindowEnd: hour.Add(time.Hour), OK: true, FileCount: 1, ManifestSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	raw, _ := json.Marshal(valid)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := hasNode1Receipt(root, "prod", hour); err != nil || !ok {
		t.Fatalf("valid receipt rejected: ok=%v err=%v", ok, err)
	}
	valid.Environment = "cn-prod"
	raw, _ = json.Marshal(valid)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := hasNode1Receipt(root, "prod", hour); err == nil || ok {
		t.Fatalf("mismatched receipt accepted: ok=%v err=%v", ok, err)
	}
}
