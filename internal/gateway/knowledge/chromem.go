package knowledge

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/philippgille/chromem-go"
)

// chromemStore implements Store using chromem-go as the vector backend.
type chromemStore struct {
	db            *chromem.DB
	embeddingFunc chromem.EmbeddingFunc
	mu            sync.RWMutex
}

func newChromemStore(cfg *Config) (*chromemStore, error) {
	var db *chromem.DB
	var err error

	if cfg.DataDir != "" {
		db, err = chromem.NewPersistentDB(cfg.DataDir, true)
		if err != nil {
			return nil, fmt.Errorf("chromem persistent DB: %w", err)
		}
	} else {
		db = chromem.NewDB()
	}

	ef, err := buildEmbeddingFunc(cfg.Embedding)
	if err != nil {
		return nil, fmt.Errorf("embedding func: %w", err)
	}

	return &chromemStore{
		db:            db,
		embeddingFunc: ef,
	}, nil
}

func buildEmbeddingFunc(cfg EmbeddingConfig) (chromem.EmbeddingFunc, error) {
	provider := cfg.Provider
	if provider == "" {
		provider = "openai_compatible"
	}

	switch provider {
	case "openai":
		model := chromem.EmbeddingModelOpenAI(cfg.Model)
		if cfg.Model == "" {
			model = chromem.EmbeddingModelOpenAI3Small
		}
		return chromem.NewEmbeddingFuncOpenAI(cfg.APIKey, model), nil

	case "openai_compatible":
		if cfg.BaseURL == "" {
			return nil, fmt.Errorf("base_url is required for openai_compatible embedding provider")
		}
		model := cfg.Model
		if model == "" {
			model = "text-embedding-3-small"
		}
		// normalized=true because chromem uses cosine similarity
		return chromem.NewEmbeddingFuncOpenAICompat(
			strings.TrimSuffix(cfg.BaseURL, "/"),
			cfg.APIKey,
			model,
			nil,
		), nil

	case "ollama":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434/api"
		}
		model := cfg.Model
		if model == "" {
			model = "nomic-embed-text"
		}
		return chromem.NewEmbeddingFuncOllama(model, baseURL), nil

	default:
		return nil, fmt.Errorf("unknown embedding provider: %q", provider)
	}
}

func (s *chromemStore) getOrCreateCollection(name string) (*chromem.Collection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.GetOrCreateCollection(name, nil, s.embeddingFunc)
}

func (s *chromemStore) AddDocuments(ctx context.Context, collection string, docs []Document) error {
	coll, err := s.getOrCreateCollection(collection)
	if err != nil {
		return fmt.Errorf("get/create collection %q: %w", collection, err)
	}

	chromDocs := make([]chromem.Document, len(docs))
	for i, d := range docs {
		chromDocs[i] = chromem.Document{
			ID:       d.ID,
			Content:  d.Content,
			Metadata: d.Metadata,
		}
	}

	concurrency := runtime.NumCPU()
	if concurrency > 8 {
		concurrency = 8
	}
	if concurrency < 1 {
		concurrency = 1
	}

	return coll.AddDocuments(ctx, chromDocs, concurrency)
}

func (s *chromemStore) Query(ctx context.Context, collection string, query string, topK int, where map[string]string) ([]SearchResult, error) {
	s.mu.RLock()
	coll := s.db.GetCollection(collection, s.embeddingFunc)
	s.mu.RUnlock()

	if coll == nil {
		return nil, nil
	}

	if topK <= 0 {
		topK = 5
	}

	results, err := coll.Query(ctx, query, topK, where, nil)
	if err != nil {
		return nil, fmt.Errorf("query collection %q: %w", collection, err)
	}

	out := make([]SearchResult, len(results))
	for i, r := range results {
		out[i] = SearchResult{
			ID:         r.ID,
			Content:    r.Content,
			Metadata:   r.Metadata,
			Similarity: r.Similarity,
		}
	}
	return out, nil
}

func (s *chromemStore) ListCollections(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	colls := s.db.ListCollections()
	names := make([]string, 0, len(colls))
	for name := range colls {
		names = append(names, name)
	}
	return names, nil
}

func (s *chromemStore) DeleteCollection(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.DeleteCollection(name)
}

func (s *chromemStore) Close() error {
	// chromem-go doesn't have an explicit close, but persistent DB flushes on write.
	return nil
}
