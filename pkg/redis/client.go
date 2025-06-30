package redis

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

type StorageKeyContext string

const (
	Actions               StorageKeyContext = "QueuedAction"
	Results               StorageKeyContext = "ActionResult"
	InProcessingQueue     StorageKeyContext = "InProcessingQueue"
	VotingProcess         StorageKeyContext = "VotingProcess"
	InstructionIdToHashes StorageKeyContext = "InstructionIdToHashes"

	VotingProcessVotes     StorageKeyContext = "VotingProcessVotes"
	VotingProcessFinalized StorageKeyContext = "VotingProcessFinalized"

	ReadQueue StorageKeyContext = "ReadActionQueue"
	MainQueue StorageKeyContext = "MainActionQueue"
)

type Storage[T any] struct {
	client            *redis.Client
	storageKeyContext StorageKeyContext
}

func NewClient(host string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr: host})
}

func NewStore[T any](storageKeyContext StorageKeyContext, client *redis.Client) *Storage[T] {
	return &Storage[T]{
		client:            client,
		storageKeyContext: storageKeyContext,
	}
}

func (s *Storage[T]) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *Storage[T]) Set(ctx context.Context, key string, value T) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, s.prefix(key), data, 0).Err()
}

func (s *Storage[T]) SetWithTTL(ctx context.Context, key string, value T, expiration time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, s.prefix(key), data, expiration).Err()
}

func (s *Storage[T]) Get(ctx context.Context, key string) (T, error) {
	var value T
	data, err := s.client.Get(ctx, s.prefix(key)).Bytes()
	if err != nil {
		return value, err
	}

	err = json.Unmarshal(data, &value)
	return value, err
}

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

	ttl, err := s.client.TTL(ctx, key).Result()
	return value, ttl, err
}

func (s *Storage[T]) Remove(ctx context.Context, key string) error {
	return s.client.Del(ctx, s.prefix(key)).Err()
}

func (s *Storage[T]) Enqueue(ctx context.Context, item T) error {
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return s.client.LPush(ctx, s.prefix(""), data).Err()
}

func (s *Storage[T]) Dequeue(ctx context.Context) (T, error) {
	var value T
	data, err := s.client.RPop(ctx, s.prefix("")).Bytes()
	if err != nil {
		return value, err
	}
	if len(data) == 0 {
		return value, errors.New("empty queue")
	}
	err = json.Unmarshal(data, &value)
	return value, err
}

func (s *Storage[T]) GetQueueLength(ctx context.Context) (int64, error) {
	return s.client.LLen(ctx, s.prefix("")).Result()
}

func (s *Storage[T]) ClearStorageForContext(ctx context.Context) error {
	keys, err := s.client.Keys(ctx, s.prefix("*")).Result()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	return s.client.Del(ctx, keys...).Err()
}

// prefix prefixes key with storageKeyContext-.
func (s *Storage[T]) prefix(key string) string {
	return string(s.storageKeyContext) + "-" + key
}

func (s *Storage[T]) ClearRedis() {
	s.client.FlushAll(context.Background())
}
