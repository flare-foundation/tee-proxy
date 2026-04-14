package storage

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Storage implementations when a key does not exist or has expired.
var ErrNotFound = errors.New("not found")

// Storage is a generic key-value store.
type Storage[T any] interface {
	Set(ctx context.Context, key string, item T) error
	SetWithTTL(ctx context.Context, key string, item T, expiration time.Duration) error
	Get(ctx context.Context, key string) (T, error)
	Remove(ctx context.Context, key string) error
}

// Subscription represents an active pub/sub subscription.
type Subscription interface {
	// ReceiveMessage blocks until a message arrives or the context is cancelled.
	ReceiveMessage(ctx context.Context) (string, error)
	// Close closes the subscription.
	Close() error
}

// Notifier provides pub/sub functionality for key-based notifications.
type Notifier interface {
	// Publish sends a notification on the given channel.
	Publish(ctx context.Context, channel string) error
	// Subscribe returns a Subscription for the given channel.
	Subscribe(ctx context.Context, channel string) Subscription
}

// Queue is a generic FIFO queue backed by a persistent store.
type Queue[T any] interface {
	Enqueue(ctx context.Context, item T) error
	Dequeue(ctx context.Context) (T, error)
	QueueLength(ctx context.Context) (int64, error)
}
