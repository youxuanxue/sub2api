package trajectory

import "sync/atomic"

var dlqWrites atomic.Int64

func RecordDLQWrite() {
	dlqWrites.Add(1)
}

func DLQWrites() int64 {
	return dlqWrites.Load()
}
