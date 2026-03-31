package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── NewQdrantClient ──────────────────────────────────────────────────────────

func TestNewQdrantClient_TrimsTrailingSlash(t *testing.T) {
	q := NewQdrantClient("http://localhost:6333/")
	if q.baseURL != "http://localhost:6333" {
		t.Errorf("baseURL: got %q, want without trailing slash", q.baseURL)
	}
}

func TestNewQdrantClient_NoTrailingSlash(t *testing.T) {
	q := NewQdrantClient("http://localhost:6333")
	if q.baseURL != "http://localhost:6333" {
		t.Errorf("baseURL: got %q, want http://localhost:6333", q.baseURL)
	}
	if q.httpClient == nil {
		t.Error("httpClient should be non-nil")
	}
	if q.collections == nil {
		t.Error("collections map should be non-nil")
	}
}

// ─── pointID ─────────────────────────────────────────────────────────────────

func TestPointID_Deterministic(t *testing.T) {
	id1 := pointID("abc123")
	id2 := pointID("abc123")
	if id1 != id2 {
		t.Error("pointID is not deterministic for the same input")
	}
}

func TestPointID_DifferentInputsDifferentIDs(t *testing.T) {
	if pointID("alpha") == pointID("beta") {
		t.Error("different inputs should produce different point IDs")
	}
}

func TestPointID_EmptyString(t *testing.T) {
	// Empty string should not panic and should produce a stable value.
	id1 := pointID("")
	id2 := pointID("")
	if id1 != id2 {
		t.Error("pointID(\"\") is not stable")
	}
}

// ─── ensureCollection ────────────────────────────────────────────────────────

func TestQdrantClient_EnsureCollection_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	q := NewQdrantClient(srv.URL)
	if err := q.ensureCollection(context.Background(), "memories", 4); err != nil {
		t.Fatalf("ensureCollection: %v", err)
	}
	// Collection should now be cached.
	q.mu.Lock()
	cached := q.collections["memories"]
	q.mu.Unlock()
	if !cached {
		t.Error("collection should be cached after successful ensureCollection")
	}
}

func TestQdrantClient_EnsureCollection_CachedSkipsHTTP(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	q := NewQdrantClient(srv.URL)
	ctx := context.Background()
	q.ensureCollection(ctx, "dedup-coll", 4)   // first call: HTTP
	q.ensureCollection(ctx, "dedup-coll", 4)   // second call: should be no-op
	if calls != 1 {
		t.Errorf("expected exactly 1 HTTP call (second call cached), got %d", calls)
	}
}

func TestQdrantClient_EnsureCollection_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	q := NewQdrantClient(srv.URL)
	err := q.ensureCollection(context.Background(), "bad-coll", 4)
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error should mention HTTP 500, got: %v", err)
	}
	// Failed collection should not be cached.
	q.mu.Lock()
	cached := q.collections["bad-coll"]
	q.mu.Unlock()
	if cached {
		t.Error("failed collection should not be cached")
	}
}

// ─── Upsert ──────────────────────────────────────────────────────────────────

func TestQdrantClient_Upsert_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ensureCollection hits /collections/{name}, upsert hits /collections/{name}/points
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	q := NewQdrantClient(srv.URL)
	err := q.Upsert(context.Background(), "test-coll", "entry-1",
		[]float32{0.1, 0.2, 0.3}, map[string]interface{}{"entry_id": "entry-1"})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}

func TestQdrantClient_Upsert_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/points") {
			// The upsert call fails.
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			// ensureCollection succeeds.
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	q := NewQdrantClient(srv.URL)
	err := q.Upsert(context.Background(), "err-coll", "entry-1",
		[]float32{0.1, 0.2}, map[string]interface{}{"entry_id": "entry-1"})
	if err == nil {
		t.Fatal("expected error when upsert returns HTTP 500")
	}
}

func TestQdrantClient_Upsert_SendsCorrectPayload(t *testing.T) {
	var captured map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/points") {
			json.NewDecoder(r.Body).Decode(&captured)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	q := NewQdrantClient(srv.URL)
	q.Upsert(context.Background(), "payload-coll", "my-entry",
		[]float32{1.0, 0.0}, map[string]interface{}{"entry_id": "my-entry", "agent": "bot"})

	points, ok := captured["points"].([]interface{})
	if !ok || len(points) == 0 {
		t.Fatalf("expected 'points' array in upsert payload, got: %v", captured)
	}
}

// ─── Search ───────────────────────────────────────────────────────────────────

func qdrantSearchServer(t *testing.T, result interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}))
}

