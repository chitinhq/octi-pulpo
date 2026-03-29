// Package crosssquad provides cross-squad work request routing.
// Agents from any squad can request work from another squad's SR via
// request_work, and target SRs can check_requests and fulfill_request.
package crosssquad

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Status values for cross-squad requests.
const (
	StatusPending   = "pending"
	StatusClaimed   = "claimed"
	StatusFulfilled = "fulfilled"
	StatusEscalated = "escalated"
)

// ValidTypes is the set of accepted request types.
var ValidTypes = map[string]bool{
	"report": true,
	"query":  true,
	"review": true,
	"fix":    true,
	"deploy": true,
}

// Request is a cross-squad work request.
type Request struct {
	ID              string `json:"id"`
	FromAgent       string `json:"from_agent"`
	ToSquad         string `json:"to_squad"`
	Type            string `json:"type"`
	Description     string `json:"description"`
	Priority        int    `json:"priority"`        // 0=urgent, 1=high, 2=normal
	DeadlineMinutes int    `json:"deadline_minutes,omitempty"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
	FulfilledAt     string `json:"fulfilled_at,omitempty"`
	Result          string `json:"result,omitempty"`
	PRNumber        int    `json:"pr_number,omitempty"`
	AgeMinutes      int    `json:"age_minutes,omitempty"` // computed on read, not persisted
}

// Store manages cross-squad requests in Redis.
type Store struct {
	rdb *redis.Client
	ns  string
}

// New creates a cross-squad request store backed by the given Redis client.
func New(rdb *redis.Client, namespace string) *Store {
	return &Store{rdb: rdb, ns: namespace}
}

func (s *Store) key(suffix string) string {
	return s.ns + ":xsquad:" + suffix
}

// Create stores a new cross-squad request. Returns the generated request.
func (s *Store) Create(ctx context.Context, fromAgent, toSquad, reqType, description string, priority, deadlineMinutes int) (*Request, error) {
	if !ValidTypes[reqType] {
		return nil, fmt.Errorf("invalid request type %q: must be one of report, query, review, fix, deploy", reqType)
	}
	if priority < 0 || priority > 2 {
		priority = 2
	}

	id := fmt.Sprintf("req-%d", time.Now().UnixMilli())
	req := &Request{
		ID:              id,
		FromAgent:       fromAgent,
		ToSquad:         toSquad,
		Type:            reqType,
		Description:     description,
		Priority:        priority,
		DeadlineMinutes: deadlineMinutes,
		Status:          StatusPending,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	pipe := s.rdb.Pipeline()
	// Store request body, retained for 7 days
	pipe.Set(ctx, s.key("request:"+id), data, 7*24*time.Hour)
	// Add to the target squad's pending sorted set — lower score = higher priority
	pipe.ZAdd(ctx, s.key("pending:"+toSquad), redis.Z{
		Score:  float64(priority),
		Member: id,
	})
	_, err = pipe.Exec(ctx)
	return req, err
}

// List returns all pending/claimed requests for a squad, sorted by priority.
// Fulfilled and escalated entries are cleaned up on read.
func (s *Store) List(ctx context.Context, toSquad string) ([]Request, error) {
	ids, err := s.rdb.ZRange(ctx, s.key("pending:"+toSquad), 0, -1).Result()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var requests []Request

	for _, id := range ids {
		raw, err := s.rdb.Get(ctx, s.key("request:"+id)).Result()
		if err != nil {
			// Key expired — remove from sorted set
			s.rdb.ZRem(ctx, s.key("pending:"+toSquad), id)
			continue
		}
		var req Request
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			continue
		}
		// Clean up terminal states from the pending set
		if req.Status == StatusFulfilled || req.Status == StatusEscalated {
			s.rdb.ZRem(ctx, s.key("pending:"+toSquad), id)
			continue
		}
		// Compute age for display
		if t, err := time.Parse(time.RFC3339, req.CreatedAt); err == nil {
			req.AgeMinutes = int(now.Sub(t).Minutes())
		}
		// Escalate overdue requests
		if req.DeadlineMinutes > 0 && req.AgeMinutes > req.DeadlineMinutes && req.Status == StatusPending {
			req.Status = StatusEscalated
			s.save(ctx, &req)
			s.rdb.ZRem(ctx, s.key("pending:"+toSquad), id)
			continue
		}
		requests = append(requests, req)
	}
	return requests, nil
}

// Fulfill marks a request as completed.
func (s *Store) Fulfill(ctx context.Context, requestID, result string, prNumber int) (*Request, error) {
	raw, err := s.rdb.Get(ctx, s.key("request:"+requestID)).Result()
	if err != nil {
		return nil, fmt.Errorf("request %s not found", requestID)
	}
	var req Request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return nil, err
	}

	req.Status = StatusFulfilled
	req.Result = result
	req.FulfilledAt = time.Now().UTC().Format(time.RFC3339)
	req.PRNumber = prNumber

	if err := s.save(ctx, &req); err != nil {
		return nil, err
	}
	// Remove from pending set
	s.rdb.ZRem(ctx, s.key("pending:"+req.ToSquad), requestID)
	return &req, nil
}

// PendingSquads returns the names of squads that have at least one pending request.
func (s *Store) PendingSquads(ctx context.Context) ([]string, error) {
	prefix := s.key("pending:")
	keys, err := s.rdb.Keys(ctx, prefix+"*").Result()
	if err != nil {
		return nil, err
	}

	var squads []string
	for _, k := range keys {
		count, _ := s.rdb.ZCard(ctx, k).Result()
		if count > 0 {
			squads = append(squads, k[len(prefix):])
		}
	}
	return squads, nil
}

// save persists a request body back to Redis, preserving the existing TTL.
func (s *Store) save(ctx context.Context, req *Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	ttl, _ := s.rdb.TTL(ctx, s.key("request:"+req.ID)).Result()
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	return s.rdb.Set(ctx, s.key("request:"+req.ID), data, ttl).Err()
}
