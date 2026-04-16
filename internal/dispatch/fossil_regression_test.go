package dispatch

import (
	"strings"
	"testing"
)

// deadAgents are squad-era agent names excised in octi#271 Phase 1.
// Any of these reappearing in DefaultChains, DefaultRules, or the
// signal-watcher fixtures means the fossil regrew.
var deadAgents = []string{
	"kernel-em", "cloud-em", "platform-em", "analytics-em",
	"shellforge-em", "octi-pulpo-em", "studio-em",
	"marketing-em", "design-em", "site-em", "qa-em",
	"jared-conductor", "director", "hq-em",
	"octi-pulpo-sr", "studio-sr", "office-sim-sr", "cloud-sr",
	"octi-pulpo-qa", "studio-qa", "office-sim-qa", "cloud-qa",
}

// TestNoFossilAgentsInChains asserts DefaultChains has no entries for
// squad-era agents and no chain edge dispatches one. See octi#271.
func TestNoFossilAgentsInChains(t *testing.T) {
	chains := DefaultChains()
	deadSet := make(map[string]bool, len(deadAgents))
	for _, a := range deadAgents {
		deadSet[a] = true
	}

	for _, dead := range deadAgents {
		if _, ok := chains[dead]; ok {
			t.Errorf("fossil agent %q found as DefaultChains key — squads are dead (see octi#271)", dead)
		}
	}

	for src, action := range chains {
		for _, bucket := range [][]string{action.OnSuccess, action.OnFailure, action.OnCommit} {
			for _, target := range bucket {
				if deadSet[target] {
					t.Errorf("chain %q -> fossil %q (see octi#271)", src, target)
				}
			}
		}
	}
}

// TestNoFossilTimersInRules asserts DefaultRules registers no timer
// for a squad-era agent, and that every timer rule's agent has a
// prefix matching a live repo. See octi#271.
func TestNoFossilTimersInRules(t *testing.T) {
	rules := DefaultRules()
	deadSet := make(map[string]bool, len(deadAgents))
	for _, a := range deadAgents {
		deadSet[a] = true
	}

	for _, rule := range rules {
		if rule.EventType != EventTimer {
			continue
		}
		if deadSet[rule.AgentName] {
			t.Errorf("timer rule registers fossil agent %q (see octi#271)", rule.AgentName)
		}

		// Every timer agent name must start with a live-repo prefix.
		// Wildcards and wildcard-like non-repo rules should not be
		// timer-registered.
		matched := false
		for _, repo := range LiveRepos {
			if strings.HasPrefix(rule.AgentName, repo+"-") {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("timer rule for %q has no LiveRepos prefix — timers must bind to a real repo (see octi#271)", rule.AgentName)
		}
	}
}

// TestSrForRepo_UnknownReturnsEmpty locks the contract that Brain
// callers rely on: an unknown repo returns "" so the dispatch is
// skipped. Exercised by brain.go:1280 (P0), :1313 (idle), :1377 (next).
func TestSrForRepo_UnknownReturnsEmpty(t *testing.T) {
	b := &Brain{}
	if got := b.srForRepo("not-a-repo"); got != "" {
		t.Errorf("srForRepo(unknown) = %q, want empty string", got)
	}
	// Spot-check every live repo has a mapping.
	for _, repo := range LiveRepos {
		if got := b.srForRepo(repo); got == "" {
			t.Errorf("srForRepo(%q) is empty — live repo must map to an SR", repo)
		}
	}
}
