//go:build unit

package bundle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
)

func TestArchiveStorePreservesMetadataAndMapsImmutableConflict(t *testing.T) {
	ctx := context.Background()
	inner := archive.NewMemoryObjectStore()
	store := NewArchiveStore(inner)
	body := []byte("compressed-page")
	metadata := ObjectMetadata{ContentType: "application/json", ContentEncoding: "gzip"}

	if err := store.Create(ctx, "bundles/7/11/g/page.json.gz", bytes.NewReader(body), int64(len(body)), metadata); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read(ctx, "bundles/7/11/g/page.json.gz")
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("Read()=%q err=%v", got, err)
	}
	opened, err := store.Open(ctx, "bundles/7/11/g/page.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	streamed, err := io.ReadAll(opened.Body)
	closeErr := opened.Body.Close()
	if err != nil || closeErr != nil || opened.Size != int64(len(body)) || !bytes.Equal(streamed, body) {
		t.Fatalf("Open() size=%d body=%q err=%v closeErr=%v", opened.Size, streamed, err, closeErr)
	}
	storedMetadata, ok := inner.Metadata("bundles/7/11/g/page.json.gz")
	if !ok || storedMetadata.ContentType != metadata.ContentType || storedMetadata.ContentEncoding != metadata.ContentEncoding {
		t.Fatalf("metadata=%+v ok=%v", storedMetadata, ok)
	}
	err = store.Create(ctx, "bundles/7/11/g/page.json.gz", bytes.NewReader(body), int64(len(body)), metadata)
	if !errors.Is(err, ErrObjectExists) {
		t.Fatalf("second Create() error=%v, want ErrObjectExists", err)
	}
}