func TestQdrantClient_Search_Success(t *testing.T) {
	response := map[string]interface{}{
		"result": []map[string]interface{}{
			{"score": 0.95, "payload": map[string]interface{}{"entry_id": "entry-1"}},
			{"score": 0.80, "payload": map[string]interface{}{"entry_id": "entry-2"}},
		},
	}
	srv := qdrantSearchServer(t, response)
	defer srv.Close()

	q := NewQdrantClient(srv.URL)
	q.mu.Lock()
	q.collections["mem"] = true
	q.mu.Unlock()

	results, err := q.Search(context.Background(), "mem", []float32{0.1, 0.2}, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != "entry-1" {
		t.Errorf("first result ID: got %q, want entry-1", results[0].ID)
	}
	if results[0].Score != 0.95 {
		t.Errorf("first result Score: got %v, want 0.95", results[0].Score)
	}
}

func TestQdrantClient_Search_SkipsEntriesWithoutID(t *testing.T) {
	response := map[string]interface{}{
		"result": []map[string]interface{}{
			{"score": 0.9, "payload": map[string]interface{}{"entry_id": ""}},  // empty ID — skip
			{"score": 0.8, "payload": map[string]interface{}{}},                // missing key — skip
			{"score": 0.7, "payload": map[string]interface{}{"entry_id": "ok"}},
		},
	}
	srv := qdrantSearchServer(t, response)
	defer srv.Close()

	q := NewQdrantClient(srv.URL)
	q.mu.Lock()
	q.collections["mem"] = true
	q.mu.Unlock()

	results, err := q.Search(context.Background(), "mem", []float32{0.1}, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].ID != "ok" {
		t.Errorf("expected 1 result with ID 'ok', got %v", results)
	}
}

func TestQdrantClient_Search_EmptyResults(t *testing.T) {
	response := map[string]interface{}{"result": []interface{}{}}
	srv := qdrantSearchServer(t, response)
	defer srv.Close()

	q := NewQdrantClient(srv.URL)
	q.mu.Lock()
	q.collections["mem"] = true
	q.mu.Unlock()

	results, err := q.Search(context.Background(), "mem", []float32{0.1}, 5)
	if err != nil {
		t.Fatalf("Search on empty result: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestQdrantClient_Search_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	q := NewQdrantClient(srv.URL)
	q.mu.Lock()
	q.collections["mem"] = true
	q.mu.Unlock()

	_, err := q.Search(context.Background(), "mem", []float32{0.1}, 5)
	if err == nil {
		t.Fatal("expected error for HTTP 503")
	}
}

// ─── NewHTTPEmbedder ─────────────────────────────────────────────────────────

func TestNewHTTPEmbedder_TrimsTrailingSlash(t *testing.T) {
	e := NewHTTPEmbedder("https://api.openai.com/", "mykey", "text-embedding-3-small")
	if e.apiURL != "https://api.openai.com" {
		t.Errorf("apiURL: got %q, want without trailing slash", e.apiURL)
	}
	if e.model != "text-embedding-3-small" {
		t.Errorf("model: got %q, want text-embedding-3-small", e.model)
	}
	if e.apiKey != "mykey" {
		t.Errorf("apiKey: got %q, want mykey", e.apiKey)
	}
}

// ─── HTTPEmbedder.Embed ───────────────────────────────────────────────────────

func embeddingsServer(t *testing.T, requireAuth bool, vector []float32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requireAuth && r.Header.Get("Authorization") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{"embedding": vector},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestHTTPEmbedder_Embed_Success(t *testing.T) {
	want := []float32{0.1, 0.2, 0.3}
	srv := embeddingsServer(t, true, want)
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "testkey", "test-model")
	vec, err := e.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != len(want) {
		t.Fatalf("vector length: got %d, want %d", len(vec), len(want))
	}
}

func TestHTTPEmbedder_Embed_NoAuthHeader(t *testing.T) {
	// Empty apiKey → no Authorization header should be sent.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected auth header", http.StatusBadRequest)
			return
		}
		resp := map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{0.5, 0.5}}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "", "test-model")
	vec, err := e.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed with empty apiKey: %v", err)
	}
	if len(vec) == 0 {
		t.Error("expected non-empty vector")
	}
}

func TestHTTPEmbedder_Embed_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "bad-key", "model")
	_, err := e.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for HTTP 401")
	}
	if !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("error should mention HTTP 401, got: %v", err)
	}
}

func TestHTTPEmbedder_Embed_EmptyEmbeddingResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{"data": []interface{}{}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "", "model")
	_, err := e.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for empty embedding data array")
	}
	if !strings.Contains(err.Error(), "empty embedding") {
		t.Errorf("error should mention empty embedding, got: %v", err)
	}
}

func TestHTTPEmbedder_Embed_SendsModelAndInput(t *testing.T) {
	var captured map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		resp := map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1.0}}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "", "my-embedding-model")
	e.Embed(context.Background(), "the input text")

	if captured["model"] != "my-embedding-model" {
		t.Errorf("request body model: got %q, want my-embedding-model", captured["model"])
	}
	if captured["input"] != "the input text" {
		t.Errorf("request body input: got %q, want 'the input text'", captured["input"])
	}
}
