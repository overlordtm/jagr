// Package knowledge provides a pluggable vector store for RAG-based
// knowledge retrieval. The Store interface can be backed by chromem-go
// (embedded, default), Redis, or any other vector database.
package knowledge

import (
	"context"
	"fmt"
)

// Document represents a piece of knowledge to be stored and retrieved.
type Document struct {
	ID       string            `json:"id"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// SearchResult represents a document returned from a similarity search.
type SearchResult struct {
	ID         string            `json:"id"`
	Content    string            `json:"content"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Similarity float32           `json:"similarity"`
}

// Store is the pluggable interface for knowledge storage and retrieval.
// Implementations must be safe for concurrent use.
type Store interface {
	// AddDocuments indexes one or more documents into the named collection.
	// If the collection does not exist it is created.
	AddDocuments(ctx context.Context, collection string, docs []Document) error

	// Query performs a semantic similarity search and returns the top-N results.
	// Optional metadata filters narrow the search (exact match on each key).
	Query(ctx context.Context, collection string, query string, topK int, where map[string]string) ([]SearchResult, error)

	// ListCollections returns the names of all collections.
	ListCollections(ctx context.Context) ([]string, error)

	// DeleteCollection removes a collection and all its documents.
	DeleteCollection(ctx context.Context, collection string) error

	// Close releases any resources held by the store.
	Close() error
}

// Config holds backend-agnostic configuration for the knowledge store.
type Config struct {
	// Backend selects the store implementation: "chromem" (default), "redis", etc.
	Backend string `yaml:"backend" json:"backend"`

	// DataDir is the on-disk path for persistent backends (chromem).
	// Empty means in-memory only.
	DataDir string `yaml:"data_dir" json:"data_dir"`

	// Embedding configures how document embeddings are generated.
	Embedding EmbeddingConfig `yaml:"embedding" json:"embedding"`
}

// EmbeddingConfig controls the embedding function used by the store.
type EmbeddingConfig struct {
	// Provider: "openai", "openai_compatible", "ollama". Default: "openai_compatible".
	Provider string `yaml:"provider" json:"provider"`

	// Model name (e.g. "text-embedding-3-small", "nomic-embed-text").
	Model string `yaml:"model" json:"model"`

	// BaseURL for the embedding API (required for openai_compatible and ollama).
	BaseURL string `yaml:"base_url" json:"base_url"`

	// APIKey for the embedding provider (required for openai, openai_compatible).
	APIKey string `yaml:"api_key" json:"api_key"`
}

// NewStore creates a Store from the given config. Falls back to a no-op store
// if config is nil (knowledge disabled).
func NewStore(cfg *Config) (Store, error) {
	if cfg == nil {
		return &nopStore{}, nil
	}

	backend := cfg.Backend
	if backend == "" {
		backend = "chromem"
	}

	switch backend {
	case "chromem":
		return newChromemStore(cfg)
	default:
		return nil, fmt.Errorf("unknown knowledge backend: %q", backend)
	}
}

// nopStore is returned when knowledge is disabled. All operations succeed but
// return empty results.
type nopStore struct{}

func (n *nopStore) AddDocuments(_ context.Context, _ string, _ []Document) error { return nil }
func (n *nopStore) Query(_ context.Context, _ string, _ string, _ int, _ map[string]string) ([]SearchResult, error) {
	return nil, nil
}
func (n *nopStore) ListCollections(_ context.Context) ([]string, error) { return nil, nil }
func (n *nopStore) DeleteCollection(_ context.Context, _ string) error   { return nil }
func (n *nopStore) Close() error                                         { return nil }
