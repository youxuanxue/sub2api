package captureledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	DefaultFreshness = 5 * time.Minute
	ledgerVersion    = 1
)

var ErrHourUnsealed = errors.New("qa capture hour is not sealed")

type Outcome string

const (
	OutcomePersisted     Outcome = "persisted"
	OutcomeDegraded      Outcome = "degraded"
	OutcomePersistFailed Outcome = "persist_failed"
)

type HealthStatus string

const (
	HealthHealthy  HealthStatus = "healthy"
	HealthDegraded HealthStatus = "degraded"
	HealthFailed   HealthStatus = "failed"
)

type CaptureIdentity struct {
	RequestID  string    `json:"request_id"`
	CapturedAt time.Time `json:"captured_at"`
}

func (i CaptureIdentity) SourceHour() time.Time {
	return i.CapturedAt.UTC().Truncate(time.Hour)
}

type HourCounters struct {
	Pending         int64      `json:"pending"`
	Inflight        int64      `json:"inflight"`
	Persisted       int64      `json:"persisted"`
	Degraded        int64      `json:"degraded"`
	Failed          int64      `json:"failed"`
	BacklogSince    *time.Time `json:"backlog_since,omitempty"`
	LastSubmittedAt *time.Time `json:"last_submitted_at,omitempty"`
	LastPersistedAt *time.Time `json:"last_persisted_at,omitempty"`
}

type RuntimeReceipt struct {
	Version          int                      `json:"version"`
	RuntimeID        string                   `json:"runtime_id"`
	StartedAt        time.Time                `json:"started_at"`
	LastSnapshotAt   time.Time                `json:"last_snapshot_at"`
	DrainedAt        *time.Time               `json:"drained_at,omitempty"`
	Drained          bool                     `json:"drained"`
	UnsealableSince  *time.Time               `json:"unsealable_since,omitempty"`
	UnsealableReason string                   `json:"unsealable_reason,omitempty"`
	TransitionClean  bool                     `json:"transition_clean"`
	Hours            map[string]*HourCounters `json:"hours"`
}

type FailureReceipt struct {
	Version       int             `json:"version"`
	FailureID     string          `json:"failure_id"`
	Identity      CaptureIdentity `json:"identity"`
	SourceHour    time.Time       `json:"source_hour"`
	Stage         string          `json:"stage"`
	OccurredAt    time.Time       `json:"occurred_at"`
	IntervalStart *time.Time      `json:"interval_start,omitempty"`
	IntervalEnd   *time.Time      `json:"interval_end,omitempty"`
	RuntimeID     string          `json:"runtime_id"`
	Status        string          `json:"status"`
	RecoveredAt   *time.Time      `json:"recovered_at,omitempty"`
}

type HourSeal struct {
	Version     int       `json:"version"`
	SourceHour  time.Time `json:"source_hour"`
	SealedAt    time.Time `json:"sealed_at"`
	RuntimeIDs  []string  `json:"runtime_ids"`
	StateDigest string    `json:"state_digest"`
}

type Health struct {
	Status             HealthStatus `json:"status"`
	Reason             string       `json:"reason,omitempty"`
	ObservedAt         time.Time    `json:"observed_at"`
	UnresolvedFailures int          `json:"unresolved_failures"`
	Pending            int64        `json:"pending"`
	Inflight           int64        `json:"inflight"`
}

type Ledger struct {
	root        string
	runtimePath string
	now         func() time.Time
	mu          sync.Mutex
	receipt     RuntimeReceipt
	writeAtomic func(string, []byte) error
}

