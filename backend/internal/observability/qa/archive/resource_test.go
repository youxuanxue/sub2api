//go:build unit

package archive

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	resourceFixtureRows    = 32768
	resourceFixturePayload = 16 * 1024
	resourceRSSLimitMiB    = 320
)

func TestBuildSegmentDenseFixtureHasBoundedRSS(t *testing.T) {
	if os.Getenv("QA_ARCHIVE_RESOURCE_CHILD") == "1" {
		runDenseResourceChild(t)
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestBuildSegmentDenseFixtureHasBoundedRSS$", "-test.count=1")
	command.Env = append(os.Environ(), "QA_ARCHIVE_RESOURCE_CHILD=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("resource child failed: %v\n%s", err, output)
	}
}

func runDenseResourceChild(t *testing.T) {
	driverName := "qa-archive-resource"
	sql.Register(driverName, denseFixtureDriver{})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	start := time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)
	built, err := BuildSegment(context.Background(), conn, BuildInput{
		WindowStart: start, WindowEnd: start.Add(time.Hour), SegmentKind: SegmentKindBase,
		BlobRoot: t.TempDir(), ScratchRoot: t.TempDir(), PageSize: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = built.Close() }()
	if built.Manifest.RecordCount != resourceFixtureRows {
		t.Fatalf("record count=%d", built.Manifest.RecordCount)
	}
	peakMiB, err := maxRSSMiB()
	if err != nil {
		t.Fatal(err)
	}
	limitMiB := float64(resourceRSSLimitMiB + raceRSSAllowanceMiB)
	if peakMiB >= limitMiB {
		t.Fatalf("peak RSS %.1f MiB exceeds %.0f MiB for %.1f MiB logical fixture", peakMiB, limitMiB, float64(resourceFixtureRows*resourceFixturePayload)/(1<<20))
	}
}

type denseFixtureDriver struct{}
type denseFixtureConn struct{}
type denseFixtureRows struct {
	index int
	end   int
}

func (denseFixtureDriver) Open(string) (driver.Conn, error)  { return denseFixtureConn{}, nil }
func (denseFixtureConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (denseFixtureConn) Close() error                        { return nil }
func (denseFixtureConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (denseFixtureConn) QueryContext(_ context.Context, _ string, args []driver.NamedValue) (driver.Rows, error) {
	if len(args) != 5 {
		return nil, fmt.Errorf("query args=%d", len(args))
	}
	cursor, _ := args[3].Value.(string)
	index := 0
	if strings.HasPrefix(cursor, "req-") {
		parsed, err := strconv.Atoi(strings.TrimPrefix(cursor, "req-"))
		if err != nil {
			return nil, err
		}
		index = parsed + 1
	}
	limit, ok := args[4].Value.(int64)
	if !ok || limit <= 0 {
		return nil, fmt.Errorf("invalid limit %v", args[4].Value)
	}
	end := index + int(limit)
	if end > resourceFixtureRows {
		end = resourceFixtureRows
	}
	return &denseFixtureRows{index: index, end: end}, nil
}

func (r *denseFixtureRows) Columns() []string { return segmentColumns }
func (r *denseFixtureRows) Close() error      { return nil }
func (r *denseFixtureRows) Next(values []driver.Value) error {
	if r.index >= r.end {
		return io.EOF
	}
	index := r.index
	r.index++
	payload := strings.Repeat("x", resourceFixturePayload)
	createdAt := time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC).Add(time.Duration(index+1) * time.Microsecond)
	row := []driver.Value{
		fmt.Sprintf("req-%08d", index), nil, int64(1), nil, int64(2), nil,
		"anthropic", nil, payload, nil, int64(200), true, int64(10), false,
		int64(1), int64(2), "request-sha", "response-sha",
		nil, nil, nil, nil, "captured", createdAt,
	}
	copy(values, row)
	return nil
}

func maxRSSMiB() (float64, error) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, err
	}
	bytes := float64(usage.Maxrss)
	if runtime.GOOS != "darwin" {
		bytes *= 1024
	}
	return bytes / (1 << 20), nil
}

var _ driver.QueryerContext = denseFixtureConn{}
