package storage

import (
	"context"
	"errors"
	"fmt"
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
		name       string
		preExpired bool
		err        error
		want       string
	}{
		{"success", false, nil, "success"},
		{"not found", false, ErrNotFound, "not_found"},
		{"cancelled", false, fmt.Errorf("reading gcs object: %w", context.Canceled), "cancelled"},
		// The op consumed its own deadline budget (hung backend) — alertable.
		{"deadline consumed by op", false, fmt.Errorf("committing gcs object: %w", context.DeadlineExceeded), "timeout"},
		// The deadline was spent before the op started (post-timeout hopeful fetch) — caller-side.
		{"deadline expired on entry", true, fmt.Errorf("reading gcs object: %w", context.DeadlineExceeded), "cancelled"},
		{"error", false, errors.New("boom"), "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.preExpired {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Unix(0, 0))
				defer cancel()
			}

			obs := &capturedObs{}
			s := WithMetrics[int](&fakeStorage[int]{err: tt.err}, obs, "redis", "results")

			_, _ = s.Get(ctx, "k")

			require.Equal(t, 1, obs.calls)
			require.Equal(t, "redis", obs.backend)
			require.Equal(t, "results", obs.namespace)
			require.Equal(t, "get", obs.operation)
			require.Equal(t, tt.want, obs.outcome)
		})
	}
}

// TestWithMetricsSetWithTTLSnapshotsEntryContext pins the per-method preExpired snapshot on
// the write path the storage watchdogs actually use (storeResult/createNewBackup write via
// SetWithTTL under a 10s budget).
func TestWithMetricsSetWithTTLSnapshotsEntryContext(t *testing.T) {
	err := fmt.Errorf("committing gcs object: %w", context.DeadlineExceeded)

	obs := &capturedObs{}
	s := WithMetrics[int](&fakeStorage[int]{err: err}, obs, "gcs", "backups")

	require.Error(t, s.SetWithTTL(context.Background(), "k", 1, time.Minute))
	require.Equal(t, "set_with_ttl", obs.operation)
	require.Equal(t, "timeout", obs.outcome, "a deadline consumed by the op must be alertable")

	expired, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	require.Error(t, s.SetWithTTL(expired, "k", 1, time.Minute))
	require.Equal(t, "cancelled", obs.outcome, "a deadline spent before the op is caller-side")
}
