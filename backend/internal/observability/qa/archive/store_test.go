//go:build unit

package archive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
)

func TestArchiveTransferOptionsAreBounded(t *testing.T) {
	var options transfermanager.Options
	boundedTransferOptions(&options)
	if options.Concurrency != 1 || options.PartSizeBytes != archiveMultipartPartSize ||
		options.MultipartUploadThreshold != archiveMultipartPartSize {
		t.Fatalf("transfer options=%+v", options)
	}
}

func TestMemoryObjectStoreConditionalReaderContract(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryObjectStore()

	created, err := store.Create(ctx, "hour/commit.json", bytes.NewReader([]byte("v1")), 2, "application/json")
	if err != nil {
		t.Fatalf("Create()=%v", err)
	}
	if created.ETag == "" || created.Size != 2 {
		t.Fatalf("created=%+v", created)
	}
	if _, err := store.Create(ctx, "hour/commit.json", bytes.NewReader([]byte("other")), 5, "application/json"); !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("second Create() error=%v, want ErrPreconditionFailed", err)
	}

	opened, err := store.Open(ctx, "hour/commit.json")
	if err != nil {
		t.Fatalf("Open()=%v", err)
	}
	body, err := io.ReadAll(opened.Body)
	if err != nil {
		t.Fatalf("ReadAll()=%v", err)
	}
	if err := opened.Body.Close(); err != nil {
		t.Fatalf("Close()=%v", err)
	}
	if string(body) != "v1" || opened.Info != created {
		t.Fatalf("opened body=%q info=%+v, want v1 %+v", body, opened.Info, created)
	}

	if _, err := store.CompareAndSwap(ctx, "hour/commit.json", "stale", bytes.NewReader([]byte("v2")), 2, "application/json"); !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("stale CompareAndSwap() error=%v, want ErrPreconditionFailed", err)
	}
	updated, err := store.CompareAndSwap(ctx, "hour/commit.json", created.ETag, bytes.NewReader([]byte("v2")), 2, "application/json")
	if err != nil {
		t.Fatalf("CompareAndSwap()=%v", err)
	}
	if updated.ETag == created.ETag || updated.Size != 2 {
		t.Fatalf("updated=%+v created=%+v", updated, created)
	}
	got, err := store.Get(ctx, "hour/commit.json")
	if err != nil || string(got) != "v2" {
		t.Fatalf("Get() body=%q err=%v", got, err)
	}
}

func TestMemoryObjectStorePutReaderRejectsWrongSize(t *testing.T) {
	store := NewMemoryObjectStore()
	_, err := store.PutReader(context.Background(), "artifact", bytes.NewReader([]byte("abc")), 4, "application/octet-stream")
	if err == nil {
		t.Fatal("PutReader() accepted a body shorter than its declared size")
	}
	exists, headErr := store.Head(context.Background(), "artifact")
	if headErr != nil || exists {
		t.Fatalf("partial object persisted: exists=%v err=%v", exists, headErr)
	}
}
