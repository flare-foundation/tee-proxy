package testutil

import (
	"context"
	"sync"
	"time"

	"github.com/flare-foundation/tee-proxy/pkg/storage"
)

// MemStorage is an in-memory implementation of storage.Storage[T] for use in tests.
type MemStorage[T any] struct {
	mu sync.Mutex
	m  map[string]T
}

// NewMemStorage creates a new empty MemStorage[T].
func NewMemStorage[T any]() *MemStorage[T] {
	return &MemStorage[T]{m: make(map[string]T)}
}

func (k *MemStorage[T]) Set(_ context.Context, key string, item T) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = item
	return nil
}

func (k *MemStorage[T]) SetWithTTL(ctx context.Context, key string, item T, _ time.Duration) error {
	return k.Set(ctx, key, item)
}

func (k *MemStorage[T]) Get(_ context.Context, key string) (T, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[key]
	if !ok {
		var zero T
		return zero, storage.ErrNotFound
	}
	return v, nil
}

func (k *MemStorage[T]) Remove(_ context.Context, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, key)
	return nil
}
