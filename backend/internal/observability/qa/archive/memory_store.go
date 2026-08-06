package archive

import (
	"context"
	"fmt"
	"sync"
)

// MemoryObjectStore is an in-memory ObjectStore for unit tests.
type MemoryObjectStore struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func NewMemoryObjectStore() *MemoryObjectStore {
	return &MemoryObjectStore{objs: map[string][]byte{}}
}

func (m *MemoryObjectStore) Put(_ context.Context, key string, body []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = append([]byte(nil), body...)
	return nil
}

func (m *MemoryObjectStore) PutIfAbsent(_ context.Context, key string, body []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objs[key]; ok {
		return fmt.Errorf("qa archive object already exists: %s", key)
	}
	m.objs[key] = append([]byte(nil), body...)
	return nil
}

func (m *MemoryObjectStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body, ok := m.objs[key]
	if !ok {
		return nil, fmt.Errorf("missing key %s", key)
	}
	return append([]byte(nil), body...), nil
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
	for k := range m.objs {
		out = append(out, k)
	}
	return out
}
