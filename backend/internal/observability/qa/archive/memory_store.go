package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

type memoryObject struct {
	body     []byte
	info     ObjectInfo
	metadata ObjectWriteOptions
}

// MemoryObjectStore is an in-memory ObjectStore for unit tests.
type MemoryObjectStore struct {
	mu   sync.Mutex
	objs map[string]memoryObject
}

func NewMemoryObjectStore() *MemoryObjectStore {
	return &MemoryObjectStore{objs: map[string]memoryObject{}}
}

func readSizedBody(body io.Reader, size int64) ([]byte, error) {
	if size < 0 {
		return nil, fmt.Errorf("qa archive object size must be non-negative")
	}
	data, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, fmt.Errorf("qa archive object size mismatch: declared=%d actual=%d", size, len(data))
	}
	return data, nil
}

func memoryObjectInfo(body []byte) ObjectInfo {
	return ObjectInfo{ETag: SHA256Hex(body), Size: int64(len(body))}
}

func (m *MemoryObjectStore) PutReader(_ context.Context, key string, body io.Reader, size int64, _ string) (ObjectInfo, error) {
	data, err := readSizedBody(body, size)
	if err != nil {
		return ObjectInfo{}, err
	}
	info := memoryObjectInfo(data)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = memoryObject{body: data, info: info}
	return info, nil
}

func (m *MemoryObjectStore) Create(_ context.Context, key string, body io.Reader, size int64, _ string) (ObjectInfo, error) {
	return m.CreateWithOptions(context.Background(), key, body, size, ObjectWriteOptions{})
}

func (m *MemoryObjectStore) CreateWithOptions(_ context.Context, key string, body io.Reader, size int64, options ObjectWriteOptions) (ObjectInfo, error) {
	data, err := readSizedBody(body, size)
	if err != nil {
		return ObjectInfo{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objs[key]; ok {
		return ObjectInfo{}, fmt.Errorf("%w: %s", ErrPreconditionFailed, key)
	}
	info := memoryObjectInfo(data)
	m.objs[key] = memoryObject{body: data, info: info, metadata: options}
	return info, nil
}

func (m *MemoryObjectStore) Metadata(key string) (ObjectWriteOptions, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	object, ok := m.objs[key]
	return object.metadata, ok
}

func (m *MemoryObjectStore) CompareAndSwap(_ context.Context, key, expectedETag string, body io.Reader, size int64, _ string) (ObjectInfo, error) {
	data, err := readSizedBody(body, size)
	if err != nil {
		return ObjectInfo{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.objs[key]
	if !ok || current.info.ETag != expectedETag {
		return ObjectInfo{}, fmt.Errorf("%w: %s", ErrPreconditionFailed, key)
	}
	info := memoryObjectInfo(data)
	m.objs[key] = memoryObject{body: data, info: info}
	return info, nil
}

func (m *MemoryObjectStore) Open(_ context.Context, key string) (ObjectReader, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	object, ok := m.objs[key]
	if !ok {
		return ObjectReader{}, fmt.Errorf("NoSuchKey: missing key %s", key)
	}
	return ObjectReader{
		Info: object.info,
		Body: io.NopCloser(bytes.NewReader(append([]byte(nil), object.body...))),
	}, nil
}

func (m *MemoryObjectStore) HeadInfo(_ context.Context, key string) (ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	object, ok := m.objs[key]
	if !ok {
		return ObjectInfo{}, fmt.Errorf("NoSuchKey: missing key %s", key)
	}
	return object.info, nil
}

func (m *MemoryObjectStore) Put(ctx context.Context, key string, body []byte, contentType string) error {
	_, err := m.PutReader(ctx, key, bytes.NewReader(body), int64(len(body)), contentType)
	return err
}

func (m *MemoryObjectStore) PutIfAbsent(ctx context.Context, key string, body []byte, contentType string) error {
	_, err := m.Create(ctx, key, bytes.NewReader(body), int64(len(body)), contentType)
	if errors.Is(err, ErrPreconditionFailed) {
		return fmt.Errorf("qa archive object already exists: %s", key)
	}
	return err
}

func (m *MemoryObjectStore) Get(ctx context.Context, key string) ([]byte, error) {
	opened, err := m.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = opened.Body.Close() }()
	return io.ReadAll(opened.Body)
}

func (m *MemoryObjectStore) Head(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objs[key]
	return ok, nil
}

func (m *MemoryObjectStore) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.objs))
	for key := range m.objs {
		out = append(out, key)
	}
	return out
}
