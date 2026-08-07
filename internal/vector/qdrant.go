package vector

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/qdrant/go-client/qdrant"
)

type Embedder interface {
	Embed(context.Context, string) ([]float32, error)
	Fingerprint() string
	EmbeddingConfigured() bool
}

type Hit struct {
	FactID string  `json:"fact_id"`
	Score  float32 `json:"score"`
}

type Store struct {
	client          *qdrant.Client
	embedder        Embedder
	mu              sync.RWMutex
	collection      string
	queryCollection string
	dim             uint64
}

type Config struct {
	Host   string
	Port   int
	APIKey string
	UseTLS bool
}

func New(config Config, embedder Embedder) (*Store, error) {
	if config.Host == "" {
		config.Host = "localhost"
	}
	if config.Port == 0 {
		config.Port = 6334
	}
	client, err := qdrant.NewClient(&qdrant.Config{Host: config.Host, Port: config.Port, APIKey: config.APIKey, UseTLS: config.UseTLS, PoolSize: 1, SkipCompatibilityCheck: true})
	if err != nil {
		return nil, err
	}
	return &Store{client: client, embedder: embedder}, nil
}
func (s *Store) Close() {
	if s != nil && s.client != nil {
		s.client.Close()
	}
}

func (s *Store) Upsert(ctx context.Context, factID, text string) error {
	if s.embedder == nil || !s.embedder.EmbeddingConfigured() {
		return fmt.Errorf("embedding model is not configured")
	}
	embedding, err := s.embedder.Embed(ctx, text)
	if err != nil {
		return err
	}
	collection, err := s.ensure(ctx, len(embedding))
	if err != nil {
		return err
	}
	_, err = s.client.Upsert(ctx, &qdrant.UpsertPoints{CollectionName: collection, Wait: qdrant.PtrOf(true), Points: []*qdrant.PointStruct{{Id: qdrant.NewID(pointUUID(factID)), Vectors: qdrant.NewVectors(embedding...), Payload: qdrant.NewValueMap(map[string]any{"fact_id": factID, "status": "active"})}}})
	return err
}

func (s *Store) Search(ctx context.Context, text string, limit int) ([]Hit, error) {
	if s.embedder == nil || !s.embedder.EmbeddingConfigured() {
		return nil, nil
	}
	embedding, err := s.embedder.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	_, err = s.ensure(ctx, len(embedding))
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	collection := s.queryCollection
	if collection == "" {
		collection = s.collection
	}
	s.mu.RUnlock()
	if limit <= 0 {
		limit = 10
	}
	points, err := s.client.Query(ctx, &qdrant.QueryPoints{CollectionName: collection, Query: qdrant.NewQuery(embedding...), Limit: qdrant.PtrOf(uint64(limit)), WithPayload: qdrant.NewWithPayloadInclude("fact_id")})
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(points))
	for _, p := range points {
		if value := p.Payload["fact_id"]; value != nil && value.GetStringValue() != "" {
			hits = append(hits, Hit{FactID: value.GetStringValue(), Score: p.Score})
		}
	}
	return hits, nil
}

func (s *Store) Check(ctx context.Context) error {
	if s.embedder == nil || !s.embedder.EmbeddingConfigured() {
		return fmt.Errorf("embedding model is not configured")
	}
	embedding, err := s.embedder.Embed(ctx, "Alice Qdrant vector pipeline test")
	if err != nil {
		return err
	}
	_, err = s.ensure(ctx, len(embedding))
	return err
}

