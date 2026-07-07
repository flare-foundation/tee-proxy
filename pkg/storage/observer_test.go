package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeStorage is a minimal Storage used to drive the decorator without a backend.
type fakeStorage[T any] struct {
	val T
	err error
}

func (f *fakeStorage[T]) Set(context.Context, string, T) error                       { return f.err }
func (f *fakeStorage[T]) SetWithTTL(context.Context, string, T, time.Duration) error { return f.err }
func (f *fakeStorage[T]) Get(context.Context, string) (T, error)                     { return f.val, f.err }
func (f *fakeStorage[T]) Remove(context.Context, string) error                       { return f.err }

type capturedObs struct {
	backend, namespace, operation, outcome string
	calls                                  int
}

func (c *capturedObs) Observe(backend, namespace, operation, outcome string, _ time.Duration) {
	c.backend, c.namespace, c.operation, c.outcome = backend, namespace, operation, outcome
	c.calls++
}

func TestWithMetricsNilObserverDoesNotWrap(t *testing.T) {
	got := WithMetrics[int](&fakeStorage[int]{}, nil, "redis", "results")

	_, wrapped := got.(*instrumentedStorage[int])
	require.False(t, wrapped, "a nil observer must leave the store unwrapped")
}

func TestWithMetricsRecordsOutcome(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"success", nil, "success"},
		{"not found", ErrNotFound, "not_found"},
		{"empty queue", ErrEmptyQueue, "empty_queue"},
		{"error", errors.New("boom"), "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := &capturedObs{}
			s := WithMetrics[int](&fakeStorage[int]{err: tt.err}, obs, "redis", "results")

			_, _ = s.Get(context.Background(), "k")

			require.Equal(t, 1, obs.calls)
			require.Equal(t, "redis", obs.backend)
			require.Equal(t, "results", obs.namespace)
			require.Equal(t, "get", obs.operation)
			require.Equal(t, tt.want, obs.outcome)
		})
	}
}
