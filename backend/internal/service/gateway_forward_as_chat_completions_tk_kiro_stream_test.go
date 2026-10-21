//go:build unit

package service

import (
	"io"
	"testing"
	"time"
)

func TestKiroLiveBridgeWriterForwardsBeforeClose(t *testing.T) {
	reader, pipe := io.Pipe()
	writer := newKiroLiveBridgeWriter(pipe)
	writeDone := make(chan error, 1)
	go func() {
		_, err := writer.Write([]byte("event: message_start\n\n"))
		writeDone <- err
	}()

	data := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := reader.Read(buf)
		data <- buf[:n]
	}()

	select {
	case got := <-data:
		if string(got) != "event: message_start\n\n" {
			t.Fatalf("bridge delivered %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not deliver the first SSE frame before stream close")
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("bridge write failed: %v", err)
	}
	_ = writer.closeWithError(nil)
}
