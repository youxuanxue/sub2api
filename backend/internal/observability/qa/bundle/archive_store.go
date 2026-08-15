package bundle

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
)

type ArchiveStore struct {
	inner archive.ObjectStore
}

func NewArchiveStore(inner archive.ObjectStore) *ArchiveStore {
	return &ArchiveStore{inner: inner}
}

func (s *ArchiveStore) Create(ctx context.Context, key string, body io.Reader, size int64, metadata ObjectMetadata) error {
	if s == nil || s.inner == nil {
		return errors.New("qa bundle archive store is required")
	}
	_, err := s.inner.CreateWithOptions(ctx, key, body, size, archive.ObjectWriteOptions{
		ContentType: metadata.ContentType, ContentEncoding: metadata.ContentEncoding,
	})
	if errors.Is(err, archive.ErrPreconditionFailed) {
		return fmt.Errorf("%w: %s", ErrObjectExists, key)
	}
	return err
}

func (s *ArchiveStore) Read(ctx context.Context, key string) ([]byte, error) {
	if s == nil || s.inner == nil {
		return nil, errors.New("qa bundle archive store is required")
	}
	object, err := s.inner.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = object.Body.Close() }()
	body, err := io.ReadAll(object.Body)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) != object.Info.Size {
		return nil, errors.New("qa bundle object size mismatch")
	}
	return body, nil
}

func (s *ArchiveStore) Head(ctx context.Context, key string) (bool, error) {
	if s == nil || s.inner == nil {
		return false, errors.New("qa bundle archive store is required")
	}
	return s.inner.Head(ctx, key)
}
