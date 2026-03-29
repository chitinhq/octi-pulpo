package coordination

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RequestType categorises the kind of work being requested.
type RequestType string

const (
	RequestReport RequestType = "report"
	RequestQuery  RequestType = "query"
	RequestReview RequestType = "review"
	RequestFix    RequestType = "fix"
	RequestDeploy RequestType = "deploy"
)

// RequestStatus tracks the lifecycle of a cross-squad request.
type RequestStatus string

const (
	RequestPending   RequestStatus = "pending"
	RequestClaimed   RequestStatus = "claimed"
	RequestFulfilled RequestStatus = "fulfilled"
	RequestEscalated RequestStatus = "escalated"
)

// CrossSquadRequest is a work request from one agent to another squad.
type CrossSquadRequest struct {
	ID              string        `json:"id"`
	FromAgent       string        `json:"from_agent"`
	ToSquad         string        `json:"to_squad"`
	Type            RequestType   `json:"type"`
	Description     string        `json:"description"`
	Priority        int           `json:"priority"`         // 0=urgent, 1=high, 2=normal
	Status          RequestStatus `json:"status"`
	DeadlineMinutes int           `json:"deadline_minutes"` // 0 = no deadline
	CreatedAt       string        `json:"created_at"`
	ClaimedBy       string        `json:"claimed_by,omitempty"`
	ClaimedAt       string        `json:"claimed_at,omitempty"`
	FulfilledAt     string        `json:"fulfilled_at,omitempty"`
	Result          string        `json:"result,omitempty"`
	PRNumber        int           `json:"pr_number,omitempty"`
}

// AgeMinutes returns how many minutes have elapsed since the request was created.
func (r *CrossSquadRequest) AgeMinutes() float64 {
	t, err := time.Parse(time.RFC3339, r.CreatedAt)
	if err != nil {
		return 0
	}
	return time.Since(t).Minutes()
}

// IsOverdue returns true when a deadline was set and has passed.
func (r *CrossSquadRequest) IsOverdue() bool {
	if r.DeadlineMinutes == 0 {
		return false
	}
	return r.AgeMinutes() > float64(r.DeadlineMinutes)
}

// RequestStore manages cross-squad requests in Redis.
type RequestStore struct {
	rdb *redis.Client
	ns  string
}

// NewRequestStore creates a RequestStore backed by the given Redis client.
func NewRequestStore(rdb *redis.Client, namespace string) *RequestStore {
	return &RequestStore{rdb: rdb, ns: namespace}
}

// Submit stores a new cross-squad request and returns it with its generated ID.
func (s *RequestStore) Submit(ctx context.Context, fromAgent, toSquad string, reqType RequestType, description string, priority, deadlineMinutes int) (*CrossSquadRequest, error) {
	now := time.Now().UTC()
	req := &CrossSquadRequest{
		ID:              fmt.Sprintf("req-%s-%d", toSquad, now.UnixNano()),
		FromAgent:       fromAgent,
		ToSquad:         toSquad,
		Type:            reqType,
		Description:     description,
		Priority:        priority,
		Status:          RequestPending,
		DeadlineMinutes: deadlineMinutes,
		CreatedAt:       now.Format(time.RFC3339),
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Score: priority tier * 1e12 + unix_ms (lower score = higher priority, FIFO within tier)
	score := float64(priority)*1e12 + float64(now.UnixMilli())

	ttl := 24 * time.Hour
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, s.key("request:"+req.ID), data, ttl)
	pipe.ZAdd(ctx, s.key("requests:to:"+toSquad), redis.Z{Score: score, Member: req.ID})
	_, err = pipe.Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("store request: %w", err)
	}

	return req, nil
}

// Pending returns open requests for a squad, ordered by priority then age.
func (s *RequestStore) Pending(ctx context.Context, squad string) ([]*CrossSquadRequest, error) {
	ids, err := s.rdb.ZRange(ctx, s.key("requests:to:"+squad), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("list pending: %w", err)
	}

	var out []*CrossSquadRequest
	for _, id := range ids {
		req, err := s.Get(ctx, id)
		if err != nil {
			continue // stale / expired entry
		}
		if req.Status == RequestPending || req.Status == RequestClaimed {
			out = append(out, req)
		}
	}
	return out, nil
}

// Get retrieves a request by ID.
func (s *RequestStore) Get(ctx context.Context, id string) (*CrossSquadRequest, error) {
	data, err := s.rdb.Get(ctx, s.key("request:"+id)).Result()
	if err != nil {
		return nil, fmt.Errorf("get request %s: %w", id, err)
	}
	var req CrossSquadRequest
	if err := json.Unmarshal([]byte(data), &req); err != nil {
		return nil, fmt.Errorf("unmarshal request: %w", err)
	}
	return &req, nil
}

// Claim marks a request as claimed by an agent.
func (s *RequestStore) Claim(ctx context.Context, id, agentID string) (*CrossSquadRequest, error) {
	req, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if req.Status != RequestPending {
		return nil, fmt.Errorf("request %s is not pending (status: %s)", id, req.Status)
	}

	req.Status = RequestClaimed
	req.ClaimedBy = agentID
	req.ClaimedAt = time.Now().UTC().Format(time.RFC3339)

	return req, s.save(ctx, req)
}

// Fulfill marks a request as complete with a result.
func (s *RequestStore) Fulfill(ctx context.Context, id, result string, prNumber int) (*CrossSquadRequest, error) {
	req, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if req.Status == RequestFulfilled {
		return nil, fmt.Errorf("request %s is already fulfilled", id)
	}

	req.Status = RequestFulfilled
	req.Result = result
	req.PRNumber = prNumber
	req.FulfilledAt = time.Now().UTC().Format(time.RFC3339)

	if err := s.save(ctx, req); err != nil {
		return nil, err
	}

	// Remove from the pending queue
	s.rdb.ZRem(ctx, s.key("requests:to:"+req.ToSquad), id)
	return req, nil
}

// Escalate flags a request as escalated (deadline passed without completion).
func (s *RequestStore) Escalate(ctx context.Context, id string) error {
	req, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	req.Status = RequestEscalated
	return s.save(ctx, req)
}

func (s *RequestStore) save(ctx context.Context, req *CrossSquadRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	ttl := s.rdb.TTL(ctx, s.key("request:"+req.ID)).Val()
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return s.rdb.Set(ctx, s.key("request:"+req.ID), data, ttl).Err()
}

func (s *RequestStore) key(suffix string) string {
	return s.ns + ":" + suffix
}
