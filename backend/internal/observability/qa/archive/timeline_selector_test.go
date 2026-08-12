//go:build unit

package archive

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

type fakeCatchupTimelineControl struct {
	cutover    ForwardCutover
	hours      map[time.Time]CatchupHourStatus
	inspected  []time.Time
	classified []time.Time
}

func (f *fakeCatchupTimelineControl) ReadForwardCutover(context.Context, *sql.Conn) (ForwardCutover, bool, error) {
	return f.cutover, true, nil
}

func (f *fakeCatchupTimelineControl) InspectCatchupHour(_ context.Context, _ *sql.Conn, window Window) (CatchupHourStatus, error) {
	f.inspected = append(f.inspected, window.Start)
	return f.hours[window.Start], nil
}

func (f *fakeCatchupTimelineControl) MarkSourceUnavailableAfterRetention(_ context.Context, _ *sql.Conn, window Window) (int64, error) {
	f.classified = append(f.classified, window.Start)
	status := f.hours[window.Start]
	if status.ShardID == 0 {
		status.ShardID = int64(len(f.classified))
	}
	status.Exists = true
	status.State = StateFailed
	status.VerificationErrorCode = IntegritySourceUnavailableAfterRetention
	f.hours[window.Start] = status
	return status.ShardID, nil
}

func TestUS045_TimelineCompensationEnumeratesMissingHoursAfterCutover(t *testing.T) {
	cutover := Phase2ForwardCutoverWindow()
	normal := Window{Start: cutover.Start.Add(5 * time.Hour), End: cutover.Start.Add(6 * time.Hour)}
	terminal := cutover.End
	complete := terminal.Add(time.Hour)
	missing := complete.Add(time.Hour)
	control := &fakeCatchupTimelineControl{
		cutover: ForwardCutover{ShardID: 45, Window: cutover, RestoreVerifiedAt: cutover.End},
		hours: map[time.Time]CatchupHourStatus{
			terminal: {
				Exists: true, State: StateFailed,
				VerificationErrorCode: IntegritySourceUnavailableAfterRetention,
			},
			complete: {
				Exists: true, State: StateCommitted, RestoreVerified: true,
			},
		},
	}

	selection, ok, err := SelectOldestCatchup(
		context.Background(), nil, control, normal, complete.Add(30*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || selection.Window.Start != missing || selection.Disposition != CatchupDispositionReconcile {
		t.Fatalf("selection=%+v ok=%v", selection, ok)
	}
	wantInspected := []time.Time{terminal, complete, missing}
	if len(control.inspected) != len(wantInspected) {
		t.Fatalf("inspected=%v", control.inspected)
	}
	for index := range wantInspected {
		if !control.inspected[index].Equal(wantInspected[index]) {
			t.Fatalf("inspected=%v", control.inspected)
		}
	}
	if len(control.classified) != 0 {
		t.Fatalf("timely missing hour was terminalized: %v", control.classified)
	}
}

func TestUS045_TimelineCompensationClassifiesExpiredMissingHourWithoutStarvingNext(t *testing.T) {
	cutover := Phase2ForwardCutoverWindow()
	first := cutover.End
	second := first.Add(time.Hour)
	normal := Window{Start: second.Add(time.Hour), End: second.Add(2 * time.Hour)}
	control := &fakeCatchupTimelineControl{
		cutover: ForwardCutover{ShardID: 45, Window: cutover, RestoreVerifiedAt: cutover.End},
		hours: map[time.Time]CatchupHourStatus{
			second: {Exists: true, ShardID: 47, State: StatePending, SourceExists: true, UncoveredSourceExists: true},
		},
	}

	selection, ok, err := SelectOldestCatchup(
		context.Background(), nil, control, normal, first.Add(30*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || selection.Window.Start != first || selection.Disposition != CatchupDispositionSourceUnavailableAfterRetention {
		t.Fatalf("first selection=%+v ok=%v", selection, ok)
	}
	if len(control.classified) != 1 || !control.classified[0].Equal(first) {
		t.Fatalf("classified=%v", control.classified)
	}

	selection, ok, err = SelectOldestCatchup(
		context.Background(), nil, control, normal, first.Add(30*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || selection.Window.Start != second || selection.Disposition != CatchupDispositionReconcile {
		t.Fatalf("second selection=%+v ok=%v", selection, ok)
	}
}

func TestTimelineCompensationKeepsUnknownCommitExistenceRetryableAfterRetention(t *testing.T) {
	cutover := Phase2ForwardCutoverWindow()
	unknown := cutover.End
	normal := Window{Start: unknown.Add(time.Hour), End: unknown.Add(2 * time.Hour)}
	control := &fakeCatchupTimelineControl{
		cutover: ForwardCutover{ShardID: 45, Window: cutover, RestoreVerifiedAt: cutover.End},
		hours: map[time.Time]CatchupHourStatus{
			unknown: {
				Exists: true, ShardID: 46, State: StateFailed,
				VerificationErrorCode: IntegrityCommitExistenceUnknown,
			},
		},
	}

	selection, ok, err := SelectOldestCatchup(
		context.Background(), nil, control, normal, unknown.Add(30*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || selection.Window.Start != unknown || selection.ShardID != 46 || selection.Disposition != CatchupDispositionReconcile {
		t.Fatalf("selection=%+v ok=%v", selection, ok)
	}
	if len(control.classified) != 0 {
		t.Fatalf("unknown commit existence was terminalized: %v", control.classified)
	}
}

func TestUS045_TimelineCompensationSelectsUncoveredLateIdentityUntilConverged(t *testing.T) {
	cutover := Phase2ForwardCutoverWindow()
	late := cutover.End
	complete := late.Add(time.Hour)
	normal := Window{Start: complete.Add(time.Hour), End: complete.Add(2 * time.Hour)}
	control := &fakeCatchupTimelineControl{
		cutover: ForwardCutover{ShardID: 45, Window: cutover, RestoreVerifiedAt: cutover.End},
		hours: map[time.Time]CatchupHourStatus{
			late: {
				Exists: true, ShardID: 46, State: StateCommitted, RestoreVerified: true,
				SourceExists: true, UncoveredSourceExists: true,
			},
			complete: {Exists: true, ShardID: 47, State: StateCommitted, RestoreVerified: true},
		},
	}

	selection, ok, err := SelectOldestCatchup(context.Background(), nil, control, normal, cutover.Start)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || selection.Window.Start != late || selection.ShardID != 46 {
		t.Fatalf("selection=%+v ok=%v", selection, ok)
	}
	status := control.hours[late]
	status.UncoveredSourceExists = false
	control.hours[late] = status

	_, ok, err = SelectOldestCatchup(context.Background(), nil, control, normal, cutover.Start)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("converged membership still qualified for compensation")
	}
}