func Open(root, runtimeID string, startedAt time.Time, now func() time.Time) (*Ledger, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || root == "" {
		return nil, errors.New("qa capture ledger root is required")
	}
	runtimeID = strings.TrimSpace(runtimeID)
	if !safeName(runtimeID) {
		return nil, errors.New("qa capture ledger runtime id is unsafe")
	}
	if now == nil {
		now = time.Now
	}
	for _, dir := range []string{root, filepath.Join(root, "runtimes"), filepath.Join(root, "failures"), filepath.Join(root, "seals")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create qa capture ledger directory: %w", err)
		}
	}

	observedAt := now().UTC()
	startedAt = startedAt.UTC()
	if startedAt.IsZero() || startedAt.After(observedAt) {
		startedAt = observedAt
	}
	l := &Ledger{
		root:        root,
		runtimePath: filepath.Join(root, "runtimes", runtimeID+".json"),
		now:         now,
		writeAtomic: writeFileAtomic,
		receipt: RuntimeReceipt{
			Version:         ledgerVersion,
			RuntimeID:       runtimeID,
			StartedAt:       startedAt,
			LastSnapshotAt:  observedAt,
			TransitionClean: true,
			Hours:           map[string]*HourCounters{},
		},
	}
	l.ensureHours(startedAt, observedAt)
	if err := l.persistRuntime(); err != nil {
		return nil, err
	}
	if err := l.recordStaleRuntimes(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Ledger) Root() string {
	if l == nil {
		return ""
	}
	return l.root
}

func (l *Ledger) Begin(identity CaptureIdentity) error {
	return l.mutateRuntime(func(receipt *RuntimeReceipt, observedAt time.Time) error {
		hour, err := receipt.hour(identity.SourceHour())
		if err != nil {
			return err
		}
		if hour.Pending+hour.Inflight == 0 {
			hour.BacklogSince = timePointer(observedAt)
		}
		hour.Pending++
		hour.LastSubmittedAt = timePointer(observedAt)
		return nil
	})
}

func (l *Ledger) Start(identity CaptureIdentity) error {
	return l.mutateRuntime(func(receipt *RuntimeReceipt, _ time.Time) error {
		hour, err := receipt.hour(identity.SourceHour())
		if err != nil {
			return err
		}
		if hour.Pending <= 0 {
			return errors.New("qa capture ledger pending transition missing")
		}
		hour.Pending--
		hour.Inflight++
		return nil
	})
}

func (l *Ledger) Complete(identity CaptureIdentity, outcome Outcome) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	observedAt := l.now().UTC()
	l.ensureHours(l.receipt.LastSnapshotAt, observedAt)
	hour, err := l.receipt.hour(identity.SourceHour())
	if err != nil {
		return err
	}
	if hour.Inflight <= 0 {
		return errors.New("qa capture ledger inflight transition missing")
	}
	hour.Inflight--
	switch outcome {
	case OutcomePersisted:
		hour.Persisted++
		hour.LastPersistedAt = timePointer(observedAt)
	case OutcomeDegraded:
		hour.Persisted++
		hour.Degraded++
		hour.LastPersistedAt = timePointer(observedAt)
	case OutcomePersistFailed:
		hour.Failed++
		failure := FailureReceipt{
			Version:    ledgerVersion,
			FailureID:  failureID(identity),
			Identity:   normalizedIdentity(identity),
			SourceHour: identity.SourceHour(),
			Stage:      "persist",
			OccurredAt: observedAt,
			RuntimeID:  l.receipt.RuntimeID,
			Status:     "unresolved",
		}
		if err := l.persistFailure(failure); err != nil {
			l.markUnsealable(observedAt, "ledger_write_failed")
		}
	default:
		return fmt.Errorf("unknown qa capture outcome %q", outcome)
	}
	if hour.Pending+hour.Inflight == 0 {
		hour.BacklogSince = nil
	}
	l.receipt.LastSnapshotAt = observedAt
	if err := l.persistRuntime(); err != nil {
		l.markUnsealable(observedAt, "ledger_write_failed")
		return err
	}
	return nil
}

