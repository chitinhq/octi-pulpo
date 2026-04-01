package optimize

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewBatchQueue(t *testing.T) {
	q := NewBatchQueue("test-key")
	if q.maxSize != defaultBatchMaxSize {
		t.Errorf("expected maxSize %d, got %d", defaultBatchMaxSize, q.maxSize)
	}
	if q.Pending() != 0 {
		t.Errorf("expected 0 pending, got %d", q.Pending())
	}
}

func TestBatchQueue_Enqueue(t *testing.T) {
	q := NewBatchQueue("test-key")
	// Set high max so it doesn't auto-flush
	q.maxSize = 100

	err := q.Enqueue(context.Background(), "req_1", map[string]any{
		"model":      "claude-haiku-4-5-20251001",
		"max_tokens": 1024,
		"messages":   []map[string]string{{"role": "user", "content": "hello"}},
	})
	if err != nil {
		t.Fatalf("enqueue error: %v", err)
	}
	if q.Pending() != 1 {
		t.Errorf("expected 1 pending, got %d", q.Pending())
	}
}

func TestBatchQueue_FlushEmpty(t *testing.T) {
	q := NewBatchQueue("test-key")
	err := q.Flush(context.Background())
	if err != nil {
		t.Fatalf("flush empty should not error: %v", err)
	}
}

func TestBatchQueue_FlushSendsRequests(t *testing.T) {
	var receivedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)

		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key test-key, got %s", r.Header.Get("x-api-key"))
		}

		resp := BatchResponse{
			ID:               "batch_123",
			Type:             "message_batch",
			ProcessingStatus: "in_progress",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	q := NewBatchQueue("test-key")
	q.baseURL = server.URL
	q.maxSize = 100

	q.Enqueue(context.Background(), "req_1", map[string]any{"model": "haiku"})
	q.Enqueue(context.Background(), "req_2", map[string]any{"model": "haiku"})

	err := q.Flush(context.Background())
	if err != nil {
		t.Fatalf("flush error: %v", err)
	}

	if q.Pending() != 0 {
		t.Errorf("expected 0 pending after flush, got %d", q.Pending())
	}

	requests, ok := receivedBody["requests"].([]any)
	if !ok || len(requests) != 2 {
		t.Fatalf("expected 2 requests in batch, got %v", receivedBody["requests"])
	}

	ids := q.BatchIDs()
	if len(ids) != 1 || ids[0] != "batch_123" {
		t.Errorf("expected batch ID batch_123, got %v", ids)
	}
}

func TestBatchQueue_AutoFlushOnMaxSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := BatchResponse{ID: "batch_auto", ProcessingStatus: "in_progress"}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	q := NewBatchQueue("test-key")
	q.baseURL = server.URL
	q.maxSize = 2

	// First enqueue — no flush yet
	q.Enqueue(context.Background(), "req_1", map[string]any{})
	if q.Pending() != 1 {
		t.Errorf("expected 1 pending, got %d", q.Pending())
	}

	// Second enqueue — triggers auto-flush
	q.Enqueue(context.Background(), "req_2", map[string]any{})
	if q.Pending() != 0 {
		t.Errorf("expected 0 pending after auto-flush, got %d", q.Pending())
	}

	if len(q.BatchIDs()) != 1 {
		t.Errorf("expected 1 batch ID, got %d", len(q.BatchIDs()))
	}
}

func TestBatchQueue_CheckBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := BatchResponse{
			ID:               "batch_check",
			ProcessingStatus: "ended",
		}
		resp.RequestCounts.Succeeded = 5
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	q := NewBatchQueue("test-key")
	q.baseURL = server.URL

	result, err := q.CheckBatch(context.Background(), "batch_check")
	if err != nil {
		t.Fatalf("check batch error: %v", err)
	}
	if result.ProcessingStatus != "ended" {
		t.Errorf("expected ended, got %s", result.ProcessingStatus)
	}
	if result.RequestCounts.Succeeded != 5 {
		t.Errorf("expected 5 succeeded, got %d", result.RequestCounts.Succeeded)
	}
}
