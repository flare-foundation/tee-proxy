package storage

import (
	"testing"
	"time"

	"github.com/fsouza/fake-gcs-server/fakestorage"
	"github.com/stretchr/testify/require"
)

const testBucket = "test-bucket"

// newFakeGCS starts an in-process fake GCS server and returns a client bound to it.
func newFakeGCS(t *testing.T) *fakestorage.Server {
	t.Helper()

	server, err := fakestorage.NewServerWithOptions(fakestorage.Options{NoListener: true})
	require.NoError(t, err)
	t.Cleanup(server.Stop)

	server.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: testBucket})

	return server
}

func TestGCSStorage(t *testing.T) {
	server := newFakeGCS(t)
	s := NewGCSStorage[TestStruct](server.Client(), testBucket, "testMain")

	t.Run("Set and Get", func(t *testing.T) {
		t.Parallel()

		item := TestStruct{ID: "1", Name: "Test"}
		err := s.SetWithTTL(t.Context(), item.ID, item, 60*time.Minute)
		require.NoError(t, err)

		retrieved, err := s.Get(t.Context(), item.ID)
		require.NoError(t, err)
		require.Equal(t, item, retrieved)
	})

	t.Run("Set TTL expiration", func(t *testing.T) {
		t.Parallel()

		item := TestStruct{ID: "2", Name: "Test"}
		// Already-expired TTL: expiry is enforced lazily on Get.
		err := s.SetWithTTL(t.Context(), item.ID, item, -time.Second)
		require.NoError(t, err)

		_, err = s.Get(t.Context(), item.ID)
		require.ErrorIs(t, err, ErrNotFound)

		// The expired object is deleted on first Get; still not found afterwards.
		_, err = s.Get(t.Context(), item.ID)
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("Set without TTL does not expire", func(t *testing.T) {
		t.Parallel()

		item := TestStruct{ID: "3", Name: "Test"}
		err := s.Set(t.Context(), item.ID, item)
		require.NoError(t, err)

		retrieved, err := s.Get(t.Context(), item.ID)
		require.NoError(t, err)
		require.Equal(t, item, retrieved)
	})

	t.Run("Set and remove", func(t *testing.T) {
		t.Parallel()

		item := TestStruct{ID: "4", Name: "Test"}
		err := s.SetWithTTL(t.Context(), item.ID, item, 60*time.Minute)
		require.NoError(t, err)

		err = s.Remove(t.Context(), item.ID)
		require.NoError(t, err)

		_, err = s.Get(t.Context(), item.ID)
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("Remove missing key", func(t *testing.T) {
		t.Parallel()

		err := s.Remove(t.Context(), "no-such-key")
		require.NoError(t, err)
	})

	t.Run("Get missing key", func(t *testing.T) {
		t.Parallel()

		_, err := s.Get(t.Context(), "also-no-such-key")
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("Set with empty key", func(t *testing.T) {
		t.Parallel()

		item := TestStruct{ID: "5", Name: "Test"}

		err := s.Set(t.Context(), "", item)
		require.ErrorIs(t, err, ErrEmptyKey)

		err = s.SetWithTTL(t.Context(), "", item, 10*time.Minute)
		require.ErrorIs(t, err, ErrEmptyKey)
	})

	t.Run("Set and rewrite", func(t *testing.T) {
		t.Parallel()

		item := TestStruct{ID: "6", Name: "Test"}
		item2 := TestStruct{ID: "6", Name: "Test2"}

		err := s.Set(t.Context(), item.ID, item)
		require.NoError(t, err)

		err = s.Set(t.Context(), item2.ID, item2)
		require.NoError(t, err)

		retrieved, err := s.Get(t.Context(), item.ID)
		require.NoError(t, err)
		require.Equal(t, item2, retrieved)
	})
}

// TestGCSStoragePrefixIsolation verifies that storages sharing a bucket but using
// different prefixes do not see each other's keys.
func TestGCSStoragePrefixIsolation(t *testing.T) {
	server := newFakeGCS(t)

	a := NewGCSStorage[TestStruct](server.Client(), testBucket, "collectionA")
	b := NewGCSStorage[TestStruct](server.Client(), testBucket, "collectionB")

	item := TestStruct{ID: "1", Name: "OnlyInA"}
	require.NoError(t, a.Set(t.Context(), item.ID, item))

	_, err := b.Get(t.Context(), item.ID)
	require.ErrorIs(t, err, ErrNotFound)

	got, err := a.Get(t.Context(), item.ID)
	require.NoError(t, err)
	require.Equal(t, item, got)
}

// TestGCSStorageLargeValue verifies values beyond Firestore's former 1 MiB document
// limit round-trip — the motivation for the GCS backend.
func TestGCSStorageLargeValue(t *testing.T) {
	server := newFakeGCS(t)
	s := NewGCSStorage[[]byte](server.Client(), testBucket, "large")

	big := make([]byte, 5<<20) // 5 MiB
	for i := range big {
		big[i] = byte(i % 251)
	}

	require.NoError(t, s.Set(t.Context(), "blob", big))

	got, err := s.Get(t.Context(), "blob")
	require.NoError(t, err)
	require.Equal(t, big, got)
}
