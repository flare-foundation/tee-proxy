package storage

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

type TestStruct struct {
	ID   string
	Name string
}

func TestStorage(t *testing.T) {
	mr := miniredis.RunT(t)
	c := NewClient(mr.Addr())

	s := NewRedisStorage[TestStruct]("testMain", c)

	t.Run("Set TTL and Get", func(t *testing.T) {
		t.Parallel()

		item := TestStruct{ID: "1", Name: "Test"}
		err := s.SetWithTTL(t.Context(), item.ID, item, 60*time.Minute)
		require.NoError(t, err)

		retrieved, err := s.Get(t.Context(), item.ID)
		require.NoError(t, err)
		require.Equal(t, item, retrieved)
	})

	t.Run("Set TTL wait for expiration", func(t *testing.T) {
		t.Parallel()

		item := TestStruct{ID: "2", Name: "Test"}
		err := s.SetWithTTL(t.Context(), item.ID, item, 20*time.Minute)
		require.NoError(t, err)

		retrieved, err := s.Get(t.Context(), item.ID)
		require.NoError(t, err)
		require.Equal(t, item, retrieved)

		mr.FastForward(30 * time.Minute)

		_, err = s.Get(t.Context(), item.ID)
		require.Error(t, err)
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("Set and remove", func(t *testing.T) {
		t.Parallel()

		item := TestStruct{ID: "3", Name: "Test"}

		err := s.SetWithTTL(t.Context(), item.ID, item, 60*time.Minute)
		require.NoError(t, err)

		err = s.Remove(t.Context(), item.ID)
		require.NoError(t, err)

		_, err = s.Get(t.Context(), item.ID)
		require.Error(t, err)
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("Enqueue and Dequeue", func(t *testing.T) {
		t.Parallel()

		item1 := TestStruct{ID: "4", Name: "Test1"}
		item2 := TestStruct{ID: "5", Name: "Test2"}

		err := s.Enqueue(t.Context(), item1)
		require.NoError(t, err)

		err = s.Enqueue(t.Context(), item2)
		require.NoError(t, err)

		l, err := s.QueueLength(t.Context())
		require.NoError(t, err)
		require.Equal(t, int64(2), l)

		dequeued1, err := s.Dequeue(t.Context())
		require.NoError(t, err)
		require.Equal(t, item1, dequeued1)

		dequeued2, err := s.Dequeue(t.Context())
		require.NoError(t, err)
		require.Equal(t, item2, dequeued2)

		_, err = s.Dequeue(t.Context())
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrEmptyQueue))
	})

	t.Run("Set with empty key", func(t *testing.T) {
		t.Parallel()

		item := TestStruct{ID: "6", Name: "Test"}

		err := s.Set(t.Context(), "", item)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrEmptyKey))

		err = s.SetWithTTL(t.Context(), "", item, 10*time.Minute)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrEmptyKey))
	})

	t.Run("Set and rewrite", func(t *testing.T) {
		t.Parallel()

		item := TestStruct{ID: "7", Name: "Test"}
		item2 := TestStruct{ID: "7", Name: "Test2"}

		err := s.Set(t.Context(), item.ID, item)
		require.NoError(t, err)

		err = s.SetWithTTL(t.Context(), item2.ID, item2, 60*time.Minute)
		require.NoError(t, err)

		retrieved, err := s.Get(t.Context(), item.ID)
		require.NoError(t, err)
		require.Equal(t, item2, retrieved)
	})
}

func TestClearDatabase(t *testing.T) {
	mr := miniredis.RunT(t)
	c := NewClient(mr.Addr())

	s := NewRedisStorage[TestStruct]("testClear", c)

	items := []TestStruct{
		{ID: "1", Name: "Test1"},
		{ID: "2", Name: "Test2"},
		{ID: "3", Name: "Test3"},
	}

	for _, item := range items {
		err := s.Set(t.Context(), item.ID, item)
		require.NoError(t, err)
	}

	require.Len(t, mr.Keys(), len(items))

	err := s.Clear(t.Context())
	require.NoError(t, err)

	require.Len(t, mr.Keys(), 0)
}

func TestRemove(t *testing.T) {
	mr := miniredis.RunT(t)
	c := NewClient(mr.Addr())

	s := NewRedisStorage[TestStruct]("testRemove", c)

	item1 := TestStruct{ID: "1", Name: "Test1"}
	item2 := TestStruct{ID: "2", Name: "Test2"}

	err := s.Set(t.Context(), item1.ID, item1)
	require.NoError(t, err)

	err = s.Set(t.Context(), item2.ID, item2)
	require.NoError(t, err)

	err = s.Remove(t.Context(), item1.ID)
	require.NoError(t, err)

	_, err = s.Get(t.Context(), item1.ID)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNotFound)

	err = s.Remove(t.Context(), item1.ID)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())

	cancel()
	err = s.Remove(ctx, item2.ID)
	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled))
}

// TestNotifierSubscribeActiveOnReturn guards the subscribe confirmation: a publish issued right
// after Subscribe returns must be delivered, otherwise waiters can miss their notification.
func TestNotifierSubscribeActiveOnReturn(t *testing.T) {
	mr := miniredis.RunT(t)
	n := NewNotifier(NewClient(mr.Addr()))

	for i := range 500 {
		channel := "notify-" + strconv.Itoa(i)

		sub, err := n.Subscribe(t.Context(), channel)
		require.NoError(t, err)

		require.NoError(t, n.Publish(t.Context(), channel))

		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		_, err = sub.ReceiveMessage(ctx)
		cancel()
		require.NoError(t, err, "publish right after Subscribe was not delivered (iteration %d)", i)

		require.NoError(t, sub.Close())
	}
}
