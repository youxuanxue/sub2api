package main

import (
	"context"
	"io"
	"testing"
)

func TestRejectUnconfirmedOrUnknownPhase(t *testing.T) {
	for _, args := range [][]string{nil, {"--phase=archive"}, {"--phase=provision"}, {"--phase=provision", "--confirm=wrong"}, {"--phase=provision", "--confirm=tokenkey-qa-local-provision-v1", "extra"}} {
		if err := run(context.Background(), args, io.Discard); err == nil {
			t.Fatalf("unsafe command accepted: %v", args)
		}
	}
}
