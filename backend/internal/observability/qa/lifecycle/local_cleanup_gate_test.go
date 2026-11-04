package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Holding an hour back is the gate doing its job, not a service fault. On
// 2026-11-03 prod alerted "service failed: exit-code" while the gate was
// correctly protecting 12,468 files whose receipts claimed an archive that was
// never on node1 -- written by an earlier local-copy run. The alert was noise
// that would mask a real failure.
func TestReceiptGateReportsBlockedHoursWithoutFailing(t *testing.T) {
	source := readSource(t)
	gate := sliceBetween(t, source, `if opts.ArchiveReceiptRoot != ""`, "if dryRun {\n\t\treturn result, nil")

	if strings.Contains(gate, "return result, receiptErr") {
		t.Error("a blocked hour must not surface as an error: the unit exits non-zero and the host job watcher alerts")
	}
	if strings.Contains(gate, "gate blocked cleanup (%d hours)") {
		t.Error("the blocked-hour error wrapper is still returned")
	}
	if !strings.Contains(gate, "return result, nil") {
		t.Error("the gate must return a result, not an error")
	}
	if !strings.Contains(gate, "result.SelectedHours = nil") {
		t.Error("a partly unprovable batch must drop nothing at all")
	}
	if !strings.Contains(gate, "ReceiptBlockedReasons") {
		t.Error("the reason must reach the receipt so an operator can tell the two cases apart")
	}
}

// The reasons field is what distinguishes "not archived yet" from "a receipt
// lies about node1", which is the difference between waiting and investigating.
func TestBlockedReasonsAreCarriedInTheResult(t *testing.T) {
	source := readSource(t)
	if !strings.Contains(source, `json:"receipt_blocked_reasons,omitempty"`) {
		t.Fatal("LocalCleanupResult must expose receipt_blocked_reasons")
	}
}

// Everything that is genuinely broken must still fail loudly.
func TestRealFailuresStillPropagate(t *testing.T) {
	source := readSource(t)
	tail := source[strings.Index(source, "control := NewSQLControlAdapter"):]
	if !strings.Contains(tail, "joined") {
		t.Error("per-hour expiry errors must still be joined and returned")
	}
	for _, fatal := range []string{"return result, err"} {
		if !strings.Contains(source, fatal) {
			t.Errorf("expected genuine failures to return an error (%q missing)", fatal)
		}
	}
}

// A receipt that cannot be parsed, or whose manifest is absent, must block --
// never be treated as permission to delete.
func TestUnreadableReceiptBlocks(t *testing.T) {
	root := t.TempDir()
	receiptRoot := filepath.Join(root, "receipts")
	hour := time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC)
	path := filepath.Join(receiptRoot, "prod", hour.Format("2006/01/02/15")+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err := hasNode1Receipt(receiptRoot, "prod", hour)
	if ok {
		t.Fatal("an unparseable receipt must not authorise deletion")
	}
	if err == nil {
		t.Fatal("an unparseable receipt must be reported, not silently treated as absent")
	}
}

