package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SearchResult is a single vector search hit.
type SearchResult struct {
	ID    string
	Score float32
}

// VectorClient stores and retrieves memory vectors.
// Implementations must be safe for concurrent use.
type VectorClient interface {
	// Upsert inserts or replaces a point in the given collection.
	// payload must include an "entry_id" key matching the Redis memory ID.
	Upsert(ctx context.Context, collection, id string, vector []float32, payload map[string]interface{}) error

	// Search returns up to limit nearest neighbours to the query vector.
	// Results are ordered by descending score (most similar first).
	Search(ctx context.Context, collection string, vector []float32, limit int) ([]SearchResult, error)
}

// Embedder converts arbitrary text to a dense float32 vector.
// Implementations must be safe for concurrent use.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// ─── Qdrant REST client ───────────────────────────────────────────────────────

// QdrantClient implements VectorClient against a Qdrant REST API.
type QdrantClient struct {
	baseURL    string
	httpClient *http.Client

	mu          sync.Mutex
	collections map[string]bool // collections already ensured in this process
}

// NewQdrantClient creates a Qdrant client for the given base URL
// (e.g. "http://localhost:6333").
func NewQdrantClient(baseURL string) *QdrantClient {
	return &QdrantClient{
		baseURL:     strings.TrimRight(baseURL, "/"),
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		collections: make(map[string]bool),
	}
}

// pointID hashes an entry-ID string to a Qdrant-compatible uint64.
func pointID(id string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(id))
	return h.Sum64()
}

// ensureCollection creates the Qdrant collection on first use.
// Subsequent calls for the same name are no-ops (tracked in memory).
func (q *QdrantClient) ensureCollection(ctx context.Context, name string, dim int) error {
	q.mu.Lock()
	if q.collections[name] {
		q.mu.Unlock()
		return nil
	}
	q.mu.Unlock()

	body, err := json.Marshal(map[string]interface{}{
		"vectors": map[string]interface{}{
			"size":     dim,
			"distance": "Cosine",
		},
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		q.baseURL+"/collections/"+name, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant create collection %q: %w", name, err)
	}
	defer resp.Body.Close()

	// 200 OK: created or unchanged — mark as ready.
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qdrant create collection %q: HTTP %d", name, resp.StatusCode)
	}

	q.mu.Lock()
	q.collections[name] = true
	q.mu.Unlock()
	return nil
}

// Upsert stores a vector point in Qdrant.
func (q *QdrantClient) Upsert(ctx context.Context, collection, id string, vector []float32, payload map[string]interface{}) error {
	if err := q.ensureCollection(ctx, collection, len(vector)); err != nil {
		return err
	}

	body, err := json.Marshal(map[string]interface{}{
		"points": []map[string]interface{}{
			{
				"id":      pointID(id),
				"vector":  vector,
				"payload": payload,
			},
		},
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		q.baseURL+"/collections/"+collection+"/points", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant upsert: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qdrant upsert: HTTP %d", resp.StatusCode)
	}
	return nil
}

// Search returns the nearest vector neighbours from Qdrant.
func (q *QdrantClient) Search(ctx context.Context, collection string, vector []float32, limit int) ([]SearchResult, error) {
	body, err := json.Marshal(map[string]interface{}{
		"vector":       vector,
		"limit":        limit,
		"with_payload": true,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		q.baseURL+"/collections/"+collection+"/points/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qdrant search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qdrant search: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Result []struct {
			Score   float32                `json:"score"`
			Payload map[string]interface{} `json:"payload"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("qdrant search decode: %w", err)
	}

	hits := make([]SearchResult, 0, len(result.Result))
	for _, r := range result.Result {
		entryID, _ := r.Payload["entry_id"].(string)
		if entryID == "" {
			continue
		}
		hits = append(hits, SearchResult{ID: entryID, Score: r.Score})
	}
	return hits, nil
}

// ─── HTTP embedder (OpenAI-compatible) ────────────────────────────────────────

// HTTPEmbedder calls an OpenAI-compatible /v1/embeddings endpoint.
type HTTPEmbedder struct {
	apiURL     string
	apiKey     string
	model      string
	httpClient *http.Client
}

// NewHTTPEmbedder creates an embedder that POSTs to apiURL + "/v1/embeddings".
//
//	apiURL: base URL, e.g. "https://api.openai.com" or "http://localhost:8080"
//	apiKey: Bearer token (empty string = no Authorization header)
//	model:  model name, e.g. "text-embedding-3-small"
func NewHTTPEmbedder(apiURL, apiKey, model string) *HTTPEmbedder {
	return &HTTPEmbedder{
		apiURL:     strings.TrimRight(apiURL, "/"),
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Embed calls the embeddings API and returns the dense float32 vector.
func (e *HTTPEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(map[string]string{
		"model": e.model,
		"input": text,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.apiURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings API: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("embeddings API decode: %w", err)
	}
	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embeddings API: empty embedding in response")
	}
	return result.Data[0].Embedding, nil
}
