package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const GapDecisionPlanSchemaVersion = "qa-archive-gap-decision-v1"

var gapDecisionHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type GapDecisionControl struct {
	Exists                bool      `json:"exists"`
	ShardID               int64     `json:"shard_id,omitempty"`
	WindowEnd             time.Time `json:"window_end"`
	State                 string    `json:"state,omitempty"`
	VerificationErrorCode string    `json:"verification_error_code,omitempty"`
	UpdatedAt             time.Time `json:"updated_at,omitempty"`
	SegmentFingerprint    string    `json:"segment_fingerprint"`
	HasCommitReadySegment bool      `json:"has_commit_ready_segment"`
}

type GapDecisionWindow struct {
	WindowStart       time.Time          `json:"window_start"`
	WindowEnd         time.Time          `json:"window_end"`
	CommitKey         string             `json:"commit_key"`
	CommitExists      bool               `json:"commit_exists"`
	SourceRecordCount int64              `json:"source_record_count"`
	Control           GapDecisionControl `json:"control"`
}

type GapDecisionAnchor struct {
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
}

type GapDecisionPlan struct {
	SchemaVersion           string              `json:"schema_version"`
	DBUTCAnchor             time.Time           `json:"db_utc_anchor"`
	RetentionCutoff         time.Time           `json:"retention_cutoff"`
	ForwardCutover          GapDecisionAnchor   `json:"forward_cutover"`
	LatestNormalWindowStart time.Time           `json:"latest_normal_window_start"`
	Region                  string              `json:"region"`
	Bucket                  string              `json:"bucket"`
	RecoveryRoleARN         string              `json:"recovery_role_arn"`
	RecoveryRunID           string              `json:"recovery_run_id"`
	Windows                 []GapDecisionWindow `json:"windows"`
	PlanHash                string              `json:"plan_hash"`
}

type GapDecisionReceipt struct {
	PlanHash       string    `json:"plan_hash"`
	ApprovedBy     string    `json:"approved_by"`
	WindowCount    int       `json:"window_count"`
	AppliedAt      time.Time `json:"applied_at"`
	AlreadyApplied bool      `json:"already_applied"`
}

