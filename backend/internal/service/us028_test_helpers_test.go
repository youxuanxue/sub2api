//go:build unit

package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// us028ReadServiceFile loads a file from this same package directory,
// resolved via the runtime caller info so the test works regardless of the
// CWD the `go test` invocation uses.
func us028ReadServiceFile(t *testing.T, filename string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	path := filepath.Join(dir, filename)
	bs, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(bs)
}

// us028CountSubstring is a tiny substring counter used by the
// "all bridge dispatch sites call the funnel" check.
func us028CountSubstring(s, sub string) int {
	if sub == "" {
		return 0
	}
	count := 0
	for {
		idx := strings.Index(s, sub)
		if idx < 0 {
			break
		}
		count++
		s = s[idx+len(sub):]
	}
	return count
}
