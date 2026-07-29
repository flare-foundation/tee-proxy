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
	preExpired := ctx.Err() != nil
	err := s.inner.Set(ctx, key, item)
	s.obs.Observe(s.backend, s.namespace, "set", outcome(err, preExpired), time.Since(start))
	return err
}

func (s *instrumentedStorage[T]) SetWithTTL(ctx context.Context, key string, item T, expiration time.Duration) error {
	start := time.Now()
	preExpired := ctx.Err() != nil
	err := s.inner.SetWithTTL(ctx, key, item, expiration)
	s.obs.Observe(s.backend, s.namespace, "set_with_ttl", outcome(err, preExpired), time.Since(start))
	return err
}

func (s *instrumentedStorage[T]) Get(ctx context.Context, key string) (T, error) {
	start := time.Now()
	preExpired := ctx.Err() != nil
	item, err := s.inner.Get(ctx, key)
	s.obs.Observe(s.backend, s.namespace, "get", outcome(err, preExpired), time.Since(start))
	return item, err
}

func (s *instrumentedStorage[T]) Remove(ctx context.Context, key string) error {
	start := time.Now()
	preExpired := ctx.Err() != nil
	err := s.inner.Remove(ctx, key)
	s.obs.Observe(s.backend, s.namespace, "remove", outcome(err, preExpired), time.Since(start))
	return err
}

// outcome classifies an operation result, normalizing a missing key to not_found. Caller-side
// cancellation — context.Canceled, or a deadline already expired before the operation started
// (preExpired, e.g. the post-timeout "hopeful" result fetch) — is cancelled. A deadline the
// operation consumed itself is timeout: a hung backend under a retrying client (GCS RetryAlways)
// only ever fails this way, so the storage-error alerts must see it.
func outcome(err error, preExpired bool) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		if preExpired {
			return "cancelled"
		}
		return "timeout"
	default:
		return "error"
	}
}