func (l *Ledger) Recover(identity CaptureIdentity) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	path := filepath.Join(l.root, "failures", failureID(identity)+".json")
	var failure FailureReceipt
	if err := readJSON(path, &failure); err != nil {
		return err
	}
	if failure.Status == "recovered" {
		return nil
	}
	now := l.now().UTC()
	failure.Status = "recovered"
	failure.RecoveredAt = &now
	return l.persistJSON(path, failure)
}

func (l *Ledger) Snapshot() error {
	if err := l.mutateRuntime(func(_ *RuntimeReceipt, _ time.Time) error { return nil }); err != nil {
		return err
	}
	return l.recordStaleRuntimes()
}

func (l *Ledger) Drain() error {
	return l.mutateRuntime(func(receipt *RuntimeReceipt, observedAt time.Time) error {
		for _, counters := range receipt.Hours {
			if counters.Pending != 0 || counters.Inflight != 0 {
				return errors.New("qa capture ledger cannot drain with active captures")
			}
		}
		receipt.Drained = true
		receipt.DrainedAt = timePointer(observedAt)
		return nil
	})
}

func (l *Ledger) SealHour(sourceHour time.Time) (HourSeal, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var seal HourSeal
	err := withLedgerLock(l.root, func() error {
		state, err := loadState(l.root)
		if err != nil {
			return err
		}
		seal, err = evaluateHour(state, sourceHour, l.now().UTC(), DefaultFreshness)
		if err != nil {
			return err
		}
		return l.persistJSONUnlocked(sealPath(l.root, sourceHour), seal)
	})
	return seal, err
}

func ValidateHourSeal(root string, sourceHour, now time.Time, freshness time.Duration) (HourSeal, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	var persisted HourSeal
	err := withLedgerLock(root, func() error {
		if err := readJSON(sealPath(root, sourceHour), &persisted); err != nil {
			return fmt.Errorf("%w: seal unavailable: %v", ErrHourUnsealed, err)
		}
		state, err := loadState(root)
		if err != nil {
			return err
		}
		current, err := evaluateHour(state, sourceHour, now.UTC(), freshness)
		if err != nil {
			return err
		}
		if persisted.SourceHour != current.SourceHour || persisted.StateDigest != current.StateDigest {
			return fmt.Errorf("%w: ledger state changed after seal", ErrHourUnsealed)
		}
		return nil
	})
	return persisted, err
}

func (l *Ledger) Health() (Health, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var health Health
	err := withLedgerLock(l.root, func() error {
		state, err := loadState(l.root)
		if err != nil {
			return err
		}
		health = evaluateHealth(state, l.now().UTC(), DefaultFreshness)
		return nil
	})
	return health, err
}

func (l *Ledger) mutateRuntime(update func(*RuntimeReceipt, time.Time) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	observedAt := l.now().UTC()
	l.ensureHours(l.receipt.LastSnapshotAt, observedAt)
	if err := update(&l.receipt, observedAt); err != nil {
		return err
	}
	l.receipt.LastSnapshotAt = observedAt
	if err := l.persistRuntime(); err != nil {
		l.markUnsealable(observedAt, "ledger_write_failed")
		return err
	}
	return nil
}

func (l *Ledger) ensureHours(from, until time.Time) {
	if l.receipt.Hours == nil {
		l.receipt.Hours = map[string]*HourCounters{}
	}
	start := from.UTC().Truncate(time.Hour)
	end := until.UTC().Truncate(time.Hour)
	for hour := start; !hour.After(end); hour = hour.Add(time.Hour) {
		key := hourKey(hour)
		if l.receipt.Hours[key] == nil {
			l.receipt.Hours[key] = &HourCounters{}
		}
	}
}

func (r *RuntimeReceipt) hour(sourceHour time.Time) (*HourCounters, error) {
	key := hourKey(sourceHour)
	counters := r.Hours[key]
	if counters == nil {
		return nil, errors.New("qa capture identity is outside the runtime ledger window")
	}
	return counters, nil
}

