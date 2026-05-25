package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// NewFirestoreClient creates a new Firestore client for the given project.
//
// If credentialsFile is empty, Application Default Credentials are used.
// If databaseID is empty, the default database is used.
func NewFirestoreClient(ctx context.Context, projectID, databaseID, credentialsFile, url string) (*firestore.Client, error) {
	opts := make([]option.ClientOption, 0, 1)
	if credentialsFile != "" {
		opts = append(opts, option.WithCredentialsFile(credentialsFile))
	}
	if url != "" {
		err := os.Setenv("FIRESTORE_EMULATOR_HOST", url)
		if err != nil {
			return nil, err
		}
	}

	if databaseID != "" {
		return firestore.NewClientWithDatabase(ctx, projectID, databaseID, opts...)
	}
	return firestore.NewClient(ctx, projectID, opts...)
}

type firestoreDoc struct {
	Data      []byte    `firestore:"data"`
	ExpiresAt time.Time `firestore:"expiresAt"`
}

// FirestoreStorage is a Storage backed by Firestore.
type FirestoreStorage[T any] struct {
	client     *firestore.Client
	collection string
}

// NewFirestoreStorage creates a new FirestoreStorage[T] backed by the given Firestore collection.
func NewFirestoreStorage[T any](client *firestore.Client, collection string) *FirestoreStorage[T] {
	return &FirestoreStorage[T]{
		client:     client,
		collection: collection,
	}
}

func (f *FirestoreStorage[T]) Set(ctx context.Context, key string, item T) error {
	return f.write(ctx, key, item, time.Time{})
}

func (f *FirestoreStorage[T]) SetWithTTL(ctx context.Context, key string, item T, expiration time.Duration) error {
	return f.write(ctx, key, item, time.Now().Add(expiration))
}

func (f *FirestoreStorage[T]) Get(ctx context.Context, key string) (T, error) {
	var zero T

	snap, err := f.client.Collection(f.collection).Doc(key).Get(ctx)
	if err != nil {
		if grpcstatus.Code(err) == codes.NotFound {
			return zero, ErrNotFound
		}
		return zero, fmt.Errorf("getting firestore doc %s/%s: %w", f.collection, key, err)
	}

	var doc firestoreDoc
	if err = snap.DataTo(&doc); err != nil {
		return zero, fmt.Errorf("decoding firestore doc %s/%s: %w", f.collection, key, err)
	}

	if !doc.ExpiresAt.IsZero() && time.Now().After(doc.ExpiresAt) {
		_, _ = f.client.Collection(f.collection).Doc(key).Delete(ctx) // best-effort cleanup; safe to retry on next Get
		return zero, ErrNotFound
	}

	var value T
	if err = json.Unmarshal(doc.Data, &value); err != nil {
		return zero, fmt.Errorf("unmarshaling firestore doc %s/%s: %w", f.collection, key, err)
	}

	return value, nil
}

func (f *FirestoreStorage[T]) Remove(ctx context.Context, key string) error {
	_, err := f.client.Collection(f.collection).Doc(key).Delete(ctx)
	if err != nil {
		return fmt.Errorf("deleting firestore doc %s/%s: %w", f.collection, key, err)
	}
	return nil
}

func (f *FirestoreStorage[T]) write(ctx context.Context, key string, item T, expiresAt time.Time) error {
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("marshaling value for firestore doc %s/%s: %w", f.collection, key, err)
	}

	_, err = f.client.Collection(f.collection).Doc(key).Set(ctx, firestoreDoc{
		Data:      data,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return fmt.Errorf("writing firestore doc %s/%s: %w", f.collection, key, err)
	}
	return nil
}
