package budget

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// AgentBudget tracks per-agent monthly spending limits.
type AgentBudget struct {
	Agent              string `json:"agent"`
	Driver             string `json:"driver"`
	Box                string `json:"box"`
	BudgetMonthlyCents int    `json:"budget_monthly_cents"`
	SpentMonthlyCents  int    `json:"spent_monthly_cents"`
	RunsThisMonth      int    `json:"runs_this_month"`
	LastRunAt          string `json:"last_run_at,omitempty"`
	Paused             bool   `json:"paused"`
}

// priorityThresholds defines the minimum remaining-budget fraction required
// for each priority level. Lower thresholds allow spending deeper into the budget.
var priorityThresholds = map[string]float64{
	"CRITICAL":   0.0,
	"HIGH":       0.15,
	"NORMAL":     0.30,
	"BACKGROUND": 0.50,
}

// BudgetStore manages per-agent budgets in Redis.
type BudgetStore struct {
	rdb       *redis.Client
	namespace string
}

// NewBudgetStore creates a budget store backed by Redis.
func NewBudgetStore(rdb *redis.Client, namespace string) *BudgetStore {
	return &BudgetStore{
		rdb:       rdb,
		namespace: namespace,
	}
}

// checkAndIncrementScript is a Lua script for atomic budget check + increment.
//
// KEYS[1] = budget key
// ARGV[1] = cost_cents (int)
// ARGV[2] = priority threshold (float, 0.0 for CRITICAL)
// ARGV[3] = timestamp (string)
// ARGV[4] = is_critical (1 or 0)
//
// Returns 1 if allowed, 0 if denied.
var checkAndIncrementScript = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
if not raw then
  return 0
end

local data = cjson.decode(raw)
local cost = tonumber(ARGV[1])
local threshold = tonumber(ARGV[2])
local timestamp = ARGV[3]
local is_critical = tonumber(ARGV[4])

-- CRITICAL bypasses everything: increment and return 1
if is_critical == 1 then
  data.spent_monthly_cents = data.spent_monthly_cents + cost
  data.runs_this_month = data.runs_this_month + 1
  data.last_run_at = timestamp
  redis.call('SET', KEYS[1], cjson.encode(data))
  return 1
end

-- If paused, deny
if data.paused then
  return 0
end

local budget = data.budget_monthly_cents
local spent = data.spent_monthly_cents

-- If budget is 0, deny (avoid division by zero)
if budget <= 0 then
  data.paused = true
  redis.call('SET', KEYS[1], cjson.encode(data))
  return 0
end

local remaining_frac = (budget - spent) / budget

-- If no remaining budget, auto-pause and deny
if remaining_frac <= 0 then
  data.paused = true
  redis.call('SET', KEYS[1], cjson.encode(data))
  return 0
end

-- Check if remaining fraction meets the priority threshold
if remaining_frac <= threshold then
  return 0
end

-- Allowed: increment spent, runs, and last_run_at
data.spent_monthly_cents = spent + cost
data.runs_this_month = data.runs_this_month + 1
data.last_run_at = timestamp
redis.call('SET', KEYS[1], cjson.encode(data))
return 1
`)

// SetBudget stores an agent budget in Redis.
func (bs *BudgetStore) SetBudget(ctx context.Context, budget AgentBudget) error {
	data, err := json.Marshal(budget)
	if err != nil {
		return fmt.Errorf("marshal budget for %s: %w", budget.Agent, err)
	}
	return bs.rdb.Set(ctx, bs.key(budget.Agent), data, 0).Err()
}

// GetBudget retrieves an agent budget from Redis.
func (bs *BudgetStore) GetBudget(ctx context.Context, agent string) (AgentBudget, error) {
	raw, err := bs.rdb.Get(ctx, bs.key(agent)).Result()
	if err != nil {
		return AgentBudget{}, fmt.Errorf("get budget for %s: %w", agent, err)
	}

	var budget AgentBudget
	if err := json.Unmarshal([]byte(raw), &budget); err != nil {
		return AgentBudget{}, fmt.Errorf("parse budget for %s: %w", agent, err)
	}
	return budget, nil
}

// CheckAndIncrement atomically checks whether an agent has budget remaining
// for the given priority level, and if so, increments the spent amount.
// Uses a Redis Lua script for atomicity.
func (bs *BudgetStore) CheckAndIncrement(ctx context.Context, agent string, costCents int, priority string) (bool, error) {
	threshold, ok := priorityThresholds[priority]
	if !ok {
		threshold = priorityThresholds["NORMAL"]
	}

	isCritical := 0
	if priority == "CRITICAL" {
		isCritical = 1
	}

	timestamp := time.Now().UTC().Format(time.RFC3339)
	result, err := checkAndIncrementScript.Run(ctx, bs.rdb,
		[]string{bs.key(agent)},
		costCents, threshold, timestamp, isCritical,
	).Int()
	if err != nil {
		return false, fmt.Errorf("check and increment for %s: %w", agent, err)
	}

	return result == 1, nil
}

// ListAll returns all agent budgets stored in this namespace.
// Returns an empty slice when there are no budgets configured.
func (bs *BudgetStore) ListAll(ctx context.Context) ([]AgentBudget, error) {
	pattern := bs.namespace + ":budget:*"
	var keys []string
	iter := bs.rdb.Scan(ctx, 0, pattern, 0).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("scan budgets: %w", err)
	}

	budgets := make([]AgentBudget, 0, len(keys))
	for _, k := range keys {
		raw, err := bs.rdb.Get(ctx, k).Result()
		if err != nil {
			continue // key may have expired between SCAN and GET
		}
		var b AgentBudget
		if err := json.Unmarshal([]byte(raw), &b); err != nil {
			continue
		}
		budgets = append(budgets, b)
	}
	return budgets, nil
}

// MonthlyReset zeros out the spent amount, run count, and paused flag for an agent.
func (bs *BudgetStore) MonthlyReset(ctx context.Context, agent string) error {
	budget, err := bs.GetBudget(ctx, agent)
	if err != nil {
		return err
	}

	budget.SpentMonthlyCents = 0
	budget.RunsThisMonth = 0
	budget.Paused = false

	return bs.SetBudget(ctx, budget)
}

// key returns a namespaced Redis key for agent budgets.
func (bs *BudgetStore) key(agent string) string {
	return bs.namespace + ":budget:" + agent
}