func (l *Ledger) markUnsealable(observedAt time.Time, reason string) {
	if l.receipt.UnsealableSince == nil {
		l.receipt.UnsealableSince = timePointer(observedAt)
		l.receipt.UnsealableReason = reason
	}
	l.receipt.TransitionClean = false
}

func (l *Ledger) persistRuntime() error {
	body, err := json.MarshalIndent(l.receipt, "", "  ")
	if err != nil {
		return err
	}
	return withLedgerLock(l.root, func() error {
		return l.writeAtomic(l.runtimePath, append(body, '\n'))
	})
}

func (l *Ledger) persistFailure(failure FailureReceipt) error {
	return l.persistJSON(filepath.Join(l.root, "failures", failure.FailureID+".json"), failure)
}

func (l *Ledger) recordStaleRuntimes() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	observedAt := l.now().UTC()
	return withLedgerLock(l.root, func() error {
		state, err := loadState(l.root)
		if err != nil {
			return err
		}
		for _, runtime := range state.runtimes {
			if runtime.RuntimeID == l.receipt.RuntimeID || runtime.Drained || observedAt.Sub(runtime.LastSnapshotAt) <= DefaultFreshness {
				continue
			}
			failure := FailureReceipt{
				Version:       ledgerVersion,
				FailureID:     discontinuityFailureID(runtime),
				SourceHour:    runtime.LastSnapshotAt.UTC().Truncate(time.Hour),
				Stage:         "runtime_discontinuity",
				OccurredAt:    observedAt,
				IntervalStart: timePointer(runtime.LastSnapshotAt),
				IntervalEnd:   timePointer(observedAt),
				RuntimeID:     runtime.RuntimeID,
				Status:        "unresolved",
			}
			path := filepath.Join(l.root, "failures", failure.FailureID+".json")
			if _, err := os.Stat(path); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := l.persistJSONUnlocked(path, failure); err != nil {
				l.markUnsealable(observedAt, "ledger_write_failed")
				return err
			}
		}
		return nil
	})
}

func (l *Ledger) persistJSON(path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return withLedgerLock(l.root, func() error {
		return l.writeAtomic(path, append(body, '\n'))
	})
}

func (l *Ledger) persistJSONUnlocked(path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return l.writeAtomic(path, append(body, '\n'))
}

type ledgerState struct {
	runtimes []RuntimeReceipt
	failures []FailureReceipt
}

func loadState(root string) (ledgerState, error) {
	var state ledgerState
	if err := loadJSONDirectory(filepath.Join(root, "runtimes"), &state.runtimes); err != nil {
		return state, err
	}
	if err := loadJSONDirectory(filepath.Join(root, "failures"), &state.failures); err != nil {
		return state, err
	}
	return state, nil
}

func loadJSONDirectory[T any](dir string, out *[]T) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read qa capture ledger directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var item T
		if err := readJSON(filepath.Join(dir, entry.Name()), &item); err != nil {
			return fmt.Errorf("read qa capture ledger receipt %s: %w", entry.Name(), err)
		}
		*out = append(*out, item)
	}
	return nil
}

