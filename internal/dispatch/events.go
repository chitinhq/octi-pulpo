package dispatch

import (
	"path/filepath"
	"time"
)

// EventType classifies the trigger for a dispatch.
type EventType string

const (
	EventIssueOpened  EventType = "issue.opened"
	EventPROpened     EventType = "pr.opened"
	EventPRUpdated    EventType = "pr.updated"
	EventPRLabeled    EventType = "pr.labeled"
	EventIssueLabeled EventType = "issue.labeled"
	EventCICompleted  EventType = "ci.completed"
	EventTimer        EventType = "timer"         // replaces cron
	EventBudgetChange EventType = "budget.change"
	EventManual       EventType = "manual"
	EventSlackAction  EventType = "slack.action"
	EventCompletion   EventType = "completion"    // agent finished, trigger chain
	EventSignal       EventType = "signal"        // agent broadcast a signal
	EventPush         EventType = "push"          // git push to a repo
)

// Event is the core unit of work entering the dispatcher.
type Event struct {
	Type     EventType         `json:"type"`
	Source   string            `json:"source"`   // "github", "cron", "slack", "manual"
	Repo     string            `json:"repo"`     // "chitinhq/kernel"
	Payload  map[string]string `json:"payload"`  // event-specific data
	Priority int               `json:"priority"` // 0=critical, 1=high, 2=normal, 3=background
}

// RequiresRepo returns true if the event type is meaningless without a
// Repo — adapter CanAccept() rejects empty-repo dispatches, so events
// missing a Repo evaporate silently. System-wide events (pure timers,
// backpressure recovery, budget changes, slack meta-commands, raw
// signals) are exempt. Used by the dispatcher to fail loudly with
// telemetry instead of letting the dispatch vanish (workspace#408).
func (e Event) RequiresRepo() bool {
	switch e.Type {
	case EventIssueOpened, EventIssueLabeled,
		EventPROpened, EventPRUpdated, EventPRLabeled,
		EventCICompleted, EventPush:
		return true
	case EventType("brain.leverage"):
		// brain.leverage always targets a specific repo; empty Repo
		// means the producer forgot to populate it.
		return true
	}
	return false
}

// EventRule maps an event pattern to an agent that should handle it.
type EventRule struct {
	EventType EventType     // which event triggers this rule
	RepoMatch string        // glob pattern, e.g. "chitinhq/*"
	AgentName string        // agent to dispatch
	Priority  int           // default priority for this rule
	Cooldown  time.Duration // minimum time between dispatches for same agent
}

// EventRouter maps incoming events to the agents that should handle them.
type EventRouter struct {
	rules []EventRule
}

// NewEventRouter creates a router with the given rules.
func NewEventRouter(rules []EventRule) *EventRouter {
	return &EventRouter{rules: rules}
}

// Match returns all rules that match the given event.
func (er *EventRouter) Match(event Event) []EventRule {
	var matched []EventRule
	for _, rule := range er.rules {
		if rule.EventType != event.Type {
			continue
		}
		if rule.RepoMatch != "" && event.Repo != "" {
			ok, _ := filepath.Match(rule.RepoMatch, event.Repo)
			if !ok {
				continue
			}
		}
		matched = append(matched, rule)
	}
	return matched
}

// CooldownFor returns the cooldown duration for an agent.
// Returns the longest cooldown if multiple rules reference the same agent.
func (er *EventRouter) CooldownFor(agentName string) time.Duration {
	var longest time.Duration
	for _, rule := range er.rules {
		if rule.AgentName == agentName && rule.Cooldown > longest {
			longest = rule.Cooldown
		}
	}
	return longest
}

// DefaultRules returns the standard event routing rules matching
// the existing webhook-listener.py mappings and schedule.json timers.
func DefaultRules() []EventRule {
	return []EventRule{
		// PR review agents (triggered by PR events)
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/kernel",
			AgentName: "workspace-pr-review-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
		{
			EventType: EventPRUpdated,
			RepoMatch: "chitinhq/kernel",
			AgentName: "workspace-pr-review-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/cloud",
			AgentName: "code-review-agent-cloud",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
		{
			EventType: EventPRUpdated,
			RepoMatch: "chitinhq/cloud",
			AgentName: "code-review-agent-cloud",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/analytics",
			AgentName: "analytics-pr-review-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/workspace",
			AgentName: "workspace-pr-review-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},

		// PR merger agents (triggered by CI completion)
		{
			EventType: EventCICompleted,
			RepoMatch: "chitinhq/kernel",
			AgentName: "pr-merger-agent",
			Priority:  2,
			Cooldown:  10 * time.Minute, // prevents stampede (was 214 runs/day)
		},
		{
			EventType: EventCICompleted,
			RepoMatch: "chitinhq/cloud",
			AgentName: "pr-merger-agent-cloud",
			Priority:  2,
			Cooldown:  10 * time.Minute,
		},
		{
			EventType: EventCICompleted,
			RepoMatch: "chitinhq/analytics",
			AgentName: "analytics-pr-review-agent",
			Priority:  2,
			Cooldown:  10 * time.Minute,
		},
		{
			EventType: EventCICompleted,
			RepoMatch: "chitinhq/workspace",
			AgentName: "pr-merger-agent",
			Priority:  2,
			Cooldown:  10 * time.Minute,
		},

		// Timer-based agents (replacing blind cron)
		{
			EventType: EventTimer,
			RepoMatch: "",
			AgentName: "kernel-sr",
			Priority:  2,
			Cooldown:  3 * time.Hour,
		},
		{
			EventType: EventTimer,
			RepoMatch: "",
			AgentName: "kernel-em",
			Priority:  1,
			Cooldown:  6 * time.Hour,
		},
		{
			EventType: EventTimer,
			RepoMatch: "",
			AgentName: "cloud-em",
			Priority:  1,
			Cooldown:  6 * time.Hour,
		},
		{
			EventType: EventTimer,
			RepoMatch: "",
			AgentName: "platform-em",
			Priority:  1,
			Cooldown:  6 * time.Hour,
		},
		{
			EventType: EventTimer,
			RepoMatch: "",
			AgentName: "analytics-em",
			Priority:  1,
			Cooldown:  6 * time.Hour,
		},

		// Manual and Slack triggers (no cooldown -- explicit human action)
		{
			EventType: EventManual,
			RepoMatch: "",
			AgentName: "*", // wildcard -- manual can trigger any agent
			Priority:  0,
			Cooldown:  0,
		},
		{
			EventType: EventSlackAction,
			RepoMatch: "",
			AgentName: "*",
			Priority:  1,
			Cooldown:  2 * time.Minute,
		},
	}
}
