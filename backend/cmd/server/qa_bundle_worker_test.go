//go:build unit

package main

import "testing"

func TestQABundleWorkerRequested(t *testing.T) {
	if !qaBundleWorkerRequested([]string{"--qa-bundle-worker"}) ||
		!qaBundleWorkerRequested([]string{"--qa-bundle-worker-once"}) ||
		qaBundleWorkerRequested([]string{"--qa-maintenance-once"}) {
		t.Fatal("qaBundleWorkerRequested() dispatch mismatch")
	}
}
