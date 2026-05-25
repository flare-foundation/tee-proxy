package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStorage is a storage backed by Redis.
// All storing keys are prefixed with keyPrefix-.
type RedisStorage[T any] struct {
	client    *redis.Client
	keyPrefix string
}

// NewClient creates a new Redis client connected to the given host.
func NewClient(host string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr: host})
}

// NewRedisStorage creates a new RedisStorage with the Redis client and storing key prefix.
func NewRedisStorage[T any](keyPrefix string, client *redis.Client) *RedisStorage[T] {
	return &RedisStorage[T]{
		client:    client,
		keyPrefix: keyPrefix,
	}
}

// NewNotifier creates a new Notifier backed by Redis.
func NewNotifier(client *redis.Client) Notifier {
	return &RedisStorage[any]{client: client}
}

// NewQueue creates a new RedisStorage with the Redis client and storing key prefix that is used as a queue.
func NewQueue[T any](keyPrefix string, client *redis.Client) Queue[T] {
	return &RedisStorage[T]{
		client:    client,
		keyPrefix: keyPrefix,
	}
}

// ErrEmptyKey is returned by Set/SetWithTTL when called with an empty key, which Redis treats as a no-op.
var ErrEmptyKey = errors.New("empty key")

// Set stores the item with the key without expiration.
func (s *RedisStorage[T]) Set(ctx context.Context, key string, item T) error {
	if key == "" {
		return ErrEmptyKey
	}

	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("marshaling value for redis key %s: %w", s.prefix(key), err)
	}
	if err := s.client.Set(ctx, s.prefix(key), data, 0).Err(); err != nil {
		return fmt.Errorf("setting redis key %s: %w", s.prefix(key), err)
	}
	return nil
}

// SetWithTTL stores the item with the key and expiration.
func (s *RedisStorage[T]) SetWithTTL(ctx context.Context, key string, item T, expiration time.Duration) error {
	if key == "" {
		return ErrEmptyKey
	}

	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("marshaling value for redis key %s: %w", s.prefix(key), err)
	}
	if err := s.client.Set(ctx, s.prefix(key), data, expiration).Err(); err != nil {
		return fmt.Errorf("setting redis key %s: %w", s.prefix(key), err)
	}
	return nil
}

// Publish sends a notification on the given channel.
func (s *RedisStorage[T]) Publish(ctx context.Context, channel string) error {
	if err := s.client.Publish(ctx, channel, "stored").Err(); err != nil {
		return fmt.Errorf("publishing to redis channel %s: %w", channel, err)
	}
	return nil
}

// Subscribe returns a Subscription for the given channel.
func (s *RedisStorage[T]) Subscribe(ctx context.Context, channel string) Subscription {
	return &redisSubscription{ps: s.client.Subscribe(ctx, channel)}
}

type redisSubscription struct {
	ps *redis.PubSub
}

func (r *redisSubscription) ReceiveMessage(ctx context.Context) (string, error) {
	msg, err := r.ps.ReceiveMessage(ctx)
	if err != nil {
		return "", fmt.Errorf("receiving redis pubsub message: %w", err)
	}
	return msg.Payload, nil
}

func (r *redisSubscription) Close() error {
	return r.ps.Close()
}

// Get retrieves the value by the key.
func (s *RedisStorage[T]) Get(ctx context.Context, key string) (T, error) {
	var value T
	data, err := s.client.Get(ctx, s.prefix(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return value, ErrNotFound
	}
	if err != nil {
		return value, fmt.Errorf("getting redis key %s: %w", s.prefix(key), err)
	}

	if err := json.Unmarshal(data, &value); err != nil {
		return value, fmt.Errorf("unmarshaling redis key %s: %w", s.prefix(key), err)
	}
	return value, nil
}

// Remove deletes the value stored for the key.
func (s *RedisStorage[T]) Remove(ctx context.Context, key string) error {
	if err := s.client.Del(ctx, s.prefix(key)).Err(); err != nil {
		return fmt.Errorf("deleting redis key %s: %w", s.prefix(key), err)
	}
	return nil
}

// Enqueue enqueues an item to the queue.
func (s *RedisStorage[T]) Enqueue(ctx context.Context, item T) error {
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("marshaling value for redis queue %s: %w", s.prefix(""), err)
	}
	if err := s.client.LPush(ctx, s.prefix(""), data).Err(); err != nil {
		return fmt.Errorf("pushing to redis queue %s: %w", s.prefix(""), err)
	}
	return nil
}

// ErrEmptyQueue signals that Dequeue had no item to return; callers use it to distinguish empty from error.
var ErrEmptyQueue = errors.New("empty queue")

// Dequeue dequeues an item from the queue. If no item is available, ErrEmptyQueue error is returned.
func (s *RedisStorage[T]) Dequeue(ctx context.Context) (T, error) {
	var t T

	data, err := s.client.RPop(ctx, s.prefix("")).Bytes()
	if errors.Is(err, redis.Nil) {
		return t, ErrEmptyQueue
	}
	if err != nil {
		return t, fmt.Errorf("popping from redis queue %s: %w", s.prefix(""), err)
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return t, fmt.Errorf("unmarshaling popped item from redis queue %s: %w", s.prefix(""), err)
	}
	return t, nil
}

func (s *RedisStorage[T]) QueueLength(ctx context.Context) (int64, error) {
	n, err := s.client.LLen(ctx, s.prefix("")).Result()
	if err != nil {
		return 0, fmt.Errorf("reading redis queue length %s: %w", s.prefix(""), err)
	}
	return n, nil
}

// Clear deletes all entries under this storage's keyPrefix.
func (s *RedisStorage[T]) Clear(ctx context.Context) error {
	keys, err := s.client.Keys(ctx, s.prefix("*")).Result()
	if err != nil {
		return fmt.Errorf("listing redis keys for prefix %s: %w", s.keyPrefix, err)
	}
	if len(keys) == 0 {
		return nil
	}
	if err := s.client.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("deleting redis keys for prefix %s: %w", s.keyPrefix, err)
	}
	return nil
}

// prefix prefixes key with keyPrefix-.
func (s *RedisStorage[T]) prefix(key string) string {
	return s.keyPrefix + "-" + key
}