func (s *Store) ActivateTarget(ctx context.Context) error {
	s.mu.RLock()
	target := s.collection
	s.mu.RUnlock()
	if target == "" {
		return nil
	}
	const alias = "alice_facts_current"
	aliases, err := s.client.ListAliases(ctx)
	if err != nil {
		return err
	}
	actions := make([]*qdrant.AliasOperations, 0, 2)
	for _, a := range aliases {
		if a.GetAliasName() == alias {
			if a.GetCollectionName() == target {
				s.mu.Lock()
				s.queryCollection = alias
				s.mu.Unlock()
				return nil
			}
			actions = append(actions, &qdrant.AliasOperations{Action: &qdrant.AliasOperations_DeleteAlias{DeleteAlias: &qdrant.DeleteAlias{AliasName: alias}}})
			break
		}
	}
	actions = append(actions, &qdrant.AliasOperations{Action: &qdrant.AliasOperations_CreateAlias{CreateAlias: &qdrant.CreateAlias{AliasName: alias, CollectionName: target}}})
	if err = s.client.UpdateAliases(ctx, actions); err != nil {
		return err
	}
	s.mu.Lock()
	s.queryCollection = alias
	s.mu.Unlock()
	return nil
}

func (s *Store) Status(ctx context.Context) map[string]any {
	result := map[string]any{"driver": "qdrant", "connected": false}
	if s == nil || s.client == nil {
		return result
	}
	health, err := s.client.HealthCheck(ctx)
	if err != nil {
		result["error"] = err.Error()
		return result
	}
	result["connected"] = true
	result["version"] = health.GetVersion()
	s.mu.RLock()
	collection := s.collection
	queryCollection := s.queryCollection
	s.mu.RUnlock()
	if collection == "" && s.embedder != nil && s.embedder.EmbeddingConfigured() {
		candidate := "alice_facts_" + sanitize(s.embedder.Fingerprint())
		if exists, e := s.client.CollectionExists(ctx, candidate); e == nil && exists {
			collection = candidate
		}
	}
	if aliases, e := s.client.ListAliases(ctx); e == nil {
		for _, a := range aliases {
			if a.GetAliasName() == "alice_facts_current" {
				result["current_alias"] = a.GetCollectionName()
				queryCollection = "alice_facts_current"
				if collection == "" {
					collection = a.GetCollectionName()
				}
				break
			}
		}
	}
	result["collection"] = collection
	countCollection := queryCollection
	if countCollection == "" {
		countCollection = collection
	}
	if countCollection != "" {
		if n, e := s.client.Count(ctx, &qdrant.CountPoints{CollectionName: countCollection, Exact: qdrant.PtrOf(true)}); e == nil {
			result["points"] = n
		}
	}
	return result
}

func (s *Store) ensure(ctx context.Context, dimension int) (string, error) {
	if dimension < 1 {
		return "", fmt.Errorf("embedding dimension is zero")
	}
	fingerprint := sanitize(s.embedder.Fingerprint())
	collection := "alice_facts_" + fingerprint
	s.mu.RLock()
	ready := s.collection == collection && s.dim == uint64(dimension)
	s.mu.RUnlock()
	if ready {
		return collection, nil
	}
	exists, err := s.client.CollectionExists(ctx, collection)
	if err != nil {
		return "", err
	}
	if !exists {
		err = s.client.CreateCollection(ctx, &qdrant.CreateCollection{CollectionName: collection, VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{Size: uint64(dimension), Distance: qdrant.Distance_Cosine})})
		if err != nil {
			return "", err
		}
	} else {
		info, e := s.client.GetCollectionInfo(ctx, collection)
		if e != nil {
			return "", e
		}
		_ = info
	}
	queryCollection := collection
	if aliases, e := s.client.ListAliases(ctx); e == nil {
		for _, a := range aliases {
			if a.GetAliasName() == "alice_facts_current" {
				queryCollection = "alice_facts_current"
				break
			}
		}
	}
	s.mu.Lock()
	s.collection = collection
	s.queryCollection = queryCollection
	s.dim = uint64(dimension)
	s.mu.Unlock()
	return collection, nil
}

func pointUUID(id string) string {
	sum := sha1.Sum([]byte("alice/fact/" + id))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
func sanitize(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		b.WriteString("default")
	}
	if b.Len() > 40 {
		return b.String()[:32] + "_" + strconv.FormatUint(uint64(len(value)), 10)
	}
	return b.String()
}