func evaluateHour(state ledgerState, sourceHour, now time.Time, freshness time.Duration) (HourSeal, error) {
	sourceHour = sourceHour.UTC().Truncate(time.Hour)
	hourEnd := sourceHour.Add(time.Hour)
	if now.Before(hourEnd) {
		return HourSeal{}, fmt.Errorf("%w: source hour is still open", ErrHourUnsealed)
	}
	if freshness <= 0 {
		freshness = DefaultFreshness
	}

	relevant := make([]RuntimeReceipt, 0, len(state.runtimes))
	for _, runtime := range state.runtimes {
		if !runtimeIntersects(runtime, sourceHour, hourEnd) {
			continue
		}
		relevant = append(relevant, runtime)
		counters := runtime.Hours[hourKey(sourceHour)]
		if counters == nil || counters.Pending != 0 || counters.Inflight != 0 {
			return HourSeal{}, fmt.Errorf("%w: runtime %s is not drained for source hour", ErrHourUnsealed, runtime.RuntimeID)
		}
		if (!runtime.TransitionClean && runtime.UnsealableSince == nil) ||
			(runtime.UnsealableSince != nil && runtime.UnsealableSince.Before(hourEnd)) {
			return HourSeal{}, fmt.Errorf("%w: runtime %s is unsealable", ErrHourUnsealed, runtime.RuntimeID)
		}
		requiredSnapshotAt := hourEnd
		if runtime.DrainedAt != nil && runtime.DrainedAt.Before(hourEnd) {
			requiredSnapshotAt = *runtime.DrainedAt
		}
		if runtime.LastSnapshotAt.Before(requiredSnapshotAt) {
			return HourSeal{}, fmt.Errorf("%w: runtime %s has no post-hour snapshot", ErrHourUnsealed, runtime.RuntimeID)
		}
		if !runtime.Drained && now.Sub(runtime.LastSnapshotAt) > freshness {
			return HourSeal{}, fmt.Errorf("%w: runtime %s receipt is stale", ErrHourUnsealed, runtime.RuntimeID)
		}
	}
	if len(relevant) == 0 {
		return HourSeal{}, fmt.Errorf("%w: no runtime receipt intersects source hour", ErrHourUnsealed)
	}
	for _, failure := range state.failures {
		if failure.Status == "unresolved" && failureIntersectsHour(failure, sourceHour, hourEnd) {
			return HourSeal{}, fmt.Errorf("%w: unresolved capture failure %s", ErrHourUnsealed, failure.FailureID)
		}
	}

	sort.Slice(relevant, func(i, j int) bool { return relevant[i].RuntimeID < relevant[j].RuntimeID })
	runtimeIDs := make([]string, 0, len(relevant))
	for _, runtime := range relevant {
		runtimeIDs = append(runtimeIDs, runtime.RuntimeID)
	}
	digest, err := stateDigest(sourceHour, relevant, state.failures)
	if err != nil {
		return HourSeal{}, err
	}
	return HourSeal{
		Version:     ledgerVersion,
		SourceHour:  sourceHour,
		SealedAt:    now.UTC(),
		RuntimeIDs:  runtimeIDs,
		StateDigest: digest,
	}, nil
}

func evaluateHealth(state ledgerState, now time.Time, freshness time.Duration) Health {
	health := Health{Status: HealthHealthy, ObservedAt: now}
	degraded := false
	stalled := false
	for _, runtime := range state.runtimes {
		if runtime.UnsealableSince != nil || !runtime.TransitionClean {
			health.Status = HealthFailed
			health.Reason = "ledger_write_failed"
		}
		for _, counters := range runtime.Hours {
			health.Pending += counters.Pending
			health.Inflight += counters.Inflight
			degraded = degraded || counters.Degraded > 0
			if counters.BacklogSince != nil && now.Sub(*counters.BacklogSince) >= freshness {
				stalled = true
			}
		}
	}
	recoveryObservation := false
	unresolvedReason := ""
	for _, failure := range state.failures {
		if failure.Status == "unresolved" {
			health.UnresolvedFailures++
			if failure.Stage == "runtime_discontinuity" {
				unresolvedReason = "runtime_discontinuity"
			} else if unresolvedReason == "" {
				unresolvedReason = "persist_failed"
			}
		} else if failure.Status == "recovered" && failure.RecoveredAt != nil && now.Sub(*failure.RecoveredAt) < freshness {
			recoveryObservation = true
		}
	}
	if health.UnresolvedFailures > 0 {
		health.Status = HealthFailed
		health.Reason = unresolvedReason
		return health
	}
	if health.Status == HealthFailed {
		return health
	}
	if stalled {
		health.Status = HealthFailed
		health.Reason = "capture_stalled"
		return health
	}
	if recoveryObservation {
		health.Status = HealthFailed
		health.Reason = "recovery_observation"
		return health
	}
	if degraded {
		health.Status = HealthDegraded
		health.Reason = "evidence_dlq"
	}
	return health
}

