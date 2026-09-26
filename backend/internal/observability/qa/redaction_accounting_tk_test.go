package qa

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/captureledger"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/stretchr/testify/require"
)

// The accounting counters are process-wide, so every test here starts from a
// known zero instead of inheriting whatever ran before it.
func resetQARedactionAccounting(t *testing.T) {
	t.Helper()
	reset := func() {
		qaRedactedRecordCount.Store(0)
		qaRedactedByteCount.Store(0)
		qaUnknownFormatRecordCount.Store(0)
		qaUnknownFormatByteCount.Store(0)
	}
	reset()
	t.Cleanup(reset)
}

func TestQARedactionAccountingCountsUnknownFormatBytesAndRecords(t *testing.T) {
	resetQARedactionAccounting(t)
	svc := &Service{bodyMaxBytes: 1 << 20, optInBodyMaxBytes: 1 << 20}

	const unknown = "opaque token=secret"
	require.Equal(t, unknown, svc.sanitizeQABodyMemo([]byte(unknown), false, newQARedactionMemo()))
	jsonBody := []byte(`{"token":"secret","text":"ordinary"}`)
	svc.sanitizeQABodyMemo(jsonBody, false, newQARedactionMemo())

	got := qaRedactionAccountingSnapshot()
	require.Equal(t, int64(2), got.RedactedRecords)
	require.Equal(t, int64(len(unknown)+len(jsonBody)), got.RedactedBytes)
	require.Equal(t, int64(1), got.UnknownFormatRecords, "only the unretained-format payload counts as unknown")
	require.Equal(t, int64(len(unknown)), got.UnknownFormatBytes, "unknown bytes must be the bytes actually retained verbatim")
	require.Equal(t, qaRedactionVersion, got.RedactionVersion)
}

// A memo hit returns before classification, so one payload repeated across
// fields must not inflate the residual it represents.
func TestQARedactionAccountingCountsEachPayloadOnce(t *testing.T) {
	resetQARedactionAccounting(t)
	svc := &Service{bodyMaxBytes: 1 << 20, optInBodyMaxBytes: 1 << 20}
	memo := newQARedactionMemo()

	const unknown = "opaque token=secret"
	for i := 0; i < 5; i++ {
		svc.sanitizeQABodyMemo([]byte(unknown), false, memo)
	}

	got := qaRedactionAccountingSnapshot()
	require.Equal(t, int64(1), got.RedactedRecords)
	require.Equal(t, int64(1), got.UnknownFormatRecords)
	require.Equal(t, int64(len(unknown)), got.UnknownFormatBytes)
}

// Empty and whitespace-only payloads return before classification too, so they
// are not records at all rather than unknown-format ones.
func TestQARedactionAccountingIgnoresEmptyPayloads(t *testing.T) {
	resetQARedactionAccounting(t)
	svc := &Service{bodyMaxBytes: 1 << 20, optInBodyMaxBytes: 1 << 20}

	svc.sanitizeQABodyMemo(nil, false, newQARedactionMemo())
	svc.sanitizeQABodyMemo([]byte("   "), false, newQARedactionMemo())

	require.Equal(t, qaRedactionAccounting{RedactionVersion: qaRedactionVersion}, qaRedactionAccountingSnapshot())
}

func TestQARedactionAccountingDriftNeedsSustainedShare(t *testing.T) {
	resetQARedactionAccounting(t)

	// A handful of unknown payloads is a ratio of 1.0 but not evidence; the
	// record floor keeps it from pinning drift for the process lifetime.
	for i := 0; i < 10; i++ {
		recordQARedactionFormat(logredact.FormatUnknown, 16)
	}
	require.False(t, qaRedactionAccountingSnapshot().Drift, "a few unknown payloads must not report drift")

	// Past the floor, a share above the threshold is drift.
	resetQARedactionAccounting(t)
	for i := 0; i < qaUnknownFormatDriftMinRecords; i++ {
		recordQARedactionFormat(logredact.FormatJSON, 16)
	}
	require.False(t, qaRedactionAccountingSnapshot().Drift, "fully redacted traffic is not drift")
	for i := 0; i < 40; i++ {
		recordQARedactionFormat(logredact.FormatUnknown, 16)
	}
	got := qaRedactionAccountingSnapshot()
	require.True(t, got.Drift, "a sustained unknown-format share must report drift")
	require.Greater(t, got.UnknownFormatRatio, qaUnknownFormatDriftRatio)
}

func TestQACaptureHealthCarriesRedactionAccountingAndDegradesOnDrift(t *testing.T) {
	resetQARedactionAccounting(t)
	now := time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC)
	ledger, err := captureledger.Open(t.TempDir(), "runtime-a", now, func() time.Time { return now })
	require.NoError(t, err)
	svc := &Service{captureLedger: ledger}

	status, result, err := svc.QACaptureHealth()
	require.NoError(t, err)
	require.Equal(t, "healthy", status)

	// The ledger's own fields must survive the merge unchanged.
	var mirrored captureledger.Health
	require.NoError(t, json.Unmarshal([]byte(result), &mirrored))
	require.Equal(t, captureledger.HealthHealthy, mirrored.Status)

	var envelope struct {
		Redaction qaRedactionAccounting `json:"redaction"`
	}
	require.NoError(t, json.Unmarshal([]byte(result), &envelope))
	require.Equal(t, qaRedactionVersion, envelope.Redaction.RedactionVersion)
	require.False(t, envelope.Redaction.Drift)

	// Drift degrades a healthy ledger, which is what reaches the qa_capture
	// heartbeat, and says so in the payload rather than only in the status.
	for i := 0; i < qaUnknownFormatDriftMinRecords; i++ {
		recordQARedactionFormat(logredact.FormatUnknown, 16)
	}
	status, result, err = svc.QACaptureHealth()
	require.NoError(t, err)
	require.Equal(t, "degraded", status, "unknown-format drift must be visible as a status, not only as a counter")
	require.True(t, strings.Contains(result, `"unknown_format_drift":true`))
}

// Drift is a privacy signal, so it must not rewrite a failed ledger into a
// milder status, and it must not turn a working capture path into failed.
func TestQACaptureHealthDriftDoesNotOverrideLedgerFailure(t *testing.T) {
	resetQARedactionAccounting(t)
	for i := 0; i < qaUnknownFormatDriftMinRecords; i++ {
		recordQARedactionFormat(logredact.FormatUnknown, 16)
	}
	svc := &Service{ledgerInitErr: errors.New("ledger did not open")}
	status, _, err := svc.QACaptureHealth()
	require.Error(t, err)
	require.Equal(t, "failed", status, "drift cannot soften a failed ledger")
}