// This is the live case: the receipt verifies on its own, but node1 holds no
// manifest for that hour, so the archive it claims does not exist.
func TestReceiptWithoutNode1ManifestBlocks(t *testing.T) {
	root := t.TempDir()
	receiptRoot := filepath.Join(root, "receipts")
	hour := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	path := filepath.Join(receiptRoot, "prod", hour.Format("2006/01/02/15")+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"schema_version":"qa-node1-hour-v1","environment":"prod",` +
		`"window_start":"2026-11-02T08:00:00Z","window_end":"2026-11-02T09:00:00Z",` +
		`"ok":true,"file_count":5904,"total_bytes":1,` +
		`"manifest_sha256":"` + strings.Repeat("a", 64) + `"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err := hasNode1Receipt(receiptRoot, "prod", hour)
	if ok {
		t.Fatal("a receipt whose manifest is absent from node1 must not authorise deletion")
	}
	if err == nil {
		t.Fatal("the missing manifest must be reported so the operator can re-push the hour")
	}
}

func readSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("local_cleanup.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func sliceBetween(t *testing.T, source, from, to string) string {
	t.Helper()
	start := strings.Index(source, from)
	if start < 0 {
		t.Fatalf("cannot find %q in source", from)
	}
	end := strings.Index(source[start:], to)
	if end < 0 {
		t.Fatalf("cannot find %q after %q", to, from)
	}
	return source[start : start+end]
}

// A streamed hour lives only on node1, so re-hashing it here is impossible.
// verifyNode1Manifest walks every file in the manifest and recomputes its
// sha256; against a remote push the local qa-raw tree is empty, so the gate
// failed for every hour and blocked the whole archive. On prod the local tree
// held 0 manifests while node1 held 23 hours.
func TestStreamedHourIsAcceptedWithoutLocalPayload(t *testing.T) {
	root := t.TempDir()
	receiptRoot := filepath.Join(root, "receipts")
	hour := time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC)
	path := filepath.Join(receiptRoot, "prod", hour.Format("2006/01/02/15")+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	receipt := `{"schema_version":"qa-node1-hour-v1","environment":"prod",` +
		`"window_start":"2026-11-02T13:00:00Z","window_end":"2026-11-02T14:00:00Z",` +
		`"ok":true,"file_count":8450,"total_bytes":540580477,` +
		`"transport":"ssh-tar",` +
		`"remote_dir":"/storage/tokenkey-archive/qa-raw/prod/2026/11/02/13",` +
		`"manifest_sha256":"` + strings.Repeat("d", 64) + `"}`
	if err := os.WriteFile(path, []byte(receipt), 0o644); err != nil {
		t.Fatal(err)
	}
	// Deliberately no local qa-raw tree: that is the live shape.
	ok, err := hasNode1Receipt(receiptRoot, "prod", hour)
	if err != nil {
		t.Fatalf("a streamed hour must not need a local payload: %v", err)
	}
	if !ok {
		t.Fatal("a verified streamed receipt must authorise deletion")
	}
}

// Without remote_dir there is no record of where the hour went, so the receipt
// is not evidence of anything.
func TestStreamedReceiptWithoutRemoteDirBlocks(t *testing.T) {
	root := t.TempDir()
	receiptRoot := filepath.Join(root, "receipts")
	hour := time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)
	path := filepath.Join(receiptRoot, "prod", hour.Format("2006/01/02/15")+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	receipt := `{"schema_version":"qa-node1-hour-v1","environment":"prod",` +
		`"window_start":"2026-11-02T14:00:00Z","window_end":"2026-11-02T15:00:00Z",` +
		`"ok":true,"file_count":1,"total_bytes":1,"transport":"ssh-tar",` +
		`"manifest_sha256":"` + strings.Repeat("e", 64) + `"}`
	if err := os.WriteFile(path, []byte(receipt), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := hasNode1Receipt(receiptRoot, "prod", hour)
	if ok {
		t.Fatal("a streamed receipt with no remote_dir must not authorise deletion")
	}
	if err == nil {
		t.Fatal("the missing remote_dir must be reported")
	}
}

// A local-copy receipt still has its payload here, so it is still re-hashed.
// Accepting one on the strength of the receipt alone would undo the check that
// caught the hours that never reached node1.
func TestLocalCopyHourStillRequiresItsPayload(t *testing.T) {
	root := t.TempDir()
	receiptRoot := filepath.Join(root, "receipts")
	hour := time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC)
	path := filepath.Join(receiptRoot, "prod", hour.Format("2006/01/02/15")+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	receipt := `{"schema_version":"qa-node1-hour-v1","environment":"prod",` +
		`"window_start":"2026-11-02T06:00:00Z","window_end":"2026-11-02T07:00:00Z",` +
		`"ok":true,"file_count":13210,"total_bytes":444973653,"tier":"hot_ssd",` +
		`"manifest_sha256":"` + strings.Repeat("f", 64) + `"}`
	if err := os.WriteFile(path, []byte(receipt), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := hasNode1Receipt(receiptRoot, "prod", hour)
	if ok {
		t.Fatal("a local-copy receipt with no payload must not authorise deletion")
	}
	if err == nil {
		t.Fatal("the absent local manifest must be reported")
	}
}
