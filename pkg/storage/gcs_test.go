package storage

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	gcs "cloud.google.com/go/storage"
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

// TestGCSClientEndpointOverride verifies the config `url` branch of NewGCSClient
// round-trips against a listening fake GCS server; the default XML download API
// 404s on emulators (reads used to fail while writes succeeded).
func TestGCSClientEndpointOverride(t *testing.T) {
	server, err := fakestorage.NewServerWithOptions(fakestorage.Options{Scheme: "http", Port: 0})
	require.NoError(t, err)
	t.Cleanup(server.Stop)
	server.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: testBucket})

	client, err := NewGCSClient(t.Context(), "", server.URL()+"/storage/v1/")
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	s := NewGCSStorage[TestStruct](client, testBucket, "override")

	item := TestStruct{ID: "1", Name: "ViaEndpoint"}
	require.NoError(t, s.Set(t.Context(), item.ID, item))

	got, err := s.Get(t.Context(), item.ID)
	require.NoError(t, err)
	require.Equal(t, item, got)

	require.NoError(t, s.Remove(t.Context(), item.ID))
	_, err = s.Get(t.Context(), item.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

// TestGCSStorageExpiredObjectIsDeleted verifies the lazy-expiry cleanup physically
// removes the expired object, not just that Get reports not found.
func TestGCSStorageExpiredObjectIsDeleted(t *testing.T) {
	server := newFakeGCS(t)
	s := NewGCSStorage[TestStruct](server.Client(), testBucket, "expiry")

	item := TestStruct{ID: "1", Name: "Test"}
	require.NoError(t, s.SetWithTTL(t.Context(), item.ID, item, -time.Second))

	_, err := s.Get(t.Context(), item.ID)
	require.ErrorIs(t, err, ErrNotFound)

	_, err = server.Client().Bucket(testBucket).Object("expiry/1").Attrs(t.Context())
	require.ErrorIs(t, err, gcs.ErrObjectNotExist)
}

// TestGCSStorageDeleteGenerationSendsPrecondition verifies the expiry cleanup
// attaches the generation precondition, which real GCS enforces server-side (412 on
// mismatch) so a concurrent rewrite is never deleted. fake-gcs-server ignores
// delete preconditions, so this asserts on the outgoing request instead.
func TestGCSStorageDeleteGenerationSendsPrecondition(t *testing.T) {
	var captured atomic.Value
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			captured.Store(r.URL.Query().Get("ifGenerationMatch"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(stub.Close)

	client, err := NewGCSClient(t.Context(), "", stub.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	s := NewGCSStorage[TestStruct](client, testBucket, "gen")
	s.deleteGeneration(t.Context(), "1", 42)

	require.Equal(t, "42", captured.Load())
}

// TestGCSStorageCorruptEnvelope verifies undecodable stored bytes surface as a
// non-ErrNotFound error at both decode layers (envelope and inner value).
func TestGCSStorageCorruptEnvelope(t *testing.T) {
	server := newFakeGCS(t)
	s := NewGCSStorage[TestStruct](server.Client(), testBucket, "corrupt")

	server.CreateObject(fakestorage.Object{
		ObjectAttrs: fakestorage.ObjectAttrs{BucketName: testBucket, Name: "corrupt/envelope"},
		Content:     []byte("not json"),
	})
	_, err := s.Get(t.Context(), "envelope")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNotFound)

	server.CreateObject(fakestorage.Object{
		ObjectAttrs: fakestorage.ObjectAttrs{BucketName: testBucket, Name: "corrupt/inner"},
		Content:     []byte(`{"data":"Imp1c3QgYSBzdHJpbmci","expiresAt":"0001-01-01T00:00:00Z"}`),
	})
	_, err = s.Get(t.Context(), "inner")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNotFound)
}
