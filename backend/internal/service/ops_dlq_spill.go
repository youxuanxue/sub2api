package service

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// opsDLQ spill guards protect small edge hosts from unbounded dead-letter files
// when PostgreSQL inserts fail (e.g. missing ops_error_logs partitions).
const (
	defaultOpsDLQMaxFiles           = 2000
	defaultOpsDLQMaxBytes     int64 = 256 * 1024 * 1024 // 256 MiB
	defaultOpsDLQMaxAge             = 72 * time.Hour
	defaultOpsDLQWritesPerMin       = 120
)

type opsDLQSpillLimits struct {
	maxFiles     int
	maxBytes     int64
	maxAge       time.Duration
	writesPerMin int
}

var (
	opsDLQSpillPolicyOnce sync.Once
	opsDLQSpillLimitsVal  opsDLQSpillLimits

	opsDLQRateMu     sync.Mutex
	opsDLQRateWindow time.Time
	opsDLQRateCount  int
)

func resetOpsDLQSpillForTest() {
	opsDLQSpillPolicyOnce = sync.Once{}
	opsDLQRateMu.Lock()
	opsDLQRateWindow = time.Time{}
	opsDLQRateCount = 0
	opsDLQRateMu.Unlock()
}

func loadOpsDLQSpillLimits() opsDLQSpillLimits {
	opsDLQSpillPolicyOnce.Do(func() {
		opsDLQSpillLimitsVal = opsDLQSpillLimits{
			maxFiles:     envIntPositive("OPS_DLQ_MAX_FILES", defaultOpsDLQMaxFiles),
			maxBytes:     envInt64Positive("OPS_DLQ_MAX_BYTES", defaultOpsDLQMaxBytes),
			maxAge:       envDurationPositive("OPS_DLQ_MAX_AGE", defaultOpsDLQMaxAge),
			writesPerMin: envIntPositive("OPS_DLQ_MAX_WRITES_PER_MIN", defaultOpsDLQWritesPerMin),
		}
	})
	return opsDLQSpillLimitsVal
}

func envIntPositive(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func envInt64Positive(key string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func envDurationPositive(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func opsDLQSpillAllowWrite(now time.Time) bool {
	policy := loadOpsDLQSpillLimits()
	opsDLQRateMu.Lock()
	defer opsDLQRateMu.Unlock()
	window := now.Truncate(time.Minute)
	if opsDLQRateWindow != window {
		opsDLQRateWindow = window
		opsDLQRateCount = 0
	}
	if opsDLQRateCount >= policy.writesPerMin {
		return false
	}
	opsDLQRateCount++
	return true
}

type opsDLQFileInfo struct {
	path    string
	modTime time.Time
	size    int64
}

func pruneOpsDLQDir(dir string, now time.Time, policy opsDLQSpillLimits) (removed int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	files := make([]opsDLQFileInfo, 0, len(entries))
	var totalBytes int64
	cutoff := now.Add(-policy.maxAge)
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".json.zst") {
			continue
		}
		info, infoErr := ent.Info()
		if infoErr != nil {
			continue
		}
		fi := opsDLQFileInfo{
			path:    filepath.Join(dir, name),
			modTime: info.ModTime(),
			size:    info.Size(),
		}
		files = append(files, fi)
		totalBytes += fi.size
		if fi.modTime.Before(cutoff) {
			if rmErr := os.Remove(fi.path); rmErr == nil {
				removed++
				totalBytes -= fi.size
			}
		}
	}
	if len(files)-removed <= policy.maxFiles && totalBytes <= policy.maxBytes {
		return removed, nil
	}
	remaining := make([]opsDLQFileInfo, 0, len(files))
	for _, fi := range files {
		if _, statErr := os.Stat(fi.path); statErr != nil {
			continue
		}
		remaining = append(remaining, fi)
	}
	sort.Slice(remaining, func(i, j int) bool {
		return remaining[i].modTime.Before(remaining[j].modTime)
	})
	for len(remaining) > policy.maxFiles || totalBytes > policy.maxBytes {
		if len(remaining) == 0 {
			break
		}
		oldest := remaining[0]
		remaining = remaining[1:]
		if rmErr := os.Remove(oldest.path); rmErr != nil {
			continue
		}
		removed++
		totalBytes -= oldest.size
	}
	return removed, nil
}

func prepareOpsDLQSpill(dir string, now time.Time) (allowed bool, err error) {
	if !opsDLQSpillAllowWrite(now) {
		return false, nil
	}
	policy := loadOpsDLQSpillLimits()
	if _, err := pruneOpsDLQDir(dir, now, policy); err != nil {
		return false, err
	}
	return true, nil
}
