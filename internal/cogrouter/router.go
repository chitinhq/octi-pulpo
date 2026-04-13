package cogrouter

import (
	"fmt"

	"github.com/chitinhq/octi-pulpo/internal/flow"
)

// Router routes TaskContexts to Decisions using a loaded rules Config.
type Router struct {
	cfg *Config
}

// New constructs a Router. cfg must be non-nil (use LoadRules).
func New(cfg *Config) (*Router, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cogrouter: nil config")
	}
	return &Router{cfg: cfg}, nil
}

// Route evaluates rules in order, first match wins. If no rule matches, the
// config's default Assign is used. Always emits flow.octi.router.route.
func (r *Router) Route(ctx TaskContext) (Decision, error) {
	if r == nil || r.cfg == nil {
		return Decision{}, fmt.Errorf("cogrouter: router not initialized")
	}

	var (
		d       Decision
		matched *Rule
	)

	for i := range r.cfg.Rules {
		rule := r.cfg.Rules[i]
		if rule.matches(ctx) {
			matched = &rule
			break
		}
	}

	if matched != nil {
		d = Decision{
			TaskID:        ctx.ID,
			Soul:          matched.Assign.Soul,
			BodyProfile:   matched.Assign.BodyProfile,
			ChitinProfile: matched.Assign.ChitinProfile,
			Mode:          matched.Assign.Mode,
			Confidence:    1.0,
			RequireReview: matched.Assign.RequireReview,
			RuleID:        matched.ID,
			Rationale:     []string{fmt.Sprintf("rule %s matched", matched.ID)},
		}
	} else {
		d = Decision{
			TaskID:        ctx.ID,
			Soul:          r.cfg.Default.Soul,
			BodyProfile:   r.cfg.Default.BodyProfile,
			ChitinProfile: r.cfg.Default.ChitinProfile,
			Mode:          r.cfg.Default.Mode,
			Confidence:    1.0,
			RequireReview: r.cfg.Default.RequireReview,
			RuleID:        "default",
			Rationale:     []string{"no rule matched; applied default"},
		}
	}

	flow.Emit("octi.router.route", flow.StatusCompleted, map[string]interface{}{
		"task_id":        d.TaskID,
		"task_type":      ctx.Type,
		"risk":           ctx.Risk,
		"ambiguity":      ctx.Ambiguity,
		"soul":           d.Soul,
		"body_profile":   d.BodyProfile,
		"chitin_profile": d.ChitinProfile,
		"mode":           d.Mode,
		"rule_id":        d.RuleID,
		"require_review": d.RequireReview,
	})

	return d, nil
}
