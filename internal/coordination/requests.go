package coordination

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RequestType categorises cross-squad requests.
type RequestType string

const (
	RequestTypeReport RequestType = "report"
	RequestTypeQuery  RequestType = "query"
	RequestTypeReview RequestType = "review"
	RequestTypeFix    RequestType = "fix"
	RequestTypeDeploy RequestType = "deploy"
)

// RequestStatus is the lifecycle state of a cross-squad request.
type RequestStatus string

const (
	RequestStatusPending   RequestStatus = "pending"
	RequestStatusClaimed   RequestStatus = "claimed"
	RequestStatusFulfilled RequestStatus = "fulfilled"
	RequestStatusExpired   RequestStatus = "expired"
)

// defaultRequestTTL is applied when no deadline_minutes is specified.
const defaultRequestTTL = 24 * time.Hour

// CrossSquadRequest is a unit of cross-squad work.
type CrossSquadRequest struct {
	ID              string        `json:"id"`
	FromAgent       string        `json:"from_agent"`
	ToSquad         string        `json:"to_squad"`
	Type            RequestType   `json:"type"`
	Description     string        `json:"description"`
	Priority        int           `json:"priority"`         // 0=urgent, 1=high, 2=normal
	DeadlineMinutes int           `json:"deadline_minutes"` // 0 = default (24h)
	Status          RequestStatus `json:"status"`
	SubmittedAt     string        `json:"submitted_at"`
	FulfilledAt     string        `json:"fulfilled_at,omitempty"`
	FulfilledBy     string        `json:"fulfilled_by,omitempty"`
	Result          string        `json:"result,omitempty"`
	PRNumber        int           `json:"pr_number,omitempty"`
	AgeMinutes      int           `json:"age_minutes,omitempty"` // computed on read
}

// SubmitRequest stores a new cross-squad request and returns it.
func (e *Engine) SubmitRequest(ctx context.Context, fromAgent, toSquad string, reqType RequestType, description string, priority, deadlineMinutes int) (*CrossSquadRequest, error) {
	now := time.Now().UTC()
	id := fmt.Sprintf("req-%s-%d", fromAgent, now.UnixMilli())

	ttl := defaultRequestTTL
	if deadlineMinutes > 0 {
		ttl = time.Duration(deadlineMinutes) * time.Minute
	}

	req := &CrossSquadRequest{
		ID:              id,
		FromAgent:       fromAgent,
		ToSquad:         toSquad,
		Type:            reqType,
		Description:     description,
		Priority:        priority,
		DeadlineMinutes: deadlineMinutes,
		Status:          RequestStatusPending,
		SubmittedAt:     now.Format(time.RFC3339),
	}
	data, _ := json.Marshal(req)

	pipe := e.rdb.Pipeline()
	// Store individual request with TTL.
	pipe.Set(ctx, e.key("request:"+id), data, ttl)
	// Track in the squad's pending set (score = timestamp for age-based ordering).
	pipe.ZAdd(ctx, e.key("squad-requests:"+toSquad), redis.Z{
		Score:  float64(now.UnixMilli()),
		Member: id,
	})
	_, err := pipe.Exec(ctx)
	if err != nil {
		return nil, err
	}
	return req, nil
}

// GetPendingRequests returns all non-fulfilled requests for a squad, oldest first.
func (e *Engine) GetPendingRequests(ctx context.Context, squad string) ([]CrossSquadRequest, error) {
	ids, err := e.rdb.ZRange(ctx, e.key("squad-requests:"+squad), 0, -1).Result()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var requests []CrossSquadRequest
	for _, id := range ids {
		raw, err := e.rdb.Get(ctx, e.key("request:"+id)).Result()
		if err == redis.Nil {
			// TTL expired — clean up orphan from set.
			e.rdb.ZRem(ctx, e.key("squad-requests:"+squad), id)
			continue
		}
		if err != nil {
			continue
		}

		var req CrossSquadRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			continue
		}
		if req.Status == RequestStatusFulfilled {
			continue
		}

		// Compute age.
		if t, err := time.Parse(time.RFC3339, req.SubmittedAt); err == nil {
			req.AgeMinutes = int(now.Sub(t).Minutes())
		}
		requests = append(requests, req)
	}
	return requests, nil
}

// FulfillRequest marks a request as fulfilled and notifies the requesting agent.
func (e *Engine) FulfillRequest(ctx context.Context, requestID, agentID, result string, prNumber int) error {
	raw, err := e.rdb.Get(ctx, e.key("request:"+requestID)).Result()
	if err == redis.Nil {
		return fmt.Errorf("request %s not found or expired", requestID)
	}
	if err != nil {
		return err
	}

	var req CrossSquadRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return fmt.Errorf("unmarshal request: %w", err)
	}
	if req.Status == RequestStatusFulfilled {
		return fmt.Errorf("request %s is already fulfilled", requestID)
	}

	req.Status = RequestStatusFulfilled
	req.FulfilledAt = time.Now().UTC().Format(time.RFC3339)
	req.FulfilledBy = agentID
	req.Result = result
	if prNumber > 0 {
		req.PRNumber = prNumber
	}

	data, _ := json.Marshal(req)

	// Preserve remaining TTL when updating.
	ttl, _ := e.rdb.TTL(ctx, e.key("request:"+requestID)).Result()
	if ttl <= 0 {
		ttl = time.Hour
	}

	pipe := e.rdb.Pipeline()
	pipe.Set(ctx, e.key("request:"+requestID), data, ttl)
	pipe.ZRem(ctx, e.key("squad-requests:"+req.ToSquad), requestID)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return err
	}

	// Notify the requesting agent via coord signal.
	payload := fmt.Sprintf("request %s fulfilled by %s: %s", requestID, agentID, result)
	if prNumber > 0 {
		payload += fmt.Sprintf(" (PR #%d)", prNumber)
	}
	return e.Broadcast(ctx, agentID, "completed", payload)
}
