package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

type Queue[T any] interface {
	Enqueue(ctx context.Context, item T) error
	Dequeue(ctx context.Context) (T, error)
	QueueLength(ctx context.Context) (int64, error)
}

// NewQueue creates a new Storage with the Redis client and storing key prefix that is used as a queue.
func NewQueue[T any](keyPrefix string, client *redis.Client) Queue[T] {
	return &Storage[T]{
		client:    client,
		keyPrefix: keyPrefix,
	}
}

// Storage is a storage backed by Redis.
// All storing keys are prefixed with keyPrefix-.
type Storage[T any] struct {
	client    *redis.Client
	keyPrefix string
}

// NewClient creates a new Redis client connected to the given host.
func NewClient(host string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr: host})
}

// New creates a new Storage with the Redis client and storing key prefix.
func New[T any](keyPrefix string, client *redis.Client) *Storage[T] {
	return &Storage[T]{
		client:    client,
		keyPrefix: keyPrefix,
	}
}

func (s *Storage[T]) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

var ErrEmptyKey = errors.New("empty key")

// Set stores the item with the key without expiration.
func (s *Storage[T]) Set(ctx context.Context, key string, item T) error {
	if key == "" {
		return ErrEmptyKey
	}

	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, s.prefix(key), data, 0).Err()
}

// SetWithTTL stores the item with the key and expiration.
func (s *Storage[T]) SetWithTTL(ctx context.Context, key string, item T, expiration time.Duration) error {
	if key == "" {
		return ErrEmptyKey
	}

	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, s.prefix(key), data, expiration).Err()
}

// Get retrieves the value by the key.
func (s *Storage[T]) Get(ctx context.Context, key string) (T, error) {
	var value T
	data, err := s.client.Get(ctx, s.prefix(key)).Bytes()
	if err != nil {
		return value, err
	}

	err = json.Unmarshal(data, &value)
	return value, err
}

// GetWithTTL retrieves the value and its remaining expiration duration by the key.
func (s *Storage[T]) GetWithTTL(ctx context.Context, key string) (T, time.Duration, error) {
	var value T
	data, err := s.client.Get(ctx, s.prefix(key)).Bytes()

	if err != nil {
		return value, 0, err
	}

	err = json.Unmarshal(data, &value)
	if err != nil {
		return value, 0, err
	}

	ttl, err := s.client.TTL(ctx, s.prefix(key)).Result()
	return value, ttl, err
}

// Remove deletes the value stored for the key.
func (s *Storage[T]) Remove(ctx context.Context, key string) error {
	return s.client.Del(ctx, s.prefix(key)).Err()
}

// Enqueue enqueues an item to the queue.
func (s *Storage[T]) Enqueue(ctx context.Context, item T) error {
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return s.client.LPush(ctx, s.prefix(""), data).Err()
}

var ErrEmptyQueue = errors.New("empty queue")

// Dequeue dequeues an item from the queue. If no item is available, ErrEmptyQueue error is returned.
func (s *Storage[T]) Dequeue(ctx context.Context) (T, error) {
	var t T

	data, err := s.client.RPop(ctx, s.prefix("")).Bytes()
	if errors.Is(err, redis.Nil) {
		return t, ErrEmptyQueue
	}
	if err != nil {
		return t, err
	}
	err = json.Unmarshal(data, &t)
	return t, err
}

func (s *Storage[T]) QueueLength(ctx context.Context) (int64, error) {
	return s.client.LLen(ctx, s.prefix("")).Result()
}

// Clear deletes all storage's entries from database.
func (s *Storage[T]) Clear(ctx context.Context) error {
	keys, err := s.client.Keys(ctx, s.prefix("*")).Result()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	return s.client.Del(ctx, keys...).Err()
}

// prefix prefixes key with keyPrefix-.
func (s *Storage[T]) prefix(key string) string {
	return s.keyPrefix + "-" + key
}
