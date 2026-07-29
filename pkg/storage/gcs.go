package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	gcs "cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

// NewGCSClient creates a new Google Cloud Storage client.
//
// If credentialsFile is empty, Application Default Credentials are used.
// If url is non-empty, it is used as the endpoint without authentication —
// intended for emulators (e.g. fake-gcs-server) in tests.
func NewGCSClient(ctx context.Context, credentialsFile, url string) (*gcs.Client, error) {
	opts := make([]option.ClientOption, 0, 3)
	if credentialsFile != "" {
		opts = append(opts, option.WithAuthCredentialsFile(option.ServiceAccount, credentialsFile))
	}
	if url != "" {
		// The default XML download API 404s on emulators; force JSON reads.
		opts = append(opts, option.WithEndpoint(url), option.WithoutAuthentication(), gcs.WithJSONReads())
	}

	return gcs.NewClient(ctx, opts...)
}

// gcsDoc is the stored envelope: the JSON-marshaled value together with its
// expiration. Expiration is enforced lazily on Get, mirroring the other Storage
// implementations; a bucket lifecycle rule can garbage-collect objects that are
// never read again.
type gcsDoc struct {
	Data      []byte    `json:"data"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// GCSStorage is a Storage backed by a Google Cloud Storage bucket, holding one
// object per key under the given name prefix.
type GCSStorage[T any] struct {
	bucket *gcs.BucketHandle
	prefix string
}

// NewGCSStorage creates a new GCSStorage[T] storing objects as <prefix>/<key> in bucket.
func NewGCSStorage[T any](client *gcs.Client, bucket, prefix string) *GCSStorage[T] {
	// RetryAlways: writes are last-writer-wins envelopes, safe to retry; the default
	// policy would never retry unconditioned uploads.
	return &GCSStorage[T]{
		bucket: client.Bucket(bucket).Retryer(gcs.WithPolicy(gcs.RetryAlways)),
		prefix: prefix,
	}
}

func (s *GCSStorage[T]) object(key string) *gcs.ObjectHandle {
	return s.bucket.Object(s.prefix + "/" + key)
}

func (s *GCSStorage[T]) Set(ctx context.Context, key string, item T) error {
	return s.write(ctx, key, item, time.Time{})
}

func (s *GCSStorage[T]) SetWithTTL(ctx context.Context, key string, item T, expiration time.Duration) error {
	return s.write(ctx, key, item, time.Now().Add(expiration))
}

func (s *GCSStorage[T]) Get(ctx context.Context, key string) (T, error) {
	var zero T

	reader, err := s.object(key).NewReader(ctx)
	if err != nil {
		if errors.Is(err, gcs.ErrObjectNotExist) {
			return zero, ErrNotFound
		}
		return zero, fmt.Errorf("reading gcs object %s/%s: %w", s.prefix, key, err)
	}
	defer reader.Close() //nolint:errcheck // read errors surface from io.ReadAll

	raw, err := io.ReadAll(reader)
	if err != nil {
		return zero, fmt.Errorf("reading gcs object %s/%s: %w", s.prefix, key, err)
	}

	var doc gcsDoc
	if err = json.Unmarshal(raw, &doc); err != nil {
		return zero, fmt.Errorf("decoding gcs object %s/%s: %w", s.prefix, key, err)
	}

	if !doc.ExpiresAt.IsZero() && time.Now().After(doc.ExpiresAt) {
		s.deleteGeneration(ctx, key, reader.Attrs.Generation)
		return zero, ErrNotFound
	}

	var value T
	if err = json.Unmarshal(doc.Data, &value); err != nil {
		return zero, fmt.Errorf("unmarshaling gcs object %s/%s: %w", s.prefix, key, err)
	}

	return value, nil
}

// deleteGeneration best-effort deletes only the read generation of key, sparing a
// concurrently written fresh value.
func (s *GCSStorage[T]) deleteGeneration(ctx context.Context, key string, generation int64) {
	_ = s.object(key).If(gcs.Conditions{GenerationMatch: generation}).Delete(ctx)
}

func (s *GCSStorage[T]) Remove(ctx context.Context, key string) error {
	err := s.object(key).Delete(ctx)
	if err != nil && !errors.Is(err, gcs.ErrObjectNotExist) {
		return fmt.Errorf("deleting gcs object %s/%s: %w", s.prefix, key, err)
	}
	return nil
}

func (s *GCSStorage[T]) write(ctx context.Context, key string, item T, expiresAt time.Time) error {
	if key == "" {
		return ErrEmptyKey
	}

	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("marshaling value for gcs object %s/%s: %w", s.prefix, key, err)
	}

	doc, err := json.Marshal(gcsDoc{
		Data:      data,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return fmt.Errorf("marshaling envelope for gcs object %s/%s: %w", s.prefix, key, err)
	}

	// The write is atomic: it becomes visible only when Close succeeds.
	writer := s.object(key).NewWriter(ctx)
	if _, err = writer.Write(doc); err != nil {
		// Already failing; surface the write error.
		_ = writer.Close()
		return fmt.Errorf("writing gcs object %s/%s: %w", s.prefix, key, err)
	}
	if err = writer.Close(); err != nil {
		return fmt.Errorf("committing gcs object %s/%s: %w", s.prefix, key, err)
	}

	return nil
}
