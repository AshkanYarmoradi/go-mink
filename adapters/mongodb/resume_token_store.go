package mongodb

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// ResumeTokenStore persists MongoDB change-stream resume tokens.
type ResumeTokenStore struct {
	adapter *MongoAdapter
	coll    *mongo.Collection
}

// ResumeTokenStoreOption configures a ResumeTokenStore.
type ResumeTokenStoreOption func(*ResumeTokenStore)

// WithResumeTokenCollection sets the resume-token collection name.
func WithResumeTokenCollection(name string) ResumeTokenStoreOption {
	return func(s *ResumeTokenStore) {
		if name != "" {
			s.coll = s.adapter.collection(name)
		}
	}
}

// NewResumeTokenStoreFromAdapter creates a resume-token store from an adapter.
func NewResumeTokenStoreFromAdapter(adapter *MongoAdapter, opts ...ResumeTokenStoreOption) *ResumeTokenStore {
	store := &ResumeTokenStore{adapter: adapter, coll: adapter.resumeTokens()}
	for _, opt := range opts {
		opt(store)
	}
	return store
}

type resumeTokenDoc struct {
	Key       string    `bson:"_id"`
	Token     bson.Raw  `bson:"token"`
	UpdatedAt time.Time `bson:"updated_at"`
}

// Initialize creates resume-token indexes.
func (s *ResumeTokenStore) Initialize(ctx context.Context) error {
	_, err := s.coll.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "updated_at", Value: -1}},
		Options: options.Index().SetName("idx_resume_tokens_updated_at"),
	})
	return err
}

// Save stores a resume token for a subscription key.
func (s *ResumeTokenStore) Save(ctx context.Context, key string, token bson.Raw) error {
	if key == "" {
		return fmt.Errorf("mink/mongodb/resume: key is required")
	}
	if len(token) == 0 {
		return nil
	}
	doc := resumeTokenDoc{
		Key:       key,
		Token:     token,
		UpdatedAt: time.Now().UTC(),
	}
	_, err := s.coll.ReplaceOne(ctx, bson.M{"_id": key}, doc, options.Replace().SetUpsert(true))
	return err
}

// Load retrieves a resume token for a subscription key.
func (s *ResumeTokenStore) Load(ctx context.Context, key string) (bson.Raw, error) {
	if key == "" {
		return nil, nil
	}
	var doc resumeTokenDoc
	err := s.coll.FindOne(ctx, bson.M{"_id": key}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return doc.Token, nil
}

// Delete removes a stored resume token.
func (s *ResumeTokenStore) Delete(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	_, err := s.coll.DeleteOne(ctx, bson.M{"_id": key})
	return err
}
