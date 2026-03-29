// Package crosssquad implements cross-squad request routing — agents can request
// work from other squads and track fulfillment.
package crosssquad

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RequestType classifies the kind of work being requested.
type RequestType string

const (
	RequestTypeReport RequestType = "report"
	RequestTypeQuery  RequestType = "query"
	RequestTypeReview RequestType = "review"
	RequestTypeFix    RequestType = "fix"
	RequestTypeDeploy RequestType = "deploy"
)

// RequestStatus tracks lifecycle state of a cross-squad request.
type RequestStatus string

const (
	StatusPending   RequestStatus = "pending"
	StatusClaimed   RequestStatus = "claimed"
	StatusFulfilled RequestStatus = "fulfilled"
	StatusExpired   RequestStatus = "expired"
)

// Request is a unit of work sent from one agent to another squad.
type Request struct {
	ID              string        `json:"id"`
	FromAgent       string        `json:"from_agent"`
	ToSquad         string        `json:"to_squad"`
	Type            RequestType   `json:"type"`
	Description     string        `json:"description"`
	Priority        int           `json:"priority"`         // 0=urgent, 1=high, 2=normal
	DeadlineMinutes int           `json:"deadline_minutes"` // 0 = no deadline
	Status          RequestStatus `json:"status"`
	CreatedAt       string        `json:"created_at"`
	DeadlineAt      string        `json:"deadline_at,omitempty"`
	Result          string        `json:"result,omitempty"`
	PRNumber        int           `json:"pr_number,omitempty"`
	FulfilledAt     string        `json:"fulfilled_at,omitempty"`
	FulfilledBy     string        `json:"fulfilled_by,omitempty"`
}

// AgeMinutes returns how long ago the request was created.
func (r *Request) AgeMinutes() int {
	t, err := time.Parse(time.RFC3339, r.CreatedAt)
	if err != nil {
		return 0
	}
	return int(time.Since(t).Minutes())
}

// Store manages cross-squad requests in Redis.
type Store struct {
	rdb *redis.Client
	ns  string
}

// New creates a Store connected to the given Redis instance and namespace.
func New(rdb *redis.Client, namespace string) *Store {
	return &Store{rdb: rdb, ns: namespace}
}

// ttl returns the TTL for a request. Requests with a deadline expire at the
// deadline; otherwise they live for 24 hours so fulfilled items stay visible.
func ttl(deadlineMinutes int) time.Duration {
	if deadlineMinutes > 0 {
		// Keep for 2× the deadline so the requesting agent can read the result.
		return time.Duration(deadlineMinutes*2) * time.Minute
	}
	return 24 * time.Hour
}

// Create stores a new cross-squad request and returns it.
func (s *Store) Create(
	ctx context.Context,
	fromAgent, toSquad string,
	reqType RequestType,
	description string,
	priority, deadlineMinutes int,
) (*Request, error) {
	id := fmt.Sprintf("req-%d", time.Now().UnixNano())
	now := time.Now().UTC()

	req := &Request{
		ID:              id,
		FromAgent:       fromAgent,
		ToSquad:         toSquad,
		Type:            reqType,
		Description:     description,
		Priority:        priority,
		DeadlineMinutes: deadlineMinutes,
		Status:          StatusPending,
		CreatedAt:       now.Format(time.RFC3339),
	}
	if deadlineMinutes > 0 {
		req.DeadlineAt = now.Add(time.Duration(deadlineMinutes) * time.Minute).Format(time.RFC3339)
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// score = unix millis; lower priority number = more urgent = higher score for ZREVRANGE
	score := float64(time.Now().UnixMilli()) - float64(priority)*1e9

	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, s.reqKey(id), data, ttl(deadlineMinutes))
	pipe.ZAdd(ctx, s.squadKey(toSquad), redis.Z{Score: score, Member: id})
	pipe.ZAdd(ctx, s.allKey(), redis.Z{Score: float64(now.UnixMilli()), Member: id})
	_, err = pipe.Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("store request: %w", err)
	}
	return req, nil
}

// GetBySquad returns all live requests for the given squad, most urgent first.
func (s *Store) GetBySquad(ctx context.Context, squad string) ([]Request, error) {
	ids, err := s.rdb.ZRevRange(ctx, s.squadKey(squad), 0, 49).Result()
	if err != nil {
		return nil, err
	}
	return s.loadRequests(ctx, ids)
}

// GetAll returns all live requests across every squad, newest first.
func (s *Store) GetAll(ctx context.Context) ([]Request, error) {
	ids, err := s.rdb.ZRevRange(ctx, s.allKey(), 0, 99).Result()
	if err != nil {
		return nil, err
	}
	return s.loadRequests(ctx, ids)
}

// Fulfill marks a request as fulfilled and records the result.
func (s *Store) Fulfill(ctx context.Context, requestID, fulfilledBy, result string, prNumber int) error {
	data, err := s.rdb.Get(ctx, s.reqKey(requestID)).Bytes()
	if err == redis.Nil {
		return fmt.Errorf("request %s not found", requestID)
	}
	if err != nil {
		return fmt.Errorf("get request: %w", err)
	}

	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal request: %w", err)
	}

	req.Status = StatusFulfilled
	req.Result = result
	req.PRNumber = prNumber
	req.FulfilledAt = time.Now().UTC().Format(time.RFC3339)
	req.FulfilledBy = fulfilledBy

	updated, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal update: %w", err)
	}

	// Keep the fulfilled record visible for 1 hour so the requester can read it.
	return s.rdb.Set(ctx, s.reqKey(requestID), updated, time.Hour).Err()
}

// loadRequests fetches and deserializes requests by ID, skipping expired ones.
func (s *Store) loadRequests(ctx context.Context, ids []string) ([]Request, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = s.reqKey(id)
	}

	vals, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}

	var reqs []Request
	for _, v := range vals {
		if v == nil {
			continue // expired
		}
		raw, ok := v.(string)
		if !ok {
			continue
		}
		var req Request
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			continue
		}
		reqs = append(reqs, req)
	}
	return reqs, nil
}

func (s *Store) reqKey(id string) string   { return s.ns + ":request:" + id }
func (s *Store) squadKey(sq string) string { return s.ns + ":requests:squad:" + sq }
func (s *Store) allKey() string            { return s.ns + ":requests:all" }
