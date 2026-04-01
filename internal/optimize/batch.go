package optimize

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	defaultBatchFlushInterval = 5 * time.Minute
	defaultBatchMaxSize       = 10
	batchAPIURL               = "https://api.anthropic.com/v1/messages/batches"
	anthropicVersion          = "2023-06-01"
)

// BatchRequest is a single request within a batch.
type BatchRequest struct {
	CustomID string         `json:"custom_id"`
	Params   map[string]any `json:"params"`
}

// BatchResponse is the API response for creating a batch.
type BatchResponse struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	ProcessingStatus string `json:"processing_status"`
	RequestCounts  struct {
		Processing int `json:"processing"`
		Succeeded  int `json:"succeeded"`
		Errored    int `json:"errored"`
		Canceled   int `json:"canceled"`
		Expired    int `json:"expired"`
	} `json:"request_counts"`
	CreatedAt string `json:"created_at"`
}

// BatchQueue accumulates requests and flushes them as batches.
type BatchQueue struct {
	mu            sync.Mutex
	requests      []BatchRequest
	maxSize       int
	flushInterval time.Duration
	apiKey        string
	baseURL       string
	client        *http.Client
	lastFlush     time.Time
	batchIDs      []string // IDs of submitted batches
}

// NewBatchQueue creates a batch queue with default settings.
func NewBatchQueue(apiKey string) *BatchQueue {
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	return &BatchQueue{
		maxSize:       defaultBatchMaxSize,
		flushInterval: defaultBatchFlushInterval,
		apiKey:        apiKey,
		baseURL:       batchAPIURL,
		client:        &http.Client{Timeout: 30 * time.Second},
		lastFlush:     time.Now(),
	}
}

// Enqueue adds a request to the batch queue. If the queue reaches maxSize
// or the flush interval has elapsed, the batch is automatically flushed.
func (q *BatchQueue) Enqueue(ctx context.Context, customID string, params map[string]any) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.requests = append(q.requests, BatchRequest{
		CustomID: customID,
		Params:   params,
	})

	// Auto-flush when full or interval elapsed
	if len(q.requests) >= q.maxSize || time.Since(q.lastFlush) >= q.flushInterval {
		return q.flushLocked(ctx)
	}
	return nil
}

// Flush sends all queued requests as a batch. Safe to call even when empty.
func (q *BatchQueue) Flush(ctx context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.flushLocked(ctx)
}

// Pending returns the number of requests waiting to be flushed.
func (q *BatchQueue) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.requests)
}

// BatchIDs returns the IDs of all submitted batches.
func (q *BatchQueue) BatchIDs() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	result := make([]string, len(q.batchIDs))
	copy(result, q.batchIDs)
	return result
}

// CheckBatch polls the status of a batch by ID.
func (q *BatchQueue) CheckBatch(ctx context.Context, batchID string) (*BatchResponse, error) {
	url := q.baseURL + "/" + batchID
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", q.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := q.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("check batch: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("check batch %s: HTTP %d: %s", batchID, resp.StatusCode, string(body))
	}

	var result BatchResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse batch response: %w", err)
	}
	return &result, nil
}

func (q *BatchQueue) flushLocked(ctx context.Context) error {
	if len(q.requests) == 0 {
		return nil
	}

	// Build batch request body
	body, err := json.Marshal(map[string]any{
		"requests": q.requests,
	})
	if err != nil {
		return fmt.Errorf("marshal batch: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", q.baseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create batch request: %w", err)
	}
	req.Header.Set("x-api-key", q.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("submit batch: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("batch API error %d: %s", resp.StatusCode, string(respBody))
	}

	var batchResp BatchResponse
	if err := json.Unmarshal(respBody, &batchResp); err != nil {
		return fmt.Errorf("parse batch response: %w", err)
	}

	q.batchIDs = append(q.batchIDs, batchResp.ID)
	q.requests = q.requests[:0]
	q.lastFlush = time.Now()

	return nil
}
