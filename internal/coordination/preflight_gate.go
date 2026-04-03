package coordination

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

// requiredPhases are the Preflight phases that must be completed before a
// task can transition from "assigned" to "in_progress".
var requiredPhases = []string{"orient", "clarify", "approach", "confirm"}

// PreflightGate blocks task state transitions unless the required Preflight
// phases have been logged. This enforces design-before-you-build discipline
// across the swarm.
type PreflightGate struct {
	rdb *redis.Client
	ns  string
}

// NewPreflightGate creates a gate backed by Redis.
func NewPreflightGate(rdb *redis.Client, namespace string) *PreflightGate {
	return &PreflightGate{rdb: rdb, ns: namespace}
}

// BlockTransition checks whether a task state transition is allowed.
// Only the "assigned" -> "in_progress" transition is gated; all other
// transitions pass through unconditionally.
//
// The gate inspects the Redis set at key `octi:preflight:{taskID}:phases`
// for completed phase entries. If any required phase is missing, the
// transition is blocked and a reason string explains which phases are absent.
func (pg *PreflightGate) BlockTransition(ctx context.Context, taskID, from, to string) (allowed bool, reason string) {
	// Only gate the specific transition.
	if from != "assigned" || to != "in_progress" {
		return true, ""
	}

	key := pg.key(taskID)

	members, err := pg.rdb.SMembers(ctx, key).Result()
	if err != nil {
		return false, fmt.Sprintf("preflight check failed: %v", err)
	}

	completed := make(map[string]bool, len(members))
	for _, m := range members {
		completed[m] = true
	}

	var missing []string
	for _, phase := range requiredPhases {
		if !completed[phase] {
			missing = append(missing, phase)
		}
	}

	if len(missing) > 0 {
		return false, fmt.Sprintf("preflight phases incomplete: missing %s", strings.Join(missing, ", "))
	}
	return true, ""
}

// LogPhase records a completed Preflight phase for a task.
func (pg *PreflightGate) LogPhase(ctx context.Context, taskID, phase string) error {
	return pg.rdb.SAdd(ctx, pg.key(taskID), phase).Err()
}

// CompletedPhases returns the set of completed phases for a task.
func (pg *PreflightGate) CompletedPhases(ctx context.Context, taskID string) ([]string, error) {
	return pg.rdb.SMembers(ctx, pg.key(taskID)).Result()
}

func (pg *PreflightGate) key(taskID string) string {
	return pg.ns + ":preflight:" + taskID + ":phases"
}
