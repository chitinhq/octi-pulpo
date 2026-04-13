// Package cogrouter is the v0 Cognitive Router: rules-first mapping from a
// TaskContext to an execution Decision (soul + body_profile + chitin_profile
// + mode). Tracks octi#196 — v0 ships admission control; scoring (v1) and a
// learned ranker (v2) are deliberately deferred.
//
// Design bias (Jared lens):
//   - deterministic control around probabilistic work: rules decide, LLMs reason
//   - first match wins in v0; no scoring engine yet
//   - telemetry from day one: every Route emits flow.octi.router.route
package cogrouter

// TaskContext is the input to the router. Mirrors the spec in octi#196; only
// fields used by v0 rule-matching are honored today.
type TaskContext struct {
	ID              string   `json:"id,omitempty"`
	Type            string   `json:"type,omitempty"`      // debugging, algorithmic, architecture, telemetry, benchmark, refactor
	Risk            string   `json:"risk,omitempty"`      // low, medium, high, critical
	Ambiguity       string   `json:"ambiguity,omitempty"` // low, medium, high
	TouchedPaths    []string `json:"touched_paths,omitempty"`
	Urgency         string   `json:"urgency,omitempty"`
	HistoricalHints []string `json:"historical_hints,omitempty"`
}

// Decision is the router's output: an execution contract.
type Decision struct {
	TaskID        string   `json:"task_id,omitempty"`
	Soul          string   `json:"soul"`
	BodyProfile   string   `json:"body_profile"`
	ChitinProfile string   `json:"chitin_profile"`
	Mode          string   `json:"mode"`
	Confidence    float64  `json:"confidence"`
	RequireReview bool     `json:"require_review,omitempty"`
	Rationale     []string `json:"rationale"`
	RuleID        string   `json:"rule_id,omitempty"`
}

// Match is the per-rule match criteria. All non-empty fields must match; path
// prefixes match if any TouchedPath has the listed prefix.
type Match struct {
	Type         string   `yaml:"type,omitempty" json:"type,omitempty"`
	Risk         string   `yaml:"risk,omitempty" json:"risk,omitempty"`
	Ambiguity    string   `yaml:"ambiguity,omitempty" json:"ambiguity,omitempty"`
	PathPrefixes []string `yaml:"path_prefixes,omitempty" json:"path_prefixes,omitempty"`
}

// Assign is the decision payload a rule applies on match.
type Assign struct {
	Soul          string `yaml:"soul" json:"soul"`
	BodyProfile   string `yaml:"body_profile" json:"body_profile"`
	ChitinProfile string `yaml:"chitin_profile" json:"chitin_profile"`
	Mode          string `yaml:"mode" json:"mode"`
	RequireReview bool   `yaml:"require_review,omitempty" json:"require_review,omitempty"`
}

// Rule is one entry in router.yaml. First matching rule wins in v0.
type Rule struct {
	ID     string `yaml:"id" json:"id"`
	When   Match  `yaml:"when" json:"when"`
	Assign Assign `yaml:"assign" json:"assign"`
}

// Config is the top-level router.yaml shape.
type Config struct {
	Version  int    `yaml:"version" json:"version"`
	Default  Assign `yaml:"default" json:"default"`
	Rules    []Rule `yaml:"rules" json:"rules"`
}