func BuildGapDecisionDBPlan(ctx context.Context, db *sql.DB) (GapDecisionPlan, error) {
	if db == nil {
		return GapDecisionPlan{}, fmt.Errorf("gap decision plan: nil database")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GapDecisionPlan{}, fmt.Errorf("gap decision plan: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := acquireGapDecisionLock(ctx, tx); err != nil {
		return GapDecisionPlan{}, err
	}

	anchor, latestNormal, err := readGapDecisionAnchors(ctx, tx)
	if err != nil {
		return GapDecisionPlan{}, err
	}
	cutover, err := readGapDecisionCutover(ctx, tx)
	if err != nil {
		return GapDecisionPlan{}, err
	}
	cutoff := anchor.Add(-24 * time.Hour)
	rows, err := tx.QueryContext(ctx, `
WITH hours AS (
    SELECT generate_series($1::timestamptz, $2::timestamptz - interval '1 hour', interval '1 hour') AS window_start
)
SELECT h.window_start, s.id, s.window_end, s.state, s.verification_error_code,
	   s.updated_at,
	   COALESCE((
	       SELECT string_agg(
	           seg.id::text || ':' || seg.state || ':' ||
	           to_char(seg.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
	           ',' ORDER BY seg.id
	       )
	       FROM qa_archive_segments seg WHERE seg.shard_id=s.id
	   ), '') AS segment_fingerprint,
	   COALESCE((
	       SELECT bool_or(seg.state IN ('verified','committed'))
	       FROM qa_archive_segments seg WHERE seg.shard_id=s.id
	   ), false) AS has_commit_ready_segment,
	   (SELECT count(*) FROM qa_records q
        WHERE q.created_at >= h.window_start AND q.created_at < h.window_start + interval '1 hour') AS source_record_count
FROM hours h
LEFT JOIN qa_archive_shards s ON s.window_start=h.window_start AND s.generation=0
WHERE h.window_start < $3
ORDER BY h.window_start`, cutover.End, latestNormal, cutoff)
	if err != nil {
		return GapDecisionPlan{}, fmt.Errorf("gap decision plan: enumerate timeline: %w", err)
	}
	defer func() { _ = rows.Close() }()

	plan := GapDecisionPlan{
		SchemaVersion: GapDecisionPlanSchemaVersion, DBUTCAnchor: anchor,
		RetentionCutoff:         cutoff,
		ForwardCutover:          GapDecisionAnchor{WindowStart: cutover.Start, WindowEnd: cutover.End},
		LatestNormalWindowStart: latestNormal,
	}
	for rows.Next() {
		var start time.Time
		var shardID sql.NullInt64
		var storedEnd sql.NullTime
		var state, code sql.NullString
		var updatedAt sql.NullTime
		var segmentFingerprint string
		var hasCommitReadySegment bool
		var sourceCount int64
		if err := rows.Scan(&start, &shardID, &storedEnd, &state, &code, &updatedAt, &segmentFingerprint, &hasCommitReadySegment, &sourceCount); err != nil {
			return GapDecisionPlan{}, fmt.Errorf("gap decision plan: scan timeline: %w", err)
		}
		control := GapDecisionControl{
			Exists: shardID.Valid, ShardID: shardID.Int64,
			State: state.String, VerificationErrorCode: code.String,
			SegmentFingerprint: segmentFingerprint, HasCommitReadySegment: hasCommitReadySegment,
		}
		if storedEnd.Valid {
			control.WindowEnd = storedEnd.Time.UTC()
		}
		if updatedAt.Valid {
			control.UpdatedAt = updatedAt.Time.UTC()
		}
		if sourceCount != 0 || !gapDecisionControlEligible(control) {
			continue
		}
		start = start.UTC()
		plan.Windows = append(plan.Windows, GapDecisionWindow{
			WindowStart: start, WindowEnd: start.Add(time.Hour),
			CommitKey:         ShardPrefix(start) + "/commit.json",
			SourceRecordCount: sourceCount, Control: control,
		})
	}
	if err := rows.Err(); err != nil {
		return GapDecisionPlan{}, fmt.Errorf("gap decision plan: timeline: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return GapDecisionPlan{}, fmt.Errorf("gap decision plan: commit read snapshot: %w", err)
	}
	return plan, nil
}

func CompleteGapDecisionPlan(
	dbPlan GapDecisionPlan,
	region, bucket, recoveryRoleARN, recoveryRunID string,
	commitExists map[string]bool,
) (GapDecisionPlan, error) {
	if err := validateGapDecisionDBPlan(dbPlan); err != nil {
		return GapDecisionPlan{}, err
	}
	plan := dbPlan
	plan.Region = strings.TrimSpace(region)
	plan.Bucket = strings.TrimSpace(bucket)
	plan.RecoveryRoleARN = strings.TrimSpace(recoveryRoleARN)
	plan.RecoveryRunID = strings.TrimSpace(recoveryRunID)
	plan.PlanHash = ""
	confirmedMissing := make([]GapDecisionWindow, 0, len(plan.Windows))
	for index := range plan.Windows {
		exists, ok := commitExists[plan.Windows[index].CommitKey]
		if !ok {
			return GapDecisionPlan{}, fmt.Errorf("gap decision plan: missing S3 fact for %s", plan.Windows[index].CommitKey)
		}
		plan.Windows[index].CommitExists = exists
		if !exists {
			confirmedMissing = append(confirmedMissing, plan.Windows[index])
		}
	}
	plan.Windows = confirmedMissing
	if len(plan.Windows) == 0 {
		return GapDecisionPlan{}, fmt.Errorf("gap decision plan: no confirmed missing commit windows")
	}
	if err := validateGapDecisionPlan(plan, false); err != nil {
		return GapDecisionPlan{}, err
	}
	hash, err := HashGapDecisionPlan(plan)
	if err != nil {
		return GapDecisionPlan{}, err
	}
	plan.PlanHash = hash
	return plan, nil
}

func CompleteGapDecisionPlanFromStore(
	ctx context.Context,
	dbPlan GapDecisionPlan,
	store ReadOnlyObjectStore,
	region, bucket, recoveryRoleARN, recoveryRunID string,
) (GapDecisionPlan, error) {
	if store == nil {
		return GapDecisionPlan{}, fmt.Errorf("gap decision plan: nil recovery store")
	}
	if err := validateGapDecisionDBPlan(dbPlan); err != nil {
		return GapDecisionPlan{}, err
	}
	commitExists := make(map[string]bool, len(dbPlan.Windows))
	for _, window := range dbPlan.Windows {
		key := strings.TrimPrefix(window.CommitKey, RawV1Prefix+"/")
		if key == window.CommitKey || key == "" {
			return GapDecisionPlan{}, fmt.Errorf("gap decision plan: commit key is outside raw/v1")
		}
		exists, err := store.Head(ctx, key)
		if err != nil {
			return GapDecisionPlan{}, fmt.Errorf("gap decision plan: inspect recovery commit %s: %w", window.CommitKey, err)
		}
		commitExists[window.CommitKey] = exists
	}
	return CompleteGapDecisionPlan(
		dbPlan, region, bucket, recoveryRoleARN, recoveryRunID, commitExists,
	)
}

func HashGapDecisionPlan(plan GapDecisionPlan) (string, error) {
	plan.PlanHash = ""
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("gap decision plan: encode canonical facts: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return "", fmt.Errorf("gap decision plan: normalize canonical facts: %w", err)
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return "", fmt.Errorf("gap decision plan: encode normalized facts: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func ApplyGapDecisionPlan(ctx context.Context, db *sql.DB, plan GapDecisionPlan, approvedBy string) (GapDecisionReceipt, error) {
	if db == nil {
		return GapDecisionReceipt{}, fmt.Errorf("gap decision apply: nil database")
	}
	approvedBy = strings.TrimSpace(approvedBy)
	if approvedBy == "" {
		return GapDecisionReceipt{}, fmt.Errorf("gap decision apply: approved-by is required")
	}
	if err := validateGapDecisionPlan(plan, true); err != nil {
		return GapDecisionReceipt{}, err
	}
	recomputed, err := HashGapDecisionPlan(plan)
	if err != nil {
		return GapDecisionReceipt{}, err
	}
	if recomputed != plan.PlanHash {
		return GapDecisionReceipt{}, fmt.Errorf("gap decision apply: plan hash drift")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return GapDecisionReceipt{}, fmt.Errorf("gap decision apply: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := acquireGapDecisionLock(ctx, tx); err != nil {
		return GapDecisionReceipt{}, err
	}
	if receipt, ok, err := readGapDecisionReceipt(ctx, tx, plan.PlanHash); err != nil {
		return GapDecisionReceipt{}, err
	} else if ok {
		if err := tx.Commit(); err != nil {
			return GapDecisionReceipt{}, fmt.Errorf("gap decision apply: commit replay: %w", err)
		}
		receipt.AlreadyApplied = true
		return receipt, nil
	}

	anchor, latestNormal, err := readGapDecisionAnchors(ctx, tx)
	if err != nil {
		return GapDecisionReceipt{}, err
	}
	if !anchor.Equal(plan.DBUTCAnchor) || !anchor.Add(-24*time.Hour).Equal(plan.RetentionCutoff) ||
		!latestNormal.Equal(plan.LatestNormalWindowStart) {
		return GapDecisionReceipt{}, fmt.Errorf("gap decision apply: database anchor or latest normal drift")
	}
	cutover, err := readGapDecisionCutover(ctx, tx)
	if err != nil {
		return GapDecisionReceipt{}, err
	}
	if !cutover.Start.Equal(plan.ForwardCutover.WindowStart) || !cutover.End.Equal(plan.ForwardCutover.WindowEnd) {
		return GapDecisionReceipt{}, fmt.Errorf("gap decision apply: forward cutover drift")
	}

	store := NewSQLControlStore()
	for _, planned := range plan.Windows {
		live, err := inspectGapDecisionControl(ctx, tx, planned.WindowStart)
		if err != nil {
			return GapDecisionReceipt{}, err
		}
		if !equalGapDecisionControl(live, planned.Control) {
			return GapDecisionReceipt{}, fmt.Errorf("gap decision apply: control drift for %s", planned.WindowStart.Format(time.RFC3339))
		}
		var sourceCount int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM qa_records WHERE created_at >= $1 AND created_at < $2`,
			planned.WindowStart, planned.WindowEnd).Scan(&sourceCount); err != nil {
			return GapDecisionReceipt{}, fmt.Errorf("gap decision apply: recount %s: %w", planned.WindowStart.Format(time.RFC3339), err)
		}
		if sourceCount != planned.SourceRecordCount {
			return GapDecisionReceipt{}, fmt.Errorf("gap decision apply: source row count drift for %s", planned.WindowStart.Format(time.RFC3339))
		}
		if _, err := store.PersistApprovedGapTerminal(ctx, tx, Window{Start: planned.WindowStart, End: planned.WindowEnd}); err != nil {
			return GapDecisionReceipt{}, err
		}
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return GapDecisionReceipt{}, fmt.Errorf("gap decision apply: encode receipt plan: %w", err)
	}
	var appliedAt time.Time
	if err := tx.QueryRowContext(ctx, `
INSERT INTO qa_archive_gap_decision_receipts
    (plan_hash, plan_schema_version, plan_json, approved_by, window_count)
VALUES ($1,$2,$3,$4,$5)
RETURNING applied_at`, plan.PlanHash, GapDecisionPlanSchemaVersion, planJSON, approvedBy, len(plan.Windows)).Scan(&appliedAt); err != nil {
		return GapDecisionReceipt{}, fmt.Errorf("gap decision apply: persist approval receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return GapDecisionReceipt{}, fmt.Errorf("gap decision apply: commit: %w", err)
	}
	return GapDecisionReceipt{
		PlanHash: plan.PlanHash, ApprovedBy: approvedBy, WindowCount: len(plan.Windows), AppliedAt: appliedAt.UTC(),
	}, nil
}

func (s *SQLControlStore) PersistApprovedGapTerminal(ctx context.Context, tx *sql.Tx, window Window) (int64, error) {
	window.Start = window.Start.UTC()
	window.End = window.End.UTC()
	if !window.End.Equal(window.Start.Add(time.Hour)) {
		return 0, fmt.Errorf("approved gap: window must be one UTC hour")
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO qa_archive_shards (
    window_start, window_end, generation, state, s3_prefix, first_attempt_at,
    cleanup_eligible, created_at, updated_at
) VALUES ($1,$2,0,$3,$4,now(),false,now(),now())
ON CONFLICT (window_start, generation) DO NOTHING`, window.Start, window.End, StatePending, ShardPrefix(window.Start)); err != nil {
		return 0, fmt.Errorf("approved gap: insert control: %w", err)
	}
	var shardID int64
	var state string
	var code sql.NullString
	var restored sql.NullTime
	if err := tx.QueryRowContext(ctx, `
SELECT id, state, verification_error_code, restore_verified_at
FROM qa_archive_shards
WHERE window_start=$1 AND generation=0
FOR UPDATE`, window.Start).Scan(&shardID, &state, &code, &restored); err != nil {
		return 0, fmt.Errorf("approved gap: lock control: %w", err)
	}
	if state == StateFailed && code.String == IntegritySourceUnavailableAfterRetention {
		return shardID, nil
	}
	if state == StateCommitted || (state == StateFailed && IsTerminalArchiveFailure(code.String)) {
		return 0, fmt.Errorf("approved gap: immutable shard state=%s code=%s", state, code.String)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE qa_archive_shards SET
    state=$1, verification_error_code=$2,
    last_error='source unavailable after retention under approved gap decision',
    last_reconciled_at=now(), cleanup_eligible=false, updated_at=now()
WHERE id=$3 AND state IN ('pending','writing','verified','failed')`,
		StateFailed, IntegritySourceUnavailableAfterRetention, shardID)
	if err != nil {
		return 0, fmt.Errorf("approved gap: update control: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return 0, fmt.Errorf("approved gap: shard changed concurrently")
	}
	return shardID, nil
}

func acquireGapDecisionLock(ctx context.Context, tx *sql.Tx) error {
	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, MaintenanceAdvisoryLockID).Scan(&locked); err != nil {
		return fmt.Errorf("gap decision: acquire QAMA lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("gap decision: QAMA lock already held")
	}
	return nil
}

func readGapDecisionAnchors(ctx context.Context, tx *sql.Tx) (time.Time, time.Time, error) {
	var anchor, latest time.Time
	err := tx.QueryRowContext(ctx, `
SELECT date_trunc('hour', clock_timestamp()), COALESCE(max(window_start), '-infinity'::timestamptz)
FROM qa_archive_shards
WHERE state='committed' AND restore_verified_at IS NOT NULL
  AND window_start < date_trunc('hour', clock_timestamp())`).Scan(&anchor, &latest)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("gap decision: read database anchors: %w", err)
	}
	if latest.IsZero() || latest.Year() < 2000 {
		return time.Time{}, time.Time{}, fmt.Errorf("gap decision: latest normal committed hour missing")
	}
	return anchor.UTC(), latest.UTC(), nil
}

func readGapDecisionCutover(ctx context.Context, tx *sql.Tx) (Window, error) {
	var out Window
	err := tx.QueryRowContext(ctx, `
SELECT window_start, window_end
FROM qa_archive_shards
WHERE forward_cutover=true`).Scan(&out.Start, &out.End)
	if errors.Is(err, sql.ErrNoRows) {
		return Window{}, fmt.Errorf("gap decision: forward cutover missing")
	}
	if err != nil {
		return Window{}, fmt.Errorf("gap decision: read forward cutover: %w", err)
	}
	out.Start, out.End = out.Start.UTC(), out.End.UTC()
	return out, nil
}

func inspectGapDecisionControl(ctx context.Context, tx *sql.Tx, start time.Time) (GapDecisionControl, error) {
	var out GapDecisionControl
	var code sql.NullString
	var updatedAt time.Time
	var segmentFingerprint string
	err := tx.QueryRowContext(ctx, `
SELECT s.id, s.window_end, s.state, s.verification_error_code, s.updated_at,
	   COALESCE((
	       SELECT string_agg(
	           seg.id::text || ':' || seg.state || ':' ||
	           to_char(seg.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
	           ',' ORDER BY seg.id
	       )
	       FROM qa_archive_segments seg WHERE seg.shard_id=s.id
	   ), '') AS segment_fingerprint,
	   COALESCE((
	       SELECT bool_or(seg.state IN ('verified','committed'))
	       FROM qa_archive_segments seg WHERE seg.shard_id=s.id
	   ), false) AS has_commit_ready_segment
FROM qa_archive_shards s
	WHERE s.window_start=$1 AND s.generation=0
	FOR UPDATE OF s`, start).Scan(&out.ShardID, &out.WindowEnd, &out.State, &code, &updatedAt, &segmentFingerprint, &out.HasCommitReadySegment)
	if errors.Is(err, sql.ErrNoRows) {
		return GapDecisionControl{}, nil
	}
	if err != nil {
		return GapDecisionControl{}, fmt.Errorf("gap decision apply: inspect control: %w", err)
	}
	out.Exists = true
	out.VerificationErrorCode = code.String
	out.UpdatedAt = updatedAt.UTC()
	out.WindowEnd = out.WindowEnd.UTC()
	out.SegmentFingerprint = segmentFingerprint
	return out, nil
}

func readGapDecisionReceipt(ctx context.Context, tx *sql.Tx, planHash string) (GapDecisionReceipt, bool, error) {
	var out GapDecisionReceipt
	err := tx.QueryRowContext(ctx, `
SELECT plan_hash, approved_by, window_count, applied_at
FROM qa_archive_gap_decision_receipts
WHERE plan_hash=$1`, planHash).Scan(&out.PlanHash, &out.ApprovedBy, &out.WindowCount, &out.AppliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return GapDecisionReceipt{}, false, nil
	}
	if err != nil {
		return GapDecisionReceipt{}, false, fmt.Errorf("gap decision apply: read receipt: %w", err)
	}
	out.AppliedAt = out.AppliedAt.UTC()
	return out, true, nil
}

func gapDecisionControlEligible(control GapDecisionControl) bool {
	if !control.Exists {
		return true
	}
	if control.State == StateCommitted || (control.State == StateFailed && IsTerminalArchiveFailure(control.VerificationErrorCode)) {
		return false
	}
	switch control.State {
	case StatePending, StateWriting, StateVerified, StateFailed:
		return true
	default:
		return false
	}
}

func validateGapDecisionDBPlan(plan GapDecisionPlan) error {
	if plan.Region != "" || plan.Bucket != "" || plan.RecoveryRoleARN != "" ||
		plan.RecoveryRunID != "" || plan.PlanHash != "" {
		return fmt.Errorf("gap decision plan: database plan contains recovery or hash facts")
	}
	return validateGapDecisionTimeline(plan)
}

func validateGapDecisionPlan(plan GapDecisionPlan, requireHash bool) error {
	if strings.TrimSpace(plan.Region) == "" || strings.TrimSpace(plan.Bucket) == "" ||
		!strings.HasPrefix(strings.TrimSpace(plan.RecoveryRoleARN), "arn:aws:iam::") ||
		strings.TrimSpace(plan.RecoveryRunID) == "" {
		return fmt.Errorf("gap decision plan: recovery scope is incomplete")
	}
	if requireHash && !gapDecisionHashPattern.MatchString(plan.PlanHash) {
		return fmt.Errorf("gap decision plan: canonical SHA-256 is required")
	}
	return validateGapDecisionTimeline(plan)
}

func validateGapDecisionTimeline(plan GapDecisionPlan) error {
	if plan.SchemaVersion != GapDecisionPlanSchemaVersion {
		return fmt.Errorf("gap decision plan: unsupported schema version")
	}
	if !isExactUTCHour(plan.DBUTCAnchor) || !isExactUTCHour(plan.RetentionCutoff) ||
		!isExactUTCHour(plan.ForwardCutover.WindowStart) || !isExactUTCHour(plan.ForwardCutover.WindowEnd) ||
		!isExactUTCHour(plan.LatestNormalWindowStart) ||
		!plan.RetentionCutoff.Equal(plan.DBUTCAnchor.Add(-24*time.Hour)) ||
		!plan.ForwardCutover.WindowEnd.Equal(plan.ForwardCutover.WindowStart.Add(time.Hour)) ||
		plan.LatestNormalWindowStart.Before(plan.ForwardCutover.WindowEnd) ||
		!plan.LatestNormalWindowStart.Before(plan.DBUTCAnchor) || len(plan.Windows) == 0 {
		return fmt.Errorf("gap decision plan: invalid timeline anchors")
	}
	ordered := append([]GapDecisionWindow(nil), plan.Windows...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].WindowStart.Before(ordered[j].WindowStart) })
	for index, window := range plan.Windows {
		if index > 0 && !plan.Windows[index-1].WindowStart.Before(window.WindowStart) {
			return fmt.Errorf("gap decision plan: windows must be unique and sorted")
		}
		if !isExactUTCHour(window.WindowStart) || !isExactUTCHour(window.WindowEnd) ||
			!window.WindowEnd.Equal(window.WindowStart.Add(time.Hour)) ||
			window.WindowStart.Before(plan.ForwardCutover.WindowEnd) ||
			!window.WindowStart.Before(plan.RetentionCutoff) ||
			!window.WindowStart.Before(plan.LatestNormalWindowStart) ||
			window.SourceRecordCount != 0 || window.CommitExists ||
			window.CommitKey != ShardPrefix(window.WindowStart)+"/commit.json" ||
			!validGapDecisionControl(window.Control) ||
			(window.Control.Exists && !window.Control.WindowEnd.Equal(window.WindowEnd)) {
			return fmt.Errorf("gap decision plan: ineligible window %s", window.WindowStart.Format(time.RFC3339))
		}
		if !ordered[index].WindowStart.Equal(window.WindowStart) {
			return fmt.Errorf("gap decision plan: windows must be sorted")
		}
	}
	return nil
}

func isExactUTCHour(value time.Time) bool {
	if value.IsZero() || value.Year() < 2000 {
		return false
	}
	_, offset := value.Zone()
	return offset == 0 && value.Equal(value.UTC().Truncate(time.Hour))
}

func validGapDecisionControl(control GapDecisionControl) bool {
	if !control.Exists {
		return control.ShardID == 0 && control.State == "" && control.VerificationErrorCode == "" &&
			control.WindowEnd.IsZero() && control.UpdatedAt.IsZero() && control.SegmentFingerprint == "" &&
			!control.HasCommitReadySegment
	}
	if (control.State == StateFailed) != (strings.TrimSpace(control.VerificationErrorCode) != "") {
		return false
	}
	return control.ShardID > 0 && isExactUTCHour(control.WindowEnd) && control.State != "" &&
		!control.UpdatedAt.IsZero() && !control.HasCommitReadySegment &&
		gapDecisionControlEligible(control)
}

func equalGapDecisionControl(left, right GapDecisionControl) bool {
	return left.Exists == right.Exists && left.ShardID == right.ShardID && left.WindowEnd.Equal(right.WindowEnd) &&
		left.State == right.State &&
		left.VerificationErrorCode == right.VerificationErrorCode && left.UpdatedAt.Equal(right.UpdatedAt) &&
		left.SegmentFingerprint == right.SegmentFingerprint &&
		left.HasCommitReadySegment == right.HasCommitReadySegment
}
