package storage

import (
	"context"
	"errors"
	"time"
)

// Observer records the outcome and latency of a single storage operation.
// It is injected from the caller so this package stays free of a metrics dependency.
type Observer interface {
	Observe(backend, namespace, operation, outcome string, d time.Duration)
}

// WithMetrics wraps s so each operation reports to obs under the given backend and
// namespace. It returns s unchanged when obs is nil, so a disabled observer adds no
// overhead to the hot path.
func WithMetrics[T any](s Storage[T], obs Observer, backend, namespace string) Storage[T] {
	if obs == nil {
		return s
	}
	return &instrumentedStorage[T]{inner: s, obs: obs, backend: backend, namespace: namespace}
}

type instrumentedStorage[T any] struct {
	inner     Storage[T]
	obs       Observer
	backend   string
	namespace string
}

var _ Storage[any] = (*instrumentedStorage[any])(nil)

func (s *instrumentedStorage[T]) Set(ctx context.Context, key string, item T) error {
	start := time.Now()
	err := s.inner.Set(ctx, key, item)
	s.obs.Observe(s.backend, s.namespace, "set", outcome(err), time.Since(start))
	return err
}

func (s *instrumentedStorage[T]) SetWithTTL(ctx context.Context, key string, item T, expiration time.Duration) error {
	start := time.Now()
	err := s.inner.SetWithTTL(ctx, key, item, expiration)
	s.obs.Observe(s.backend, s.namespace, "set_with_ttl", outcome(err), time.Since(start))
	return err
}

func (s *instrumentedStorage[T]) Get(ctx context.Context, key string) (T, error) {
	start := time.Now()
	item, err := s.inner.Get(ctx, key)
	s.obs.Observe(s.backend, s.namespace, "get", outcome(err), time.Since(start))
	return item, err
}

func (s *instrumentedStorage[T]) Remove(ctx context.Context, key string) error {
	start := time.Now()
	err := s.inner.Remove(ctx, key)
	s.obs.Observe(s.backend, s.namespace, "remove", outcome(err), time.Since(start))
	return err
}

// outcome classifies an operation result, keeping the normal control-flow errors
// (missing key, empty queue) out of the error bucket.
func outcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrEmptyQueue):
		return "empty_queue"
	default:
		return "error"
	}
}