func stateDigest(sourceHour time.Time, runtimes []RuntimeReceipt, failures []FailureReceipt) (string, error) {
	type runtimeHourProjection struct {
		RuntimeID        string        `json:"runtime_id"`
		StartedAt        time.Time     `json:"started_at"`
		Counters         *HourCounters `json:"counters"`
		UnsealableSince  *time.Time    `json:"unsealable_since,omitempty"`
		UnsealableReason string        `json:"unsealable_reason,omitempty"`
	}
	projections := make([]runtimeHourProjection, 0, len(runtimes))
	for _, runtime := range runtimes {
		projection := runtimeHourProjection{
			RuntimeID: runtime.RuntimeID,
			StartedAt: runtime.StartedAt,
			Counters:  runtime.Hours[hourKey(sourceHour)],
		}
		if runtime.UnsealableSince != nil && runtime.UnsealableSince.Before(sourceHour.Add(time.Hour)) {
			projection.UnsealableSince = runtime.UnsealableSince
			projection.UnsealableReason = runtime.UnsealableReason
		}
		projections = append(projections, projection)
	}
	relevantFailures := make([]FailureReceipt, 0, len(failures))
	for _, failure := range failures {
		if failureIntersectsHour(failure, sourceHour, sourceHour.Add(time.Hour)) {
			relevantFailures = append(relevantFailures, failure)
		}
	}
	sort.Slice(relevantFailures, func(i, j int) bool { return relevantFailures[i].FailureID < relevantFailures[j].FailureID })
	body, err := json.Marshal(struct {
		SourceHour time.Time               `json:"source_hour"`
		Runtimes   []runtimeHourProjection `json:"runtimes"`
		Failures   []FailureReceipt        `json:"failures"`
	}{sourceHour, projections, relevantFailures})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func runtimeIntersects(runtime RuntimeReceipt, start, end time.Time) bool {
	if !runtime.StartedAt.Before(end) {
		return false
	}
	return runtime.DrainedAt == nil || runtime.DrainedAt.After(start)
}

func failureID(identity CaptureIdentity) string {
	normalized := normalizedIdentity(identity)
	sum := sha256.Sum256([]byte(normalized.RequestID + "\n" + normalized.CapturedAt.Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}

func discontinuityFailureID(runtime RuntimeReceipt) string {
	sum := sha256.Sum256([]byte("runtime_discontinuity\n" + runtime.RuntimeID + "\n" + runtime.LastSnapshotAt.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}

func failureIntersectsHour(failure FailureReceipt, start, end time.Time) bool {
	if failure.Stage != "runtime_discontinuity" || failure.IntervalStart == nil || failure.IntervalEnd == nil {
		return failure.SourceHour.Equal(start)
	}
	return failure.IntervalStart.Before(end) && failure.IntervalEnd.After(start)
}

func normalizedIdentity(identity CaptureIdentity) CaptureIdentity {
	identity.RequestID = strings.TrimSpace(identity.RequestID)
	identity.CapturedAt = identity.CapturedAt.UTC()
	return identity
}

func hourKey(hour time.Time) string {
	return hour.UTC().Truncate(time.Hour).Format("20060102T15")
}

func sealPath(root string, hour time.Time) string {
	return filepath.Join(root, "seals", hourKey(hour)+".json")
}

func timePointer(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func safeName(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func withLedgerLock(root string, fn func() error) error {
	lock, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open qa capture ledger lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock qa capture ledger: %w", err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	return fn()
}

func writeFileAtomic(path string, body []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".receipt-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func readJSON(path string, out any) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return err
	}
	return nil
}
